// Package netconf implements the deliberately small NETCONF transaction needed
// to safely stage a Junos candidate, without depending on a remote shell.
package netconf

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const Base = "urn:ietf:params:xml:ns:netconf:base:1.0"
const BaseCapability = "urn:ietf:params:netconf:base:1.0"
const delimiter = "]]>]]>"
const maxFrame = 32 << 20
const candidate = "urn:ietf:params:netconf:capability:candidate:1.0"
const validate10 = "urn:ietf:params:netconf:capability:validate:1.0"
const validate11 = "urn:ietf:params:netconf:capability:validate:1.1"
const confirmed = "urn:ietf:params:netconf:capability:confirmed-commit:1.1"

type Client struct {
	conn         io.ReadWriteCloser
	reader       *bufio.Reader
	capabilities map[string]bool
	id           uint64
	mu           sync.Mutex
}
type rpcReply struct {
	XMLName   xml.Name
	MessageID string    `xml:"message-id,attr"`
	OK        *struct{} `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 ok"`
	Data      *struct {
		Inner string `xml:",innerxml"`
	} `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 data"`
	Errors []struct {
		Type    string `xml:"error-type"`
		Tag     string `xml:"error-tag"`
		Message string `xml:"error-message"`
	} `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 rpc-error"`
}

// Open exchanges only base:1.0 so delimiter framing remains unambiguous even
// when the server also advertises base:1.1. Close the transport on context cancel.
func Open(conn io.ReadWriteCloser) (*Client, error) {
	c := &Client{conn: conn, reader: bufio.NewReader(conn), capabilities: map[string]bool{}}
	hello := `<hello xmlns="` + Base + `"><capabilities><capability>` + BaseCapability + `</capability></capabilities></hello>`
	if err := c.write(hello); err != nil {
		return nil, err
	}
	frame, err := c.read()
	if err != nil {
		return nil, err
	}
	var h struct {
		XMLName      xml.Name
		Capabilities []string `xml:"capabilities>capability"`
	}
	if err = xml.Unmarshal(frame, &h); err != nil {
		return nil, errors.New("NETCONF hello contains invalid XML")
	}
	if h.XMLName.Local != "hello" || h.XMLName.Space != Base {
		return nil, errors.New("NETCONF peer did not return a namespaced hello")
	}
	for _, cap := range h.Capabilities {
		c.capabilities[strings.SplitN(cap, "?", 2)[0]] = true
	}
	if !c.capabilities[BaseCapability] {
		return nil, errors.New("NETCONF peer does not support base:1.0")
	}
	return c, nil
}

// Device diagnostics can echo authentication keys from the candidate or the
// existing running config. Retain only a recognized protocol error category.
type RPCError struct{ Tag string }

func safeErrorTag(tag string) string {
	switch tag {
	case "in-use", "invalid-value", "too-big", "missing-attribute", "bad-attribute", "unknown-attribute", "missing-element", "bad-element", "unknown-element", "unknown-namespace", "access-denied", "lock-denied", "resource-denied", "rollback-failed", "data-exists", "data-missing", "operation-not-supported", "operation-failed", "partial-operation", "malformed-message":
		return tag
	default:
		return "operation-failed"
	}
}

func (e *RPCError) Error() string {
	return "NETCONF rpc-error: " + safeErrorTag(e.Tag) + " (device details withheld because they may contain credentials)"
}

