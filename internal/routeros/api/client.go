package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	conn      net.Conn
	raw       net.Conn
	gate      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	done      chan struct{}
	limits    Limits
	timeout   time.Duration
	sequence  uint64
}

func (*Client) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "RouterOS API-SSL client")
}

func normalizeConfig(config Config) (Config, error) {
	if len(config.Address) > 512 || len(config.ServerName) > 253 || len(config.Username) < 1 || len(config.Username) > 128 || len(config.Password) > 1024 || strings.ContainsAny(config.Username, "\x00\r\n") {
		return config, &Error{Code: CodeConfig}
	}
	host, port, e := net.SplitHostPort(config.Address)
	number, parseErr := strconv.Atoi(port)
	if e != nil || parseErr != nil || number < 1 || number > 65535 || host == "" || strings.ContainsAny(host, "/\\\x00\r\n") {
		return config, &Error{Code: CodeConfig}
	}
	if config.ServerName == "" {
		config.ServerName = host
	}
	if strings.ContainsAny(config.ServerName, "/\\\x00\r\n") {
		return config, &Error{Code: CodeConfig}
	}
	if config.SPKISHA256 != "" {
		raw, e := hex.DecodeString(config.SPKISHA256)
		if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != config.SPKISHA256 {
			return config, &Error{Code: CodeConfig}
		}
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = 5 * time.Second
	}
	if config.CommandTimeout == 0 {
		config.CommandTimeout = 10 * time.Second
	}
	if config.DialTimeout < time.Millisecond || config.DialTimeout > time.Minute || config.CommandTimeout < time.Millisecond || config.CommandTimeout > time.Minute {
		return config, &Error{Code: CodeConfig}
	}
	l := config.Limits
	if l.MaxWordBytes == 0 {
		l.MaxWordBytes = 64 << 10
	}
	if l.MaxSentenceBytes == 0 {
		l.MaxSentenceBytes = 128 << 10
	}
	if l.MaxWords == 0 {
		l.MaxWords = 128
	}
	if l.MaxReplyBytes == 0 {
		l.MaxReplyBytes = 4 << 20
	}
	if l.MaxRecords == 0 {
		l.MaxRecords = 20000
	}
	if l.MaxSentences == 0 {
		l.MaxSentences = 2*l.MaxRecords + 64
	}
	if l.MaxWordBytes < 1 || l.MaxWordBytes > 1<<20 || l.MaxSentenceBytes < 128 || l.MaxSentenceBytes > 2<<20 || l.MaxWords < 4 || l.MaxWords > 256 || l.MaxReplyBytes < 128 || l.MaxReplyBytes > 16<<20 || l.MaxRecords < 1 || l.MaxRecords > 20000 || l.MaxSentences < 1 || l.MaxSentences > 40064 {
		return config, &Error{Code: CodeConfig}
	}
	config.Limits = l
	return config, nil
}
func tlsConfiguration(config Config) *tls.Config {
	c := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.ServerName}
	if config.RootCAs != nil {
		c.RootCAs = config.RootCAs.Clone()
	}
	if config.SPKISHA256 != "" {
		expected, _ := hex.DecodeString(config.SPKISHA256)
		c.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("RouterOS TLS verification failed")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(expected, sum[:]) != 1 {
				return errors.New("RouterOS TLS verification failed")
			}
			return nil
		}
	}
	return c
}
func Dial(ctx context.Context, config Config) (*Client, error) {
	return dial(ctx, config, func(ctx context.Context, address string) (net.Conn, error) {
		dialer := net.Dialer{}
		return dialer.DialContext(ctx, "tcp", address)
	})
}
func dial(ctx context.Context, config Config, connect func(context.Context, string) (net.Conn, error)) (*Client, error) {
	if ctx == nil {
		return nil, &Error{Code: CodeConfig}
	}
	normalized, e := normalizeConfig(config)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, normalized.DialTimeout)
	defer cancel()
	raw, e := connect(ctx, normalized.Address)
	if e != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: CodeCancelled}
		}
		return nil, &Error{Code: CodeTransport}
	}
	secure := tls.Client(raw, tlsConfiguration(normalized))
	if e = secure.HandshakeContext(ctx); e != nil {
		raw.Close()
		if ctx.Err() != nil {
			return nil, &Error{Code: CodeCancelled}
		}
		return nil, &Error{Code: CodeTLS}
	}
	client := &Client{conn: secure, raw: raw, gate: make(chan struct{}, 1), done: make(chan struct{}), limits: normalized.Limits, timeout: normalized.CommandTimeout}
	client.gate <- struct{}{}
	password := append([]byte(nil), config.Password...)
	defer clear(password)
	response, e := client.command(ctx, []string{"/login", "=name=" + config.Username, "=password=" + string(password)}, false, true)
	if e != nil {
		client.Close()
		return nil, &Error{Code: CodeAuthentication}
	}
	if len(response.rows) != 0 || len(response.done) != 0 {
		client.Close()
		return nil, &Error{Code: CodeAuthentication}
	}
	return client, nil
}
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var result error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.done != nil {
			close(c.done)
		}
		if c.raw != nil && c.raw.Close() != nil {
			result = &Error{Code: CodeClosed}
		}
	})
	return result
}

