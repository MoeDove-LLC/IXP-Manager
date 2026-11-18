package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"reflect"
	"strings"
	"testing"
	"time"
)

func attachmentMessage() Message {
	m := testMessage()
	m.ToAdditional = []string{"engineer@example.test"}
	m.Cc = []string{"noc@example.test"}
	m.Bcc = []string{"private-operations@example.test"}
	m.Attachments = []Attachment{{Name: "授权书.pdf", MediaType: "application/pdf", Data: []byte("%PDF-1.4\nprivate-fixture-pdf")}, {Name: "circuit.txt", MediaType: "text/plain", Data: []byte("circuit attachment 中文")}}
	return m
}
func TestMultipartOutboxEncryptsAllRecipientsAndAttachmentBytes(t *testing.T) {
	key := bytes.Repeat([]byte{5}, 32)
	m := attachmentMessage()
	q := Queue{}
	e, err := q.Enqueue("patch:fixture", m, key, time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(q)
	for _, secret := range []string{m.Bcc[0], m.Cc[0], m.ToAdditional[0], string(m.Attachments[0].Data), m.Text} {
		if bytes.Contains(state, []byte(secret)) {
			t.Fatal("private multipart data escaped ciphertext")
		}
	}
	opened, err := OpenMessage(key, e)
	m.ID = e.ID
	if err != nil || !reflect.DeepEqual(opened, m) {
		t.Fatal("encrypted multipart snapshot changed", err)
	}
	raw, err := MessageBytes(opened, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(m.Bcc[0])) || bytes.Contains(bytes.ToLower(raw), []byte("bcc:")) {
		t.Fatal("BCC leaked into MIME")
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Header.Get("To"), m.ToAdditional[0]) || !strings.Contains(parsed.Header.Get("Cc"), m.Cc[0]) {
		t.Fatal("visible recipient headers missing")
	}
	media, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || media != "multipart/mixed" {
		t.Fatal("multipart envelope missing")
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	for i := 0; i < 3; i++ {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && string(data) != m.Text || i > 0 && !bytes.Equal(data, m.Attachments[i-1].Data) {
			t.Fatal("multipart plaintext/attachment bytes changed")
		}
		if i > 0 && part.FileName() != m.Attachments[i-1].Name {
			t.Fatal("attachment filename was not MIME encoded")
		}
	}
	if _, err = reader.NextPart(); err != io.EOF {
		t.Fatal("unexpected extra MIME part")
	}
}
func TestSMTPMultipartUsesBCCOnlyInEnvelope(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	type capture struct {
		rcpt []string
		body []byte
	}
	done := make(chan capture, 1)
	go func() {
		defer server.Close()
		result := capture{}
		defer func() { done <- result }()
		reader := bufio.NewReader(server)
		io.WriteString(server, "220 fixture ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				io.WriteString(server, "250 fixture\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"):
				io.WriteString(server, "250 ok\r\n")
			case strings.HasPrefix(line, "RCPT TO:"):
				result.rcpt = append(result.rcpt, strings.TrimSpace(line))
				io.WriteString(server, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				io.WriteString(server, "354 data\r\n")
				result.body, _ = textproto.NewReader(reader).ReadDotBytes()
				io.WriteString(server, "250 accepted\r\n")
			case strings.HasPrefix(line, "QUIT"):
				io.WriteString(server, "221 bye\r\n")
				return
			default:
				return
			}
		}
	}()
	sender := SMTP{Config: Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "http://127.0.0.1:8080"}, dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	m := attachmentMessage()
	m.ID = "patch-fixture-id"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sender.Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if len(result.rcpt) != 4 || !strings.Contains(strings.Join(result.rcpt, "\n"), m.Bcc[0]) || bytes.Contains(result.body, []byte(m.Bcc[0])) {
		t.Fatal("BCC envelope/body boundary broken")
	}
}
func TestMultipartBoundsAndHeaderInjectionFailClosed(t *testing.T) {
	for _, mutate := range []func(*Message){func(m *Message) { m.Cc = []string{m.To} }, func(m *Message) { m.Bcc = []string{"a@example.test\r\nBcc: evil@example.test"} }, func(m *Message) { m.ToAdditional = make([]string, MaxRecipients) }, func(m *Message) { m.Attachments[0].Name = "../private.pdf" }, func(m *Message) { m.Attachments[0].Name = "x\r\nBcc: y.pdf" }, func(m *Message) { m.Attachments[0].MediaType = "application/pdf\r\nInjected: yes" }, func(m *Message) { m.Attachments[0].Data = make([]byte, MaxAttachmentBytes+1) }} {
		m := attachmentMessage()
		mutate(&m)
		if ValidateMessage(m) == nil {
			t.Fatal("unsafe multipart message accepted")
		}
	}
	q := Queue{Entries: []Entry{{Key: "occupied", Sealed: strings.Repeat("x", MaxQueueCipherBytes)}}}
	if _, err := q.Enqueue("new", testMessage(), bytes.Repeat([]byte{1}, 32), time.Now(), time.Time{}); err == nil || len(q.Entries) != 1 {
		t.Fatal("ciphertext storage bound failed")
	}
}