func (c *Client) Close() error { return c.conn.Close() }
func (c *Client) write(s string) error {
	if strings.Contains(s, delimiter) {
		return errors.New("NETCONF end marker in payload")
	}
	if len(s) > maxFrame {
		return errors.New("NETCONF payload too large")
	}
	n, err := io.WriteString(c.conn, s+delimiter)
	if err == nil && n != len(s)+len(delimiter) {
		return io.ErrShortWrite
	}
	return err
}
func (c *Client) read() ([]byte, error) {
	b := make([]byte, 0, 4096)
	for len(b) <= maxFrame {
		v, err := c.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		b = append(b, v)
		if len(b) >= len(delimiter) && string(b[len(b)-len(delimiter):]) == delimiter {
			return b[:len(b)-len(delimiter)], nil
		}
	}
	return nil, errors.New("NETCONF frame exceeds 32 MiB")
}
func (c *Client) call(body string) (rpcReply, error) {
	c.id++
	id := strconv.FormatUint(c.id, 10)
	if err := c.write(`<rpc xmlns="` + Base + `" message-id="` + id + `">` + body + `</rpc>`); err != nil {
		return rpcReply{}, err
	}
	frame, err := c.read()
	if err != nil {
		return rpcReply{}, err
	}
	var reply rpcReply
	if err = xml.Unmarshal(frame, &reply); err != nil {
		return reply, errors.New("NETCONF reply contains invalid XML")
	}
	if reply.XMLName.Local != "rpc-reply" || reply.XMLName.Space != Base || reply.MessageID != id {
		return reply, errors.New("NETCONF reply namespace or message-id mismatch")
	}
	if len(reply.Errors) > 0 {
		return reply, &RPCError{Tag: safeErrorTag(reply.Errors[0].Tag)}
	}
	return reply, nil
}
func (c *Client) ok(body string) error {
	reply, err := c.call(body)
	if err != nil {
		return err
	}
	if reply.OK == nil {
		return errors.New("NETCONF mutation reply lacks ok")
	}
	return nil
}
func (c *Client) requirements() error {
	if !c.capabilities[candidate] || (!c.capabilities[validate10] && !c.capabilities[validate11]) || !c.capabilities[confirmed] {
		return errors.New("target must support candidate, validate and confirmed-commit:1.1")
	}
	return nil
}
func checksum(data string) string { s := sha256.Sum256([]byte(data)); return hex.EncodeToString(s[:]) }
func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func validToken(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}

// Stage locks a CLEAN candidate, loads the reviewed artifact, performs native
// validation and starts a persistent confirmed-commit timer. It never confirms.
// Persist the token/target/checksum receipt before invoking this operation.
type StageOptions struct{ AllowGlobalRoutingChange bool }

func (c *Client) Stage(config, expected, token string, timeout uint32) error {
	return c.StageWithOptions(config, expected, token, timeout, StageOptions{})
}

