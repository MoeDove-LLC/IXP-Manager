package atlas

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Client struct {
	config Config
	base   *url.URL
	http   *http.Client
	mu     sync.Mutex
	next   time.Time
}
type Error struct {
	Code      string
	Uncertain bool
}

func (e *Error) Error() string { return "Atlas request failed: " + e.Code }
func LoadConfig(path string) (Config, error) {
	var c Config
	if path == "" {
		return c, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return c, errors.New("Atlas private config unavailable")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 65536 {
		return c, errors.New("Atlas config must be a private regular file under64 KiB")
	}
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid Atlas configuration")
	}
	return normalizeConfig(c)
}
func normalizeConfig(c Config) (Config, error) {
	if c.BaseURL == "" {
		c.BaseURL = OfficialOrigin
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return c, errors.New("Atlas baseURL must be a fixed origin")
	}
	if c.BaseURL != OfficialOrigin && c.BaseURL != OfficialOrigin+"/" {
		ip := net.ParseIP(u.Hostname())
		if !c.AllowLoopbackHTTP || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return c, errors.New("Atlas origin is fixed to official HTTPS; only explicit literal loopback HTTP fixtures are allowed")
		}
	}
	if c.Enabled && !regexp.MustCompile(`^[A-Za-z0-9._-]{16,256}$`).MatchString(c.APIKey) {
		return c, errors.New("enabled Atlas provider requires a private bounded API key")
	}
	c.BaseURL = strings.TrimSuffix(c.BaseURL, "/")
	return c, nil
}
func NewClient(c Config) (*Client, error) {
	c, err := normalizeConfig(c)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, nil
	}
	u, _ := url.Parse(c.BaseURL)
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 8 * time.Second, MaxIdleConnsPerHost: 2}
	return &Client{config: c, base: u, http: &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Origin() string {
	if c == nil {
		return ""
	}
	return c.config.BaseURL
}
func (c *Client) request(ctx context.Context, method, path string, q url.Values, payload any, want int) ([]byte, error) {
	if c == nil {
		return nil, &Error{Code: "disabled"}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c.mu.Lock()
	wait := time.Until(c.next)
	if wait > 0 {
		select {
		case <-ctx.Done():
			c.mu.Unlock()
			return nil, &Error{Code: "request-deadline"}
		case <-time.After(wait):
		}
	}
	c.next = time.Now().Add(time.Second)
	c.mu.Unlock()
	u := *c.base
	u.Path = "/api/v2/" + path
	u.RawQuery = q.Encode()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, &Error{Code: "invalid-payload"}
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, &Error{Code: "invalid-request"}
	}
	req.Header.Set("Authorization", "Key "+c.config.APIKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Code: "transport-or-timeout", Uncertain: method == "POST" || method == "DELETE"}
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		definiteRejection := false
		switch res.StatusCode {
		case 400, 401, 403, 404, 405, 409, 413, 422, 429:
			definiteRejection = true
		}
		uncertain := (method == "POST" || method == "DELETE") && !definiteRejection
		return nil, &Error{Code: fmt.Sprintf("http-%d", res.StatusCode), Uncertain: uncertain}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes {
		return nil, &Error{Code: "response-size-or-read", Uncertain: method == "POST" || method == "DELETE"}
	}
	return raw, nil
}
func (c *Client) Discover(ctx context.Context, asn uint32, af int, anchor bool) ([]Probe, []byte, error) {
	if asn == 0 || (af != 4 && af != 6) {
		return nil, nil, &Error{Code: "invalid-probe-scope"}
	}
	q := url.Values{fmt.Sprintf("asn_v%d", af): {strconv.FormatUint(uint64(asn), 10)}, "status": {"1"}, "is_public": {"true"}, "page_size": {"200"}, "format[datetime]": {"unix"}}
	if anchor {
		q.Set("is_anchor", "true")
	}
	raw, err := c.request(ctx, "GET", "probes/", q, nil, 200)
	if err != nil {
		return nil, nil, err
	}
	probes, err := parseProbes(raw, asn, af, anchor)
	return probes, raw, err
}
func (c *Client) Create(ctx context.Context, s Spec) (int64, []byte, error) {
	s, _, err := NormalizeSpec(s, time.Now())
	if err != nil || !markerPattern.MatchString(s.Description) || s.StartAt.IsZero() {
		return 0, nil, &Error{Code: "invalid-managed-spec"}
	}
	definition := map[string]any{"type": s.Type, "af": s.AF, "target": s.Target, "description": s.Description, "is_oneoff": s.IsOneoff, "is_public": s.IsPublic, "packets": 3, "size": 64, "resolve_on_probe": false, "auto_topup": false, "target_update_hours": 0}
	if s.Type == "traceroute" {
		definition["protocol"] = "ICMP"
		definition["max_hops"] = 32
		definition["first_hop"] = 1
	}
	if !s.IsOneoff {
		definition["interval"] = s.IntervalSeconds
	}
	ids := make([]string, len(s.ProbeIDs))
	for i, id := range s.ProbeIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	body := map[string]any{"definitions": []any{definition}, "probes": []any{map[string]any{"type": "probes", "requested": len(ids), "value": strings.Join(ids, ",")}}, "start_time": s.StartAt.Unix(), "is_oneoff": s.IsOneoff}
	if !s.IsOneoff {
		body["stop_time"] = s.StartAt.Add(time.Duration(s.DurationSeconds) * time.Second).Unix()
	}
	raw, err := c.request(ctx, "POST", "measurements/", nil, body, 201)
	if err != nil {
		return 0, nil, err
	}
	var reply struct {
		Measurements []int64 `json:"measurements"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Measurements) != 1 || reply.Measurements[0] < 1 {
		return 0, nil, &Error{Code: "ambiguous-creation-response", Uncertain: true}
	}
	return reply.Measurements[0], raw, nil
}
func (c *Client) Measurement(ctx context.Context, id int64) (Measurement, []byte, error) {
	if id < 1 {
		return Measurement{}, nil, &Error{Code: "invalid-measurement-id"}
	}
	raw, err := c.request(ctx, "GET", fmt.Sprintf("measurements/%d/", id), url.Values{"format[datetime]": {"unix"}}, nil, 200)
	if err != nil {
		return Measurement{}, nil, err
	}
	m, err := parseMeasurement(raw)
	if err == nil && m.ID != id {
		err = &Error{Code: "measurement-identity-mismatch"}
	}
	return m, raw, err
}
func (c *Client) Find(ctx context.Context, marker string) ([]Measurement, []byte, error) {
	if !markerPattern.MatchString(marker) {
		return nil, nil, &Error{Code: "invalid-ownership-marker"}
	}
	raw, err := c.request(ctx, "GET", "measurements/my/", url.Values{"description": {marker}, "page_size": {"100"}, "format[datetime]": {"unix"}}, nil, 200)
	if err != nil {
		return nil, nil, err
	}
	var list struct {
		Results []json.RawMessage `json:"results"`
		Next    *string           `json:"next"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Results) > 100 || list.Next != nil && *list.Next != "" {
		return nil, nil, &Error{Code: "ambiguous-ownership-search"}
	}
	out := []Measurement{}
	for _, row := range list.Results {
		m, e := parseMeasurement(row)
		if e != nil {
			return nil, nil, e
		}
		if m.Description == marker {
			out = append(out, m)
		}
	}
	return out, raw, nil
}
func (c *Client) Results(ctx context.Context, id int64, start, stop time.Time) ([]Result, []byte, error) {
	if id < 1 || start.IsZero() || stop.Before(start) || stop.Sub(start) > 8*24*time.Hour {
		return nil, nil, &Error{Code: "invalid-results-window"}
	}
	raw, err := c.request(ctx, "GET", fmt.Sprintf("measurements/%d/results/", id), url.Values{"start": {strconv.FormatInt(start.Unix(), 10)}, "stop": {strconv.FormatInt(stop.Unix(), 10)}}, nil, 200)
	if err != nil {
		return nil, nil, err
	}
	results, err := ParseResults(raw, id, start, stop)
	return results, raw, err
}
func (c *Client) Stop(ctx context.Context, id int64) error {
	if id < 1 {
		return &Error{Code: "invalid-measurement-id"}
	}
	_, err := c.request(ctx, "DELETE", fmt.Sprintf("measurements/%d/", id), nil, nil, 204)
	return err
}
