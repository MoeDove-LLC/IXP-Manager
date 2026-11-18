// Package api implements a bounded, certificate-verified RouterOS API-SSL
// transport. It has no arbitrary command or scripting entry point.
package api

import (
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type Resource string

const (
	SystemResource Resource = "system-resource"
	SystemLicense  Resource = "system-license"
	RouterBoard    Resource = "routerboard"
	Identity       Resource = "identity"
	RoutingTable   Resource = "routing-table"
	BGPInstance    Resource = "bgp-instance"
	BGPConnection  Resource = "bgp-connection"
	FilterRule     Resource = "filter-rule"
	SafeMode       Resource = "safe-mode"
)

type Action string

const (
	Add     Action = "add"
	Set     Action = "set"
	Enable  Action = "enable"
	Disable Action = "disable"
	Remove  Action = "remove"
	Unset   Action = "unset"
)

type Limits struct {
	MaxWordBytes     int
	MaxSentenceBytes int
	MaxWords         int
	MaxReplyBytes    int
	MaxRecords       int
	MaxSentences     int
}
type Config struct {
	Address        string
	ServerName     string
	Username       string
	Password       []byte         `json:"-"`
	RootCAs        *x509.CertPool `json:"-"`
	SPKISHA256     string
	DialTimeout    time.Duration
	CommandTimeout time.Duration
	Limits         Limits
}

func (Config) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS API-SSL configuration (credentials redacted)")
}
func (Config) MarshalJSON() ([]byte, error) {
	return []byte(`{"transport":"api-ssl","credentials":"redacted"}`), nil
}

type Attribute struct {
	Name      string `json:"name"`
	Value     string `json:"-"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

func (a Attribute) Format(s fmt.State, _ rune) {
	value := a.Value
	if a.Sensitive || sensitiveName(a.Name) {
		value = "[redacted]"
	}
	_, _ = io.WriteString(s, a.Name+"="+value)
}
func (a Attribute) MarshalJSON() ([]byte, error) {
	value := a.Value
	sensitive := a.Sensitive || sensitiveName(a.Name)
	if sensitive {
		value = "[redacted]"
	}
	return json.Marshal(struct {
		Name      string `json:"name"`
		Value     string `json:"value"`
		Sensitive bool   `json:"sensitive"`
	}{a.Name, value, sensitive})
}

type Mutation struct {
	Resource   Resource    `json:"resource"`
	Action     Action      `json:"action"`
	ID         string      `json:"id,omitempty"`
	Property   string      `json:"property,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`
}
type Result struct {
	Acknowledged bool   `json:"acknowledged"`
	ID           string `json:"id,omitempty"`
}
type Record map[string]string

type Capability string

const (
	Supported   Capability = "supported"
	Unsupported Capability = "unsupported"
	Unknown     Capability = "unknown"
)

type SafeModeState struct {
	Enabled bool   `json:"enabled"`
	Current bool   `json:"current"`
	User    string `json:"user,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

// Inventory contains only the fixed read dictionaries, never login passwords
// or tcp-md5-key. Unsupported/unknown optional menus never become false state.
type Inventory struct {
	Resources    []Record                `json:"resources"`
	Licenses     []Record                `json:"licenses"`
	RouterBoards []Record                `json:"routerboards"`
	Identities   []Record                `json:"identities"`
	Tables       []Record                `json:"tables"`
	Instances    []Record                `json:"instances"`
	Connections  []Record                `json:"connections"`
	FilterRules  []Record                `json:"filterRules"`
	SafeMode     *SafeModeState          `json:"safeMode"`
	Capabilities map[Resource]Capability `json:"capabilities"`
}
type PropertyValue struct {
	Known bool   `json:"known"`
	Value string `json:"value"`
}

const MaxPropertyBatch = 64

type PropertyRequest struct {
	Resource Resource
	ID       string
	Property string
}

// SecretValue requires an explicit copy to obtain plaintext. Ordinary formatting
// and JSON encoding intentionally cannot serialize it.
type SecretValue struct{ value []byte }

func (s SecretValue) CopyBytes() []byte { return append([]byte(nil), s.value...) }
func (s SecretValue) Equal(other []byte) bool {
	return s.value != nil && subtle.ConstantTimeCompare(s.value, other) == 1
}
func (SecretValue) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[redacted]") }
func (SecretValue) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

type Credential struct {
	Known bool        `json:"known"`
	Value SecretValue `json:"-"`
}

func (c Credential) Matches(expected []byte) bool { return c.Known && c.Value.Equal(expected) }

func (Credential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS credential (redacted)")
}

type ErrorCode string

const (
	CodeConfig         ErrorCode = "invalid-config"
	CodeAuthentication ErrorCode = "authentication-failed"
	CodeTLS            ErrorCode = "tls-verification-or-handshake-failed"
	CodeTransport      ErrorCode = "transport-failed"
	CodeProtocol       ErrorCode = "invalid-protocol"
	CodeLimit          ErrorCode = "limit-exceeded"
	CodeTrap           ErrorCode = "command-failed"
	CodeUnsupported    ErrorCode = "unsupported"
	CodeFatal          ErrorCode = "connection-terminated"
	CodeClosed         ErrorCode = "client-closed"
	CodeCancelled      ErrorCode = "cancelled-or-deadline"
)

// Error never contains a remote message, command attribute value, password,
// challenge, TLS diagnostic, username or wrapped network error.
type Error struct {
	Code      ErrorCode
	Uncertain bool
	Resource  Resource
	Action    Action
	Category  int
	// Read diagnostics are populated only for non-sensitive fixed print
	// requests. They contain counts/categories, never response values.
	ReplyBytes     int
	ReplyRecords   int
	ReplySentences int
	LimitKind      string
	Deadline       bool
	DeadlineKind   string
	Property       string
}

func (e *Error) Error() string {
	if e == nil {
		return "RouterOS API error"
	}
	if e.Uncertain {
		return "RouterOS API " + string(e.Code) + ": operation outcome is uncertain; do not retry automatically"
	}
	return "RouterOS API " + string(e.Code)
}
func sensitiveName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "password" || name == "response" || name == "tcp-md5-key"
}
