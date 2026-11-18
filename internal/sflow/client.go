package sflow

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

const ControlTimeout = 15 * time.Second

// ControlError retains only a safe HTTP status/retry decision, never response
// text, URLs, credentials or transport diagnostics.
type ControlError struct {
	StatusCode int
	Temporary  bool
}

func (e *ControlError) Error() string {
	if e.StatusCode == 0 {
		return "sFlow control transport unavailable"
	}
	return "sFlow control request rejected (HTTP " + strconv.Itoa(e.StatusCode) + ")"
}
func RetryableControl(err error) bool {
	var control *ControlError
	return errors.As(err, &control) && control.Temporary
}
func statusError(status int) error {
	return &ControlError{StatusCode: status, Temporary: status == 408 || status == 409 || status == 425 || status == 429 || status >= 500 && status <= 599}
}

var errControlRedirect = errors.New("sFlow control redirects forbidden")

type Exporter struct {
	ID           string `json:"id"`
	SwitchID     string `json:"switchId"`
	SourceIP     string `json:"sourceIp"`
	AgentAddress string `json:"agentAddress"`
	SubAgentID   uint32 `json:"subAgentId"`
}
type Config struct {
	Endpoint              string            `json:"endpoint"`
	Bind                  string            `json:"bind"`
	Exporters             []Exporter        `json:"exporters"`
	MaxDatagramsPerSecond int               `json:"maxDatagramsPerSecond"`
	Forwarding            *ForwardingPolicy `json:"forwarding,omitempty"`
}

