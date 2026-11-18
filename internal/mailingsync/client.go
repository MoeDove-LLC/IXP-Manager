package mailingsync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	config          Config
	source, target  *http.Client
	token, password string
}

var errTargetAuthority = errors.New("target administrator credentials or permissions rejected; verify the configured private password and REST authorization")

func httpClient(ca string) (*http.Client, error) {
	roots, e := x509.SystemCertPool()
	if e != nil {
		roots = x509.NewCertPool()
	}
	if ca != "" {
		raw, e := ReadPrivate(ca, 1<<20)
		if e != nil || !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("private CA bundle unavailable or invalid")
		}
	}
	t := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10}
	return &http.Client{Transport: t, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func NewClient(c Config) (*Client, error) {
	if e := c.validate(); e != nil {
		return nil, e
	}
	tb, e := ReadPrivate(c.Source.TokenFile, 4096)
	if e != nil {
		return nil, e
	}
	pb, e := ReadPrivate(c.Target.PasswordFile, 4096)
	if e != nil {
		return nil, e
	}
	token, password := strings.TrimSpace(string(tb)), strings.TrimSpace(string(pb))
	if token == "" || password == "" || strings.ContainsAny(token+password, "\r\n\x00") {
		return nil, errors.New("credentials must be nonempty single-line private values")
	}
	s, e := httpClient(c.Source.CAFile)
	if e != nil {
		return nil, e
	}
	t, e := httpClient(c.Target.CAFile)
	if e != nil {
		return nil, e
	}
	return &Client{c, s, t, token, password}, nil
}
func (c *Client) request(ctx context.Context, target bool, method, path string, body any) ([]byte, int, error) {
	base, h := c.config.Source.URL, c.source
	if target {
		base, h = c.config.Target.URL, c.target
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r, e := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(raw))
	if e != nil {
		return nil, 0, errors.New("request construction failed")
	}
	r.GetBody = nil // Never let the transport replay a membership mutation.
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if target {
		r.SetBasicAuth(c.config.Target.Username, c.password)
	} else {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, e := h.Do(r)
	if e != nil {
		return nil, 0, errors.New("configured service request failed")
	}
	defer response.Body.Close()
	b, e := io.ReadAll(io.LimitReader(response.Body, maxDocument+1))
	if e != nil || len(b) > maxDocument {
		return nil, response.StatusCode, errors.New("service response exceeded bound or was interrupted")
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, response.StatusCode, errors.New("redirect refused")
	}
	return b, response.StatusCode, nil
}
func remoteJSON(raw []byte, v any) error {
	var m map[string]any
	if e := strict(raw, &m); e != nil {
		return e
	}
	if e := json.Unmarshal(raw, v); e != nil {
		return errors.New("invalid target response")
	}
	return nil
}
func (c *Client) sourceExport(ctx context.Context) (Export, error) {
	var out Export
	b, status, e := c.request(ctx, false, "GET", "/api/mailing-lists/"+c.config.Source.ListID+"/export", nil)
	if e != nil || status != 200 {
		return out, errors.New("source export unavailable; verify current admin token, list and permissions")
	}
	if e = strict(b, &out); e != nil {
		return out, e
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	for _, name := range []string{"version", "list", "checksum", "subscribed", "unsubscribed", "generatedAt", "externalApplied"} {
		if len(fields[name]) == 0 || bytes.Equal(fields[name], []byte("null")) {
			return out, errors.New("source export omitted a required field")
		}
	}
	return out, out.validate(c.config)
}
func (c *Client) identity(ctx context.Context) error {
	var versions struct {
		Mailman string `json:"mailman_version"`
		API     string `json:"api_version"`
	}
	b, status, e := c.request(ctx, true, "GET", "/system/versions", nil)
	if e == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return errTargetAuthority
	}
	if e != nil || status != 200 || remoteJSON(b, &versions) != nil || versions.Mailman != "GNU Mailman 3.3.10 (Tom Sawyer)" || versions.API != "3.1" {
		return errors.New("target must expose the reviewed Mailman 3.3.10 /3.1 contract")
	}
	var list struct {
		ID    string `json:"list_id"`
		Email string `json:"fqdn_listname"`
	}
	b, status, e = c.request(ctx, true, "GET", "/lists/"+c.config.Target.ListID, nil)
	if e != nil || status != 200 || remoteJSON(b, &list) != nil || list.ID != c.config.Target.ListID || list.Email != c.config.Target.ListEmail {
		return errors.New("target mailing-list identity changed or is unavailable")
	}
	created, e := c.targetCreatedAt(ctx)
	if e != nil || c.config.Target.ListCreatedAt != "" && created != c.config.Target.ListCreatedAt {
		return errors.New("target mailing-list creation identity changed or is unavailable")
	}
	return nil
}
func (c *Client) targetCreatedAt(ctx context.Context) (string, error) {
	var created struct {
		Value string `json:"created_at"`
	}
	b, status, e := c.request(ctx, true, "GET", "/lists/"+c.config.Target.ListID+"/config/created_at", nil)
	if e != nil || status != 200 || remoteJSON(b, &created) != nil || !mailmanTime(created.Value) {
		return "", errors.New("target mailing-list creation identity unavailable")
	}
	return created.Value, nil
}
func (c *Client) member(ctx context.Context, address string) (*Member, error) {
	// An exact list+role+email resource avoids wildcard find semantics and never
	// follows a server-provided self_link. Slash-local-parts are refused below.
	if !email(address) || strings.Contains(address, "/") {
		return nil, errors.New("address cannot be safely represented by the target membership resource")
	}
	b, status, e := c.request(ctx, true, "GET", "/lists/"+c.config.Target.ListID+"/member/"+url.PathEscape(address), nil)
	if e != nil {
		return nil, e
	}
	if status == 404 {
		return nil, nil
	}
	if status != 200 {
		return nil, errors.New("target membership read failed")
	}
	var m Member
	if remoteJSON(b, &m) != nil || !m.valid(c.config, address) {
		return nil, errors.New("target membership identity or address binding is ambiguous")
	}
	return &m, nil
}
func (c *Client) Preview(ctx context.Context) (Plan, error) {
	var p Plan
	e, err := c.sourceExport(ctx)
	if err != nil {
		return p, err
	}
	if err = c.identity(ctx); err != nil {
		return p, err
	}
	p = Plan{Version: 1, ConfigChecksum: sum(c.config), Source: e, Target: c.config.Target, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Actions: []Action{}}
	created, err := c.targetCreatedAt(ctx)
	if err != nil {
		return Plan{}, err
	}
	p.Target.ListCreatedAt = created
	for group, values := range [][]string{e.Subscribed, e.Unsubscribed} {
		for _, address := range values {
			before, err := c.member(ctx, address)
			if err != nil {
				return Plan{}, err
			}
			p.Actions = append(p.Actions, Action{address, group == 0, before})
		}
	}
	p.Checksum = planSum(p)
	bound := *c
	bound.config.Target.ListCreatedAt = created
	if err = bound.fresh(ctx, p); err != nil {
		return Plan{}, err
	}
	return p, p.validate(c.config)
}

func (c *Client) readback(ctx context.Context, address string) (*Member, error) {
	if e := c.identity(ctx); e != nil {
		return nil, e
	}
	m, e := c.member(ctx, address)
	if e != nil {
		return nil, e
	}
	if e = c.identity(ctx); e != nil {
		return nil, e
	}
	return m, nil
}

func (c *Client) subscriptionNotificationsDisabled(ctx context.Context) error {
	var settings struct {
		AdminNotify *bool `json:"admin_notify_mchanges"`
	}
	b, status, e := c.request(ctx, true, "GET", "/lists/"+c.config.Target.ListID+"/config/admin_notify_mchanges", nil)
	if e != nil || status != 200 || remoteJSON(b, &settings) != nil || settings.AdminNotify == nil {
		return errors.New("cannot verify target admin_notify_mchanges=false; new subscriptions refused")
	}
	if *settings.AdminNotify {
		return errors.New("new subscriptions require target admin_notify_mchanges=false; disable administrator membership notices during exclusive maintenance before applying")
	}
	return nil
}

func (c *Client) mutate(ctx context.Context, a Action) error {
	var path, method string
	var body any
	wanted := 201
	if a.Subscribe {
		path, method = "/members", "POST"
		// Stock 3.3.10 has no per-request administrator-notice override.
		// Apply verifies the list setting before journaling this write intent.
		body = map[string]any{"list_id": c.config.Target.ListID, "subscriber": a.Email, "role": "member", "pre_verified": true, "pre_confirmed": true, "pre_approved": true, "send_welcome_message": false, "invitation": false}
	} else {
		// Mailman 3.3.10 MembersOfList.on_delete -> unsubscribe_members directly
		// unsubscribes the member role without the goodbye/admin notification
		// workflow. AMember.on_delete does NOT provide that guarantee.
		path, method = "/lists/"+c.config.Target.ListID+"/roster/member", "DELETE"
		body = map[string]any{"emails": []string{a.Email}}
		wanted = 200
	}
	_, status, e := c.request(ctx, true, method, path, body)
	if e != nil || status != wanted {
		return errors.New("membership mutation response uncertain; readback required")
	}
	return nil
}
