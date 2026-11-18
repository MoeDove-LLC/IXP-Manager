package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSMTPRequiresVerifiedTransportAndFixedOrigin(t *testing.T) {
	valid := Config{Enabled: true, Host: "smtp.example.test", Port: 587, Mode: "starttls", From: "ixp@example.test", BaseURL: "https://ixp.example.test"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Mode = "plain" }, func(c *Config) { c.BaseURL = "http://evil.example.test" }, func(c *Config) { c.BaseURL = "https://ixp.example.test/reset?token=x" }, func(c *Config) { c.From = "sender@example.test\r\nBcc: other@example.test" }, func(c *Config) { c.Mode = "plain"; c.AllowLoopbackPlain = true; c.Host = "8.8.8.8" }} {
		c := valid
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("unsafe SMTP configuration accepted")
		}
	}
}
func TestFakeSMTPPlainLoopbackOnlyAndMIME(t *testing.T) {
	client, server := net.Pipe()
	captured := make(chan []byte, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer server.Close()
		reader := bufio.NewReader(server)
		io.WriteString(server, "220 local-fixture ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				io.WriteString(server, "250-local-fixture\r\n250 SIZE 100000\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				io.WriteString(server, "250 ok\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				io.WriteString(server, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				io.WriteString(server, "354 end with dot\r\n")
				raw, err := textproto.NewReader(reader).ReadDotBytes()
				if err != nil {
					return
				}
				captured <- raw
				io.WriteString(server, "250 accepted\r\n")
			case strings.HasPrefix(line, "QUIT"):
				io.WriteString(server, "221 bye\r\n")
				return
			default:
				io.WriteString(server, "500 unknown\r\n")
			}
		}
	}()
	smtp := SMTP{Config: Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "http://127.0.0.1:8080"}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	msg := testMessage()
	msg.ID = "stable-message-id"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := smtp.Send(ctx, msg); err != nil {
		t.Fatal(err)
	}
	<-serverDone
	raw := <-captured
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Fatal("Chinese subject not MIME encoded")
	}
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, parsed.Body))
	if err != nil || string(body) != msg.Text {
		t.Fatal("body changed in transit")
	}
	if !strings.Contains(parsed.Header.Get("Message-ID"), msg.ID) {
		t.Fatal("retry-stable Message-ID missing")
	}
}
func TestSTARTTLSDowngradeFailsWithoutCredentialsOrBody(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		io.WriteString(server, "220 fixture ESMTP\r\n")
		reader := bufio.NewReader(server)
		line, _ := reader.ReadString('\n')
		if !strings.HasPrefix(line, "EHLO") {
			return
		}
		io.WriteString(server, "250 fixture\r\n")
		_, _ = reader.ReadString('\n')
	}()
	sender := SMTP{Config: Config{Enabled: true, Host: "smtp.example.test", Port: 587, Mode: "starttls", From: "ixp@example.test", BaseURL: "https://ixp.example.test", Username: "user", Password: "private-password"}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	msg := testMessage()
	msg.ID = "id"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := sender.Send(ctx, msg)
	if err == nil || strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), "very-private") {
		t.Fatal("STARTTLS downgrade or secret error echo")
	}
	<-done
}

func TestImplicitTLSRejectsUntrustedCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.example.test"}, DNSNames: []string{"smtp.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		secure := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
		_ = secure.Handshake()
	}()
	sender := SMTP{Config: Config{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "tls", From: "ixp@example.test", BaseURL: "https://manager.example.test"}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	msg := testMessage()
	msg.ID = "tls-fixture"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = sender.Send(ctx, msg)
	if err == nil || err.Error() != "SMTP TLS verification failed" {
		t.Fatalf("untrusted SMTP certificate accepted or leaked response: %v", err)
	}
	<-done
}

func TestPrivateSMTPConfigAndSecretSafeErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smtp.json")
	raw := `{"enabled":true,"host":"smtp.example.test","port":587,"mode":"starttls","username":"sender","password":"not-for-logs","from":"ixp@example.test","baseURL":"https://manager.example.test"}`
	if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("world-readable SMTP credentials accepted")
	}
	os.Chmod(path, 0600)
	config, err := LoadConfig(path)
	if err != nil || !config.Enabled {
		t.Fatal("private valid config refused")
	}
	os.WriteFile(path, []byte(`{"password":"not-for-logs", "unexpected":true}`), 0600)
	if _, err = LoadConfig(path); err == nil || strings.Contains(err.Error(), "not-for-logs") {
		t.Fatal("invalid JSON leaked SMTP secret")
	}
	link := filepath.Join(dir, "linked.json")
	os.Symlink(path, link)
	if _, err = LoadConfig(link); err == nil {
		t.Fatal("symlinked SMTP credential file accepted")
	}
}