// StageWithOptions permits deliberate replacement of existing global routing
// identity only when explicitly authorized by the host operator.
func (c *Client) StageWithOptions(config, expected, token string, timeout uint32, options StageOptions) (result error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requirements(); err != nil {
		return err
	}
	if checksum(config) != expected || len(expected) != 64 {
		return errors.New("reviewed configuration checksum mismatch")
	}
	if !validToken(token) || timeout < 60 || timeout > 3600 {
		return errors.New("persist token invalid or timeout outside 60..3600 seconds")
	}
	if err := configXML(config); err != nil {
		return err
	}
	if err := c.ok(`<lock><target><candidate/></target></lock>`); err != nil {
		return err
	}
	edited := false
	committed := false
	defer func() {
		if edited && !committed {
			if err := c.ok(`<discard-changes/>`); err != nil {
				result = errors.Join(result, fmt.Errorf("candidate cleanup failed: %w", err))
			}
		}
		if err := c.ok(`<unlock><target><candidate/></target></unlock>`); err != nil {
			result = errors.Join(result, fmt.Errorf("candidate unlock failed: %w", err))
		}
	}()
	running, err := c.call(`<get-config><source><running/></source></get-config>`)
	if err != nil {
		return err
	}
	proposed, err := c.call(`<get-config><source><candidate/></source></get-config>`)
	if err != nil {
		return err
	}
	if running.Data == nil || proposed.Data == nil {
		return errors.New("NETCONF get-config reply lacks namespaced data")
	}
	current, err := canonical(running.Data.Inner)
	if err != nil {
		return err
	}
	pending, err := canonical(proposed.Data.Inner)
	if err != nil {
		return err
	}
	if current != pending {
		return errors.New("candidate contains preexisting edits; refusing to modify, commit or discard them")
	}
	if !options.AllowGlobalRoutingChange {
		if err = checkGlobalRoutingIdentity(running.Data.Inner, config); err != nil {
			return err
		}
	}
	edited = true
	if err = c.ok(`<edit-config><target><candidate/></target><default-operation>merge</default-operation><config>` + config + `</config></edit-config>`); err != nil {
		return err
	}
	if err = c.ok(`<validate><source><candidate/></source></validate>`); err != nil {
		return err
	}
	// After sending commit, an I/O failure leaves its outcome uncertain. Do not
	// issue discard-changes: the persisted receipt is used to cancel or recover.
	committed = true
	err = c.ok(`<commit><confirmed/><confirm-timeout>` + strconv.FormatUint(uint64(timeout), 10) + `</confirm-timeout><persist>` + token + `</persist></commit>`)
	var rejected *RPCError
	if errors.As(err, &rejected) {
		committed = false
	}
	return err
}
func (c *Client) Confirm(token string) (result error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.requirements(); err != nil {
		return err
	}
	if !validToken(token) {
		return errors.New("invalid persist token")
	}
	if err := c.ok(`<lock><target><candidate/></target></lock>`); err != nil {
		return err
	}
	defer func() {
		if err := c.ok(`<unlock><target><candidate/></target></unlock>`); err != nil {
			result = errors.Join(result, err)
		}
	}()
	running, err := c.call(`<get-config><source><running/></source></get-config>`)
	if err != nil {
		return err
	}
	proposed, err := c.call(`<get-config><source><candidate/></source></get-config>`)
	if err != nil {
		return err
	}
	if running.Data == nil || proposed.Data == nil {
		return errors.New("NETCONF get-config reply lacks namespaced data")
	}
	current, err := canonical(running.Data.Inner)
	if err != nil {
		return err
	}
	pending, err := canonical(proposed.Data.Inner)
	if err != nil {
		return err
	}
	if current != pending {
		return errors.New("candidate changed since stage; refusing to commit unrelated edits")
	}
	return c.ok(`<commit><persist-id>` + token + `</persist-id></commit>`)
}
func (c *Client) Cancel(token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.capabilities[confirmed] || !validToken(token) {
		return errors.New("confirmed-commit:1.1 or valid persist token missing")
	}
	return c.ok(`<cancel-commit><persist-id>` + token + `</persist-id></cancel-commit>`)
}

func configXML(config string) error {
	if len(config) == 0 || len(config) > 16<<20 || strings.Contains(config, delimiter) {
		return errors.New("invalid NETCONF configuration size or framing")
	}
	d := xml.NewDecoder(strings.NewReader(config))
	roots := 0
	depth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("NETCONF configuration contains invalid XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if t.Name.Local != "configuration" || t.Name.Space != "" {
					return errors.New("expected Junos configuration root")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.ProcInst, xml.Directive:
			return errors.New("XML directives are forbidden")
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return errors.New("text outside configuration")
			}
		}
	}
	if roots != 1 {
		return errors.New("expected one configuration root")
	}
	return nil
}
func canonical(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("NETCONF get-config returned no configuration data")
	}
	d := xml.NewDecoder(strings.NewReader(raw))
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("NETCONF device configuration contains invalid XML")
		}
		switch v := tok.(type) {
		case xml.StartElement:
			fmt.Fprintf(&b, "<%q:%q", v.Name.Space, v.Name.Local)
			attrs := append([]xml.Attr(nil), v.Attr...)
			sort.Slice(attrs, func(i, j int) bool {
				a, b := attrs[i], attrs[j]
				return a.Name.Space+":"+a.Name.Local < b.Name.Space+":"+b.Name.Local
			})
			for _, a := range attrs {
				if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
					continue
				}
				fmt.Fprintf(&b, " %q:%q=%q", a.Name.Space, a.Name.Local, a.Value)
			}
			b.WriteString(">")
		case xml.EndElement:
			fmt.Fprintf(&b, "</%q:%q>", v.Name.Space, v.Name.Local)
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				fmt.Fprintf(&b, "%q", string(v))
			}
		case xml.Directive, xml.ProcInst:
			return "", errors.New("unexpected XML directive in device configuration")
		}
	}
	return b.String(), nil
}

// CloseOnCancel is useful with a non-SSH transport; cancellation interrupts reads.
func CloseOnCancel(ctx context.Context, c io.Closer) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}