type commandReply struct {
	rows []Record
	done Record
}

func (c *Client) command(ctx context.Context, words []string, mutation, login bool) (out commandReply, resultErr error) {
	out = commandReply{rows: []Record{}, done: Record{}}
	var reader *replyReader
	var commandDeadline time.Time
	records := 0
	limitKind := ""
	deadlineKind := "none"
	printRead := !mutation && !login && len(words) > 0 && strings.HasSuffix(words[0], "/print")
	defer func() {
		if !printRead || resultErr == nil {
			return
		}
		var e *Error
		if !errors.As(resultErr, &e) {
			return
		}
		copy := *e
		if reader != nil {
			copy.ReplyBytes = reader.bytes
			copy.ReplySentences = reader.sentences
			if limitKind == "" {
				limitKind = reader.limitKind
			}
		}
		copy.ReplyRecords = records
		copy.LimitKind = limitKind
		copy.Deadline = ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) || !commandDeadline.IsZero() && !time.Now().Before(commandDeadline)
		if copy.Deadline {
			copy.DeadlineKind = deadlineKind
		} else {
			copy.DeadlineKind = "none"
		}
		resultErr = &copy
	}()
	if ctx == nil {
		return out, &Error{Code: CodeConfig}
	}
	if c == nil || c.conn == nil || c.raw == nil || c.gate == nil || c.done == nil {
		return out, &Error{Code: CodeClosed}
	}
	parentDeadline, hasParentDeadline := ctx.Deadline()
	if hasParentDeadline && !parentDeadline.After(time.Now().Add(c.timeout)) {
		deadlineKind = "context"
	} else {
		deadlineKind = "command"
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return out, &Error{Code: CodeCancelled}
	case <-c.done:
		return out, &Error{Code: CodeClosed}
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if c.closed.Load() {
		return out, &Error{Code: CodeClosed}
	}
	if ctx.Err() != nil {
		return out, &Error{Code: CodeCancelled}
	}
	deadline, _ := ctx.Deadline()
	commandDeadline = deadline
	if e := c.conn.SetDeadline(deadline); e != nil {
		c.Close()
		return out, &Error{Code: CodeTransport}
	}
	watchDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(watchDone) })
	defer func() {
		if !stop() {
			<-watchDone
		}
	}()
	tag := ""
	if !login {
		if c.sequence == ^uint64(0) {
			c.Close()
			return out, &Error{Code: CodeClosed}
		}
		c.sequence++
		tag = "ixp-" + strconv.FormatUint(c.sequence, 10)
		words = append(append([]string(nil), words...), ".tag="+tag)
	}
	frame, e := encodeSentence(words, c.limits)
	if e != nil {
		return out, &Error{Code: CodeLimit}
	}
	defer clear(frame)
	sent := true
	n, writeErr := c.conn.Write(frame)
	if writeErr != nil || n != len(frame) {
		c.Close()
		return commandReply{}, &Error{Code: CodeTransport, Uncertain: mutation}
	}
	reader = &replyReader{r: c.conn, limits: c.limits}
	var trapped *Error
	empty := false
	for {
		words, e := reader.sentence()
		if e != nil {
			c.Close()
			code := CodeTransport
			if errors.Is(e, errProtocol) {
				code = CodeProtocol
			} else if errors.Is(e, errLimit) {
				code = CodeLimit
			} else if ctx.Err() != nil {
				code = CodeCancelled
			}
			return commandReply{}, &Error{Code: code, Uncertain: mutation && sent}
		}
		if len(words) == 0 {
			continue
		}
		response, e := parseReply(words)
		if e != nil {
			c.Close()
			return commandReply{}, &Error{Code: CodeProtocol, Uncertain: mutation && sent}
		}
		if response.kind == "!fatal" {
			c.Close()
			return commandReply{}, &Error{Code: CodeFatal, Uncertain: mutation && sent}
		}
		if response.tag != tag {
			c.Close()
			return commandReply{}, &Error{Code: CodeProtocol, Uncertain: mutation && sent}
		}
		switch response.kind {
		case "!trap":
			category := -1
			if raw, ok := response.values["category"]; ok {
				n, e := strconv.Atoi(raw)
				if e == nil && n >= 0 && n <= 7 {
					category = n
				}
			}
			if trapped == nil {
				trapped = &Error{Code: CodeTrap, Uncertain: mutation, Category: category}
			}
		case "!empty":
			if login || empty || len(out.rows) > 0 || len(response.values) > 0 {
				c.Close()
				return commandReply{}, &Error{Code: CodeProtocol, Uncertain: mutation}
			}
			empty = true
		case "!re":
			if empty || mutation || login {
				c.Close()
				return commandReply{}, &Error{Code: CodeProtocol, Uncertain: mutation}
			}
			if len(out.rows) >= c.limits.MaxRecords {
				limitKind = "records"
				c.Close()
				return commandReply{}, &Error{Code: CodeLimit, Uncertain: mutation}
			}
			out.rows = append(out.rows, response.values)
			records = len(out.rows)
		case "!done":
			if trapped != nil {
				return commandReply{}, trapped
			}
			out.done = response.values
			return out, nil
		}
	}
}
func IsUncertain(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Uncertain
}