func validIP(value string) bool {
	a, err := netip.ParseAddr(value)
	return err == nil && !a.Is4In6() && a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast() && a.String() == value
}
func endpointURL(endpoint, path string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Path != "/api/sflow" {
		return "", errors.New("sFlow control endpoint must be a fixed HTTPS /api/sflow URL")
	}
	u.Path += path
	return u.String(), nil
}
func ValidateConfig(c Config) error {
	if _, err := endpointURL(c.Endpoint, ""); err != nil {
		return err
	}
	bind, err := netip.ParseAddrPort(c.Bind)
	if err != nil || bind.Port() == 0 || bind.Addr().Is4In6() || bind.Addr().IsMulticast() || bind.Addr().Zone() != "" || bind.String() != c.Bind {
		return errors.New("sFlow listener requires an explicit canonical IP:port")
	}
	if len(c.Exporters) == 0 || len(c.Exporters) > 64 || c.MaxDatagramsPerSecond < 1 || c.MaxDatagramsPerSecond > 1000 {
		return errors.New("sFlow configuration needs 1..64 exporters and a 1..1000 datagrams/second limit")
	}
	if c.Forwarding != nil {
		if err := ValidateForwardingPolicy(*c.Forwarding); err != nil {
			return err
		}
	}
	ids, tuples := map[string]bool{}, map[string]bool{}
	for _, e := range c.Exporters {
		if !identifier.MatchString(e.ID) || !identifier.MatchString(e.SwitchID) || !validIP(e.SourceIP) || !validIP(e.AgentAddress) || ids[e.ID] {
			return errors.New("invalid or duplicate sFlow exporter")
		}
		ids[e.ID] = true
		key := e.SourceIP + "/" + e.AgentAddress + "/" + strconv.FormatUint(uint64(e.SubAgentID), 10)
		if tuples[key] {
			return errors.New("ambiguous sFlow exporter tuple")
		}
		tuples[key] = true
	}
	return nil
}
func readPrivate(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("private sFlow file cannot be opened")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > limit {
		return nil, errors.New("sFlow file must be a bounded private regular file (0600)")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, errors.New("sFlow file must be owned by this user with one link")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("private sFlow file read failed")
	}
	return b, nil
}
func ReadConfig(path string) (Config, error) {
	var c Config
	b, err := readPrivate(path, 65536)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("invalid private sFlow configuration")
	}
	if c.MaxDatagramsPerSecond == 0 {
		c.MaxDatagramsPerSecond = 100
	}
	return c, ValidateConfig(c)
}
func ReadToken(path string) (string, error) {
	b, err := readPrivate(path, 256)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 16 || len(token) > 256 {
		return "", errors.New("invalid private sFlow API token")
	}
	for _, r := range token {
		if r < 33 || r > 126 {
			return "", errors.New("invalid private sFlow API token")
		}
	}
	return token, nil
}
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: ControlTimeout, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return errControlRedirect }}
}
func controlRequest(ctx context.Context, client *http.Client, endpoint, token, method, path string, input any, output any) error {
	u, err := endpointURL(endpoint, path)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil || len(raw) > 1<<20 {
			return errors.New("sFlow upload exceeds bound")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return errors.New("invalid sFlow control request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errControlRedirect }
	if copy.Timeout <= 0 || copy.Timeout > ControlTimeout {
		copy.Timeout = ControlTimeout
	}
	res, err := copy.Do(req)
	if err != nil {
		if errors.Is(err, errControlRedirect) {
			return errControlRedirect
		}
		return &ControlError{Temporary: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return statusError(res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil {
		return &ControlError{Temporary: true}
	}
	if len(raw) > 65536 {
		return errors.New("sFlow control request rejected or exceeded response limit")
	}
	if output != nil {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(output) != nil || d.Decode(new(any)) != io.EOF {
			return errors.New("invalid sFlow control response")
		}
	}
	return nil
}
func FetchBinding(ctx context.Context, client *http.Client, endpoint, token string, e Exporter) (Binding, error) {
	var b Binding
	u, err := endpointURL(endpoint, "/bindings")
	if err != nil {
		return b, err
	}
	parsed, _ := url.Parse(u)
	parsed.RawQuery = url.Values{"exporterId": []string{e.ID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", parsed.String(), nil)
	if err != nil {
		return b, errors.New("invalid sFlow binding request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	copy := *client
	copy.Timeout = ControlTimeout
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errControlRedirect }
	res, err := copy.Do(req)
	if err != nil {
		if errors.Is(err, errControlRedirect) {
			return b, errControlRedirect
		}
		return b, &ControlError{Temporary: true}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return b, statusError(res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil {
		return b, &ControlError{Temporary: true}
	}
	if len(raw) > 65536 {
		return b, errors.New("sFlow binding request rejected")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || dec.Decode(new(any)) != io.EOF || b.ExporterID != e.ID || b.SwitchID != e.SwitchID || !revisionPattern.MatchString(b.MappingRevision) || b.IssuedAt.IsZero() || b.ExpiresAt.IsZero() || b.ExpiresAt.Sub(b.IssuedAt) > 5*time.Minute {
		return Binding{}, errors.New("invalid or expired sFlow exporter binding")
	}
	if !b.IssuedAt.Before(b.ExpiresAt) {
		return Binding{}, &ControlError{StatusCode: 409, Temporary: true}
	}
	return b, nil
}
func Upload(ctx context.Context, client *http.Client, endpoint, token string, b Batch) error {
	return controlRequest(ctx, client, endpoint, token, "POST", "/ingest", b, nil)
}

func UploadWithDeliveries(ctx context.Context, client *http.Client, endpoint, token string, b Batch) (*DeliveryEnvelope, error) {
	if !b.RequestDeliveries {
		return nil, errors.New("delivery upload requires explicit forwarding request")
	}
	if err := ValidateBatch(b); err != nil {
		return nil, err
	}
	var response map[string]json.RawMessage
	if err := controlRequest(ctx, client, endpoint, token, "POST", "/ingest", b, &response); err != nil {
		return nil, err
	}
	raw := response["deliveryEnvelope"]
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var envelope DeliveryEnvelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&envelope) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid per-packet delivery authorization")
	}
	if err := ValidateDeliveryEnvelope(envelope, b); err != nil {
		return nil, err
	}
	return &envelope, nil
}

// Delivery receipts are attempted once. Failure never retries UDP or reuses a
// previous authorization envelope.
func UploadDeliveryReport(ctx context.Context, client *http.Client, endpoint, token string, r DeliveryReport) error {
	if r.Version != 1 || !validNonce(r.RequestNonce) || !validDigest(r.DatagramDigest) || !identifier.MatchString(r.ExporterID) || !identifier.MatchString(r.SwitchID) || !revisionPattern.MatchString(r.MappingRevision) || r.ObservedAt.IsZero() || len(r.Receipts) > MaxDeliveries {
		return errors.New("invalid bounded delivery report")
	}
	seen := map[string]bool{}
	for _, receipt := range r.Receipts {
		if !identifier.MatchString(receipt.ReceiverID) || !revisionPattern.MatchString(receipt.ReceiverRevision) || seen[receipt.ReceiverID] || receipt.Datagrams < 0 || receipt.Datagrams > MaxSamples || receipt.Bytes < 0 || receipt.Bytes > MaxSamples*MaxRelayDatagramBytes {
			return errors.New("invalid delivery receipt counters")
		}
		seen[receipt.ReceiverID] = true
		switch receipt.Outcome {
		case "sent-to-socket", "partial-send-error", "send-error", "expired", "encode-rejected", "rate-limited", "blocked-policy":
		default:
			return errors.New("invalid delivery receipt outcome")
		}
		if receipt.Datagrams == 0 && receipt.Bytes != 0 || receipt.Datagrams > 0 && (receipt.Bytes < receipt.Datagrams || receipt.Bytes > receipt.Datagrams*MaxRelayDatagramBytes) {
			return errors.New("delivery byte count disagrees with datagrams")
		}
		if (receipt.Outcome == "sent-to-socket" || receipt.Outcome == "partial-send-error") && receipt.Datagrams == 0 {
			return errors.New("socket outcome requires an actual datagram write")
		}
		if receipt.Outcome != "sent-to-socket" && receipt.Outcome != "partial-send-error" && receipt.Datagrams != 0 {
			return errors.New("non-send outcome cannot claim written datagrams")
		}
	}
	return controlRequest(ctx, client, endpoint, token, "POST", "/delivery-receipts", r, nil)
}
