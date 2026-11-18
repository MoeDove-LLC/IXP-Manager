package notify

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxRecipients = 10
const MaxAttachments = 4
const MaxAttachmentBytes = 2 << 20
const MaxQueueCipherBytes = 32 << 20

// Attachment bytes are part of the immutable encrypted outbox snapshot. No
// filesystem path or URL is resolved by the SMTP worker.
type Attachment struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Data      []byte `json:"data"`
}

func messageRecipients(m Message) []string {
	out := []string{m.To}
	out = append(out, m.ToAdditional...)
	out = append(out, m.Cc...)
	return append(out, m.Bcc...)
}

func validateMessageParts(m Message) error {
	recipients := messageRecipients(m)
	if len(recipients) > MaxRecipients || len(m.Attachments) > MaxAttachments {
		return errors.New("notification recipient or attachment limit exceeded")
	}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		key := strings.ToLower(recipient)
		if len(recipient) > 254 || !address(recipient) || strings.ContainsRune(recipient, 0) || seen[key] {
			return errors.New("invalid or repeated notification recipient")
		}
		seen[key] = true
	}
	total := 0
	for _, attachment := range m.Attachments {
		mediaType, parameters, err := mime.ParseMediaType(attachment.MediaType)
		if err != nil || len(attachment.MediaType) > 127 || len(parameters) != 0 || !strings.HasPrefix(mediaType, "application/") && !strings.HasPrefix(mediaType, "image/") && !strings.HasPrefix(mediaType, "text/") {
			return errors.New("invalid notification attachment media type")
		}
		if attachment.Name == "" || attachment.Name == "." || attachment.Name == ".." || len(attachment.Name) > 240 || !utf8.ValidString(attachment.Name) || strings.ContainsAny(attachment.Name, "/\\") || len(attachment.Data) == 0 {
			return errors.New("invalid notification attachment")
		}
		for _, ch := range attachment.Name {
			if unicode.IsControl(ch) {
				return errors.New("invalid notification attachment name")
			}
		}
		total += len(attachment.Data)
		if total > MaxAttachmentBytes {
			return errors.New("notification attachments exceed 2 MiB")
		}
	}
	return nil
}

func encodedMessagePart(data []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	for len(encoded) > 76 {
		out.WriteString(encoded[:76])
		out.WriteString("\r\n")
		encoded = encoded[76:]
	}
	out.WriteString(encoded)
	out.WriteString("\r\n")
	return []byte(out.String())
}

func multipartMessageBytes(m Message, now time.Time) ([]byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	text, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}, "Content-Transfer-Encoding": {"base64"}})
	if err != nil {
		return nil, err
	}
	if _, err = text.Write(encodedMessagePart([]byte(strings.ReplaceAll(m.Text, "\r\n", "\n")))); err != nil {
		return nil, err
	}
	for _, attachment := range m.Attachments {
		headers := textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(attachment.MediaType, map[string]string{"name": attachment.Name})}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name})}, "Content-Transfer-Encoding": {"base64"}}
		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, err
		}
		if _, err = part.Write(encodedMessagePart(attachment.Data)); err != nil {
			return nil, err
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	formatRecipients := func(addresses []string) string {
		out := make([]string, len(addresses))
		for i, address := range addresses {
			out[i] = "<" + address + ">"
		}
		return strings.Join(out, ",\r\n ")
	}
	var out bytes.Buffer
	domain := m.From[strings.LastIndex(m.From, "@")+1:]
	fmt.Fprintf(&out, "From: <%s>\r\nTo: %s\r\n", m.From, formatRecipients(append([]string{m.To}, m.ToAdditional...)))
	if len(m.Cc) > 0 {
		fmt.Fprintf(&out, "Cc: %s\r\n", formatRecipients(m.Cc))
	}
	// Bcc is deliberately absent from every MIME header and body. It is used
	// only for SMTP RCPT commands through messageRecipients.
	fmt.Fprintf(&out, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", mime.QEncoding.Encode("utf-8", m.Subject), now.Format(time.RFC1123Z), m.ID, domain, writer.Boundary())
	out.Write(body.Bytes())
	return out.Bytes(), nil
}
