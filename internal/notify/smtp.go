package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Enabled            bool   `json:"enabled"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Mode               string `json:"mode"`
	AllowLoopbackPlain bool   `json:"allowLoopbackPlain"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	From               string `json:"from"`
	BaseURL            string `json:"baseURL"`
}
type Sender interface {
	Send(context.Context, Message) error
}
type SMTP struct {
	Config Config
	dial   func(context.Context, string, string) (net.Conn, error)
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if path == "" {
		return c, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return c, errors.New("SMTP config cannot be read")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return c, errors.New("SMTP config must be a private regular file (0600), at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer file.Close()
	d := json.NewDecoder(file)
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid SMTP JSON configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("SMTP config must contain one JSON object")
	}
	return c, c.Validate()
}
func address(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && !strings.ContainsAny(value, "\r\n")
}
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Host == "" || strings.ContainsAny(c.Host, "\r\n\x00 /\\") || c.Port < 1 || c.Port > 65535 || !address(c.From) {
		return errors.New("invalid SMTP host, port or sender")
	}
	if c.Mode != "tls" && c.Mode != "starttls" && c.Mode != "plain" {
		return errors.New("SMTP mode must be tls or starttls")
	}
	if c.Mode == "plain" {
		ip := net.ParseIP(c.Host)
		if !c.AllowLoopbackPlain || ip == nil || !ip.IsLoopback() || c.Username != "" || c.Password != "" {
			return errors.New("plain SMTP is permitted only for explicit loopback tests without credentials")
		}
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("baseURL must be the fixed application origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !c.AllowLoopbackPlain || u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("baseURL must use HTTPS")
		}
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("SMTP username and password must be supplied together")
	}
	return nil
}
func ValidateMessage(m Message) error {
	if !address(m.From) || !address(m.To) || len(m.Subject) == 0 || len(m.Subject) > 300 || strings.ContainsAny(m.Subject, "\x00\r\n") || len(m.Text) > 64<<10 || strings.ContainsRune(m.Text, 0) {
		return errors.New("invalid notification snapshot")
	}
	return validateMessageParts(m)
}
func MessageBytes(m Message, now time.Time) ([]byte, error) {
	if err := ValidateMessage(m); err != nil {
		return nil, err
	}
	if m.ID == "" || strings.ContainsAny(m.ID, "\r\n<> \t") {
		return nil, errors.New("invalid mail message ID")
	}
	domain := m.From[strings.LastIndex(m.From, "@")+1:]
	if len(m.ToAdditional)+len(m.Cc)+len(m.Bcc)+len(m.Attachments) > 0 {
		return multipartMessageBytes(m, now)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: <%s>\r\nTo: <%s>\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", m.From, m.To, mime.QEncoding.Encode("utf-8", m.Subject), now.Format(time.RFC1123Z), m.ID, domain)
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(m.Text, "\r\n", "\n")))
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return []byte(b.String()), nil
}
func (s SMTP) Send(ctx context.Context, m Message) error {
	c := s.Config
	if !c.Enabled {
		return errors.New("mail delivery is disabled")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := MessageBytes(m, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	dial := s.dial
	if dial == nil {
		dial = dialer.DialContext
	}
	conn, err := dial(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	done := make(chan struct{})
	defer close(done)
	go func(connection net.Conn) {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}(conn)
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.Mode == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return errors.New("SMTP TLS verification failed")
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if c.Mode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP STARTTLS unavailable")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP TLS verification failed")
		}
	}
	if c.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err = client.Mail(m.From); err != nil {
		return errors.New("SMTP sender rejected")
	}
	for _, recipient := range messageRecipients(m) {
		if err = client.Rcpt(recipient); err != nil {
			return errors.New("SMTP recipient rejected")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message rejected")
	}
	if _, err = writer.Write(data); err != nil {
		writer.Close()
		return errors.New("SMTP transfer failed")
	}
	if err = writer.Close(); err != nil {
		return errors.New("SMTP delivery acknowledgement failed")
	}
	_ = client.Quit()
	return nil
}
