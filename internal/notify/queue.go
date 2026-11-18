// Package notify provides a bounded durable-outbox model and verified SMTP.
// Queue changes are intended to commit in the same transaction as their cause.
package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const MaxEntries = 2000
const MaxAttempts = 5
const LeaseDuration = time.Minute

type Message struct {
	From         string       `json:"from"`
	To           string       `json:"to"`
	Subject      string       `json:"subject"`
	Text         string       `json:"text"`
	ID           string       `json:"id"`
	ToAdditional []string     `json:"toAdditional,omitempty"`
	Cc           []string     `json:"cc,omitempty"`
	Bcc          []string     `json:"bcc,omitempty"`
	Attachments  []Attachment `json:"attachments,omitempty"`
}
type Entry struct {
	ID            string    `json:"id"`
	Key           string    `json:"key"`
	To            string    `json:"to"`
	Subject       string    `json:"subject"`
	Sealed        string    `json:"sealed"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	CreatedAt     time.Time `json:"createdAt"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
	Lease         string    `json:"lease,omitempty"`
	LeaseUntil    time.Time `json:"leaseUntil,omitempty"`
	SentAt        time.Time `json:"sentAt,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}
type Queue struct {
	Entries []Entry `json:"entries,omitempty"`
}

func randomID() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func seal(key []byte, id string, msg Message) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, raw, []byte("outbox:"+id))), nil
}
func OpenMessage(key []byte, e Entry) (Message, error) {
	var msg Message
	raw, err := base64.RawStdEncoding.DecodeString(e.Sealed)
	if err != nil {
		return msg, errors.New("invalid encrypted outbox message")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return msg, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return msg, errors.New("invalid encrypted outbox message")
	}
	raw, err = gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte("outbox:"+e.ID))
	if err != nil {
		return msg, errors.New("outbox key mismatch or corrupted message")
	}
	if err = json.Unmarshal(raw, &msg); err != nil {
		return msg, err
	}
	if msg.ID != e.ID || msg.To != e.To || msg.Subject != e.Subject {
		return msg, errors.New("outbox snapshot metadata mismatch")
	}
	return msg, ValidateMessage(msg)
}
func (q *Queue) Enqueue(key string, msg Message, encryptionKey []byte, now, expires time.Time) (Entry, error) {
	if key == "" || len(key) > 200 || strings.ContainsAny(key, "\x00\r\n") {
		return Entry{}, errors.New("invalid outbox idempotency key")
	}
	for _, e := range q.Entries {
		if e.Key == key {
			return e, nil
		}
	}
	q.Prune(now)
	if len(q.Entries) >= MaxEntries {
		return Entry{}, errors.New("notification outbox capacity reached")
	}
	if err := ValidateMessage(msg); err != nil {
		return Entry{}, err
	}
	if !expires.IsZero() && !expires.After(now) {
		return Entry{}, errors.New("message is already expired")
	}
	msg.ID = randomID()
	sealed, err := seal(encryptionKey, msg.ID, msg)
	if err != nil {
		return Entry{}, err
	}
	cipherBytes := len(sealed)
	for _, entry := range q.Entries {
		cipherBytes += len(entry.Sealed)
	}
	if cipherBytes > MaxQueueCipherBytes {
		return Entry{}, errors.New("notification encrypted storage capacity reached")
	}
	e := Entry{ID: msg.ID, Key: key, To: msg.To, Subject: msg.Subject, Sealed: sealed, Status: "pending", CreatedAt: now.UTC(), NextAttemptAt: now.UTC(), ExpiresAt: expires}
	q.Entries = append(q.Entries, e)
	return e, nil
}
func (q Queue) Ready(now time.Time) bool {
	for _, e := range q.Entries {
		if e.Status == "pending" && !e.NextAttemptAt.After(now) || e.Status == "sending" && !e.LeaseUntil.After(now) {
			return true
		}
	}
	return false
}
func (q *Queue) Claim(now time.Time) (Entry, bool) {
	for i := range q.Entries {
		e := &q.Entries[i]
		if e.Status != "pending" && e.Status != "sending" {
			continue
		}
		if !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			e.Status = "expired"
			e.Lease = ""
			e.LeaseUntil = time.Time{}
			continue
		}
		if e.Status == "pending" && e.NextAttemptAt.After(now) || e.Status == "sending" && e.LeaseUntil.After(now) {
			continue
		}
		if e.Attempts >= MaxAttempts {
			e.Status = "failed"
			e.Lease = ""
			e.LastError = "投递重试次数已达上限"
			continue
		}
		e.Status = "sending"
		e.Attempts++
		e.Lease = randomID()
		e.LeaseUntil = now.Add(LeaseDuration)
		return *e, true
	}
	return Entry{}, false
}
func (q *Queue) Finish(id, lease string, success bool, now time.Time) error {
	for i := range q.Entries {
		e := &q.Entries[i]
		if e.ID != id {
			continue
		}
		if e.Status != "sending" || e.Lease != lease {
			return errors.New("outbox delivery lease changed")
		}
		e.Lease = ""
		e.LeaseUntil = time.Time{}
		if success {
			e.Status = "sent"
			e.SentAt = now.UTC()
			e.LastError = ""
			return nil
		}
		e.LastError = "邮件投递失败，请检查本地 SMTP 配置"
		if e.Attempts >= MaxAttempts {
			e.Status = "failed"
		} else {
			e.Status = "pending"
			e.NextAttemptAt = now.Add(time.Duration(1<<uint(e.Attempts-1)) * 30 * time.Second)
		}
		return nil
	}
	return errors.New("outbox entry not found")
}
func (q *Queue) Retry(id string, now time.Time) error {
	for i := range q.Entries {
		e := &q.Entries[i]
		if e.ID != id {
			continue
		}
		if e.Status != "failed" {
			return errors.New("only failed delivery can be retried")
		}
		if !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			return errors.New("expired message cannot be retried")
		}
		e.Status = "pending"
		e.Attempts = 0
		e.NextAttemptAt = now
		e.LastError = ""
		return nil
	}
	return errors.New("outbox entry not found")
}
func (q *Queue) Prune(now time.Time) {
	out := q.Entries[:0]
	for _, e := range q.Entries {
		if (e.Status == "pending" || e.Status == "sending") && !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			e.Status = "expired"
			e.Lease = ""
			e.LeaseUntil = time.Time{}
		}
		terminal := e.Status == "sent" || e.Status == "failed" || e.Status == "expired"
		if terminal && e.CreatedAt.Before(now.Add(-30*24*time.Hour)) {
			continue
		}
		out = append(out, e)
	}
	q.Entries = out
}
