// Package mailingsync applies only explicit IXP Manager mailing-list intentions
// to one privately configured Mailman 3 list. It never sends messages itself.
package mailingsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxDocument = 16 << 20
const maxIntents = 10000
const maxChanges = 1000

type List struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Email     string `json:"email"`
	Revision  uint64 `json:"revision"`
	CreatedAt string `json:"createdAt"`
}
type Export struct {
	Version         int      `json:"version"`
	List            List     `json:"list"`
	Checksum        string   `json:"checksum"`
	Subscribed      []string `json:"subscribed"`
	Unsubscribed    []string `json:"unsubscribed"`
	GeneratedAt     string   `json:"generatedAt"`
	ExternalApplied bool     `json:"externalApplied"`
}
type Source struct {
	URL           string `json:"url"`
	TokenFile     string `json:"tokenFile"`
	CAFile        string `json:"caFile,omitempty"`
	ListID        string `json:"listId"`
	ListKey       string `json:"listKey"`
	ListEmail     string `json:"listEmail"`
	ListCreatedAt string `json:"listCreatedAt"`
}
type Target struct {
	URL           string `json:"url"`
	Username      string `json:"username"`
	PasswordFile  string `json:"passwordFile"`
	CAFile        string `json:"caFile,omitempty"`
	ListID        string `json:"listId"`
	ListEmail     string `json:"listEmail"`
	ListCreatedAt string `json:"listCreatedAt,omitempty"`
}
type Config struct {
	Version int    `json:"version"`
	Source  Source `json:"source"`
	Target  Target `json:"target"`
}
type Member struct {
	ID     string `json:"member_id"`
	ListID string `json:"list_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Mode   string `json:"subscription_mode"`
}
type Action struct {
	Email     string  `json:"email"`
	Subscribe bool    `json:"subscribe"`
	Before    *Member `json:"before"`
}
type Plan struct {
	Version        int      `json:"version"`
	ConfigChecksum string   `json:"configChecksum"`
	Source         Export   `json:"source"`
	Target         Target   `json:"target"`
	Actions        []Action `json:"actions"`
	CreatedAt      string   `json:"createdAt"`
	Checksum       string   `json:"checksum"`
}
type Result struct {
	State      string  `json:"state"`
	Observed   *Member `json:"observed,omitempty"`
	ObservedAt string  `json:"observedAt,omitempty"`
}
type Journal struct {
	Version      int      `json:"version"`
	PlanChecksum string   `json:"planChecksum"`
	Results      []Result `json:"results"`
}
type Summary struct {
	PlanChecksum     string `json:"planChecksum"`
	Add              int    `json:"add"`
	Remove           int    `json:"remove"`
	Unchanged        int    `json:"unchanged"`
	Completed        int    `json:"completed"`
	Unknown          int    `json:"unknown"`
	NotObserved      int    `json:"notObserved"`
	SourceChecksum   string `json:"sourceChecksum"`
	RecordedComplete bool   `json:"recordedComplete"`
	NoIntentions     bool   `json:"noIntentions"`
	LastObservedAt   string `json:"lastObservedAt,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var listID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,253}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{32}$`)

func sum(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func hash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}
func email(v string) bool {
	if len(v) > 254 || v == "" || strings.ToLower(v) != v || strings.ContainsAny(v, "*\\\r\n\x00") {
		return false
	}
	for _, c := range v {
		if c < 33 || c > 126 {
			return false
		}
	}
	a, e := mail.ParseAddress(v)
	return e == nil && a.Address == v && a.Name == ""
}
func mailmanTime(v string) bool {
	if timestamp(v) {
		return true
	}
	_, e := time.Parse("2006-01-02T15:04:05.999999999", v)
	return e == nil
}
func timestamp(v string) bool { _, e := time.Parse(time.RFC3339Nano, v); return e == nil }
func httpsURL(raw, path string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && u.Path == path && u.Opaque == ""
}
func (c Config) validate() error {
	if c.Version != 1 || !httpsURL(c.Source.URL, "") || !httpsURL(c.Target.URL, "/3.1") || !identifier.MatchString(c.Source.ListID) || !identifier.MatchString(c.Source.ListKey) || !email(c.Source.ListEmail) || !timestamp(c.Source.ListCreatedAt) || !listID.MatchString(c.Target.ListID) || !email(c.Target.ListEmail) || c.Target.ListCreatedAt != "" && !mailmanTime(c.Target.ListCreatedAt) || c.Target.ListEmail != c.Source.ListEmail || c.Target.Username == "" || len(c.Target.Username) > 128 || strings.ContainsAny(c.Target.Username, ":\r\n\x00") || c.Source.TokenFile == "" || c.Target.PasswordFile == "" {
		return errors.New("invalid fixed source/target configuration")
	}
	return nil
}
func (e Export) checksum() string {
	return sum(struct {
		Version      int      `json:"version"`
		List         List     `json:"list"`
		Subscribed   []string `json:"subscribed"`
		Unsubscribed []string `json:"unsubscribed"`
	}{e.Version, e.List, e.Subscribed, e.Unsubscribed})
}
func (e Export) validate(c Config) error {
	if e.Version != 1 || e.ExternalApplied || !hash(e.Checksum) || e.Checksum != e.checksum() || e.List.ID != c.Source.ListID || e.List.Key != c.Source.ListKey || e.List.Email != c.Source.ListEmail || e.List.CreatedAt != c.Source.ListCreatedAt || e.List.Revision == 0 || e.List.Revision > 9007199254740991 || !timestamp(e.GeneratedAt) || e.Subscribed == nil || e.Unsubscribed == nil || len(e.Subscribed)+len(e.Unsubscribed) > maxIntents {
		return errors.New("source export identity, checksum or bounds invalid")
	}
	seen := map[string]bool{}
	for _, group := range [][]string{e.Subscribed, e.Unsubscribed} {
		if !sort.StringsAreSorted(group) {
			return errors.New("source intents must be canonical")
		}
		for _, v := range group {
			if !email(v) || seen[v] {
				return errors.New("source intents are ambiguous")
			}
			seen[v] = true
		}
	}
	return nil
}
func (m Member) valid(c Config, address string) bool {
	return uuid.MatchString(m.ID) && m.ListID == c.Target.ListID && m.Email == address && m.Role == "member" && m.Mode == "as_address"
}
func planSum(p Plan) string { p.Checksum = ""; return sum(p) }
func (p Plan) validate(c Config) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := p.Source.validate(c); err != nil {
		return err
	}
	expected, actual := c.Target, p.Target
	if !mailmanTime(actual.ListCreatedAt) || expected.ListCreatedAt != "" && expected.ListCreatedAt != actual.ListCreatedAt {
		return errors.New("plan target creation identity invalid")
	}
	expected.ListCreatedAt = ""
	actual.ListCreatedAt = ""
	if p.Version != 1 || p.ConfigChecksum != sum(c) || actual != expected || p.Checksum != planSum(p) || !hash(p.Checksum) || !timestamp(p.CreatedAt) || len(p.Actions) != len(p.Source.Subscribed)+len(p.Source.Unsubscribed) {
		return errors.New("plan identity or checksum changed")
	}
	i := 0
	changes := 0
	for group, values := range [][]string{p.Source.Subscribed, p.Source.Unsubscribed} {
		for _, v := range values {
			a := p.Actions[i]
			i++
			if a.Email != v || a.Subscribe != (group == 0) || a.Before != nil && !a.Before.valid(c, v) {
				return errors.New("plan actions do not match explicit intentions")
			}
			if a.Subscribe != (a.Before != nil) {
				changes++
			}
		}
	}
	if changes > maxChanges {
		return errors.New("plan exceeds 1000 membership changes")
	}
	return nil
}
func (p Plan) Summary(j *Journal) Summary {
	s := Summary{PlanChecksum: p.Checksum, SourceChecksum: p.Source.Checksum, NoIntentions: len(p.Actions) == 0}
	for i, a := range p.Actions {
		switch {
		case a.Subscribe && a.Before == nil:
			s.Add++
		case !a.Subscribe && a.Before != nil:
			s.Remove++
		default:
			s.Unchanged++
		}
		if j != nil && i < len(j.Results) {
			switch j.Results[i].State {
			case "done":
				s.Completed++
				if j.Results[i].ObservedAt > s.LastObservedAt {
					s.LastObservedAt = j.Results[i].ObservedAt
				}
			case "intent", "unknown":
				s.Unknown++
			case "not-observed":
				s.NotObserved++
			}
		}
	}
	s.RecordedComplete = j != nil && len(p.Actions) > 0 && s.Completed == len(p.Actions)
	return s
}
func DecodeConfig(raw []byte) (Config, error) {
	var c Config
	if err := strict(raw, &c); err != nil {
		return c, err
	}
	return c, c.validate()
}
func DecodePlan(raw []byte, c Config) (Plan, error) {
	var p Plan
	if err := strict(raw, &p); err != nil {
		return p, err
	}
	return p, p.validate(c)
}
func Encode(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

// Reject duplicate object keys and trailing values before decoding a private
// contract. Unknown source/config/plan fields are not silently accepted.
func strict(raw []byte, v any) error {
	if len(raw) > maxDocument {
		return errors.New("JSON exceeds size bound")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting exceeds bound")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return e
					}
					s, ok := k.(string)
					if !ok || seen[s] {
						return errors.New("duplicate JSON field")
					}
					seen[s] = true
					if e = walk(depth + 1); e != nil {
						return e
					}
				}
			case '[':
				for d.More() {
					if e := walk(depth + 1); e != nil {
						return e
					}
				}
			default:
				return errors.New("invalid JSON delimiter")
			}
			_, e = d.Token()
			return e
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("invalid JSON contract")
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("invalid JSON fields")
	}
	return nil
}
