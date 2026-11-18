// Package integrations reads allowlisted public provider APIs. It never writes
// provider data or treats an external address allocation as local authority.
package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxSnapshotBytes = 8 << 20
const maxProviderRows = 1000

type Config struct {
	Enabled                bool     `json:"enabled"`
	BaseURL                string   `json:"baseURL"`
	AllowedOrigins         []string `json:"allowedOrigins"`
	AllowLoopbackHTTP      bool     `json:"allowLoopbackHTTP"`
	RequestIntervalSeconds int      `json:"requestIntervalSeconds"`
}
type Network struct {
	ID        int64  `json:"id"`
	ASN       uint32 `json:"asn"`
	Name      string `json:"name"`
	Website   string `json:"website"`
	IRRASSet  string `json:"irr_as_set"`
	Prefixes4 *int64 `json:"info_prefixes4"`
	Prefixes6 *int64 `json:"info_prefixes6"`
	Status    string `json:"status"`
}
type NetIXLAN struct {
	ID          int64  `json:"id"`
	NetID       int64  `json:"net_id"`
	IXID        int64  `json:"ix_id"`
	IXLANID     int64  `json:"ixlan_id"`
	ASN         uint32 `json:"asn"`
	Name        string `json:"name"`
	IPv4        string `json:"ipaddr4"`
	IPv6        string `json:"ipaddr6"`
	Speed       int64  `json:"speed"`
	IsRSPeer    bool   `json:"is_rs_peer"`
	Operational *bool  `json:"operational,omitempty"`
	Status      string `json:"status"`
}
type Exchange struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	City    string `json:"city"`
	Country string `json:"country"`
	Website string `json:"website"`
	Status  string `json:"status"`
}
type Facility struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	City     string `json:"city"`
	Country  string `json:"country"`
	Address1 string `json:"address1"`
	Address2 string `json:"address2"`
	Zipcode  string `json:"zipcode"`
	Website  string `json:"website"`
	Status   string `json:"status"`
}
type Snapshot struct {
	Version    int        `json:"version"`
	Provider   string     `json:"provider"`
	FetchedAt  time.Time  `json:"fetchedAt"`
	Network    Network    `json:"network"`
	NetIXLANs  []NetIXLAN `json:"netixlans"`
	Exchanges  []Exchange `json:"exchanges"`
	Facilities []Facility `json:"facilities"`
}
type Provider interface {
	Fetch(context.Context, uint32, int64, int64) (Snapshot, error)
	Origin() string
}

type Client struct {
	config    Config
	base      *url.URL
	http      *http.Client
	mu        sync.Mutex
	nextFetch time.Time
}

func LoadConfig(path string) (Config, error) {
	var config Config
	if path == "" {
		return config, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return config, errors.New("provider config must be a private regular file under 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return config, errors.New("invalid provider configuration")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return config, errors.New("provider config must contain one JSON object")
	}
	return config, config.Validate()
}
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimRight(u.Path, "/") != "/api" {
		return errors.New("PeeringDB baseURL must be an origin plus /api")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !c.AllowLoopbackHTTP || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return errors.New("provider requires HTTPS; HTTP only allowed for explicit literal loopback fixtures")
		}
	}
	origins := c.AllowedOrigins
	if len(origins) == 0 {
		origins = []string{"https://www.peeringdb.com", "https://peeringdb.com"}
	}
	allowed := false
	for _, raw := range origins {
		origin, err := url.Parse(raw)
		if err != nil || origin.User != nil || origin.Host == "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return errors.New("invalid provider origin allowlist")
		}
		if origin.Scheme == u.Scheme && origin.Host == u.Host {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("provider origin is not explicitly allowlisted")
	}
	if c.RequestIntervalSeconds != 0 && (c.RequestIntervalSeconds < 5 || c.RequestIntervalSeconds > 3600) {
		return errors.New("provider interval must be 5..3600 seconds")
	}
	return nil
}
func NewClient(c Config) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, nil
	}
	if c.RequestIntervalSeconds == 0 {
		c.RequestIntervalSeconds = 30
	}
	base, _ := url.Parse(c.BaseURL)
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 8 * time.Second, MaxIdleConnsPerHost: 2}
	return &Client{config: c, base: base, http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Origin() string {
	if c == nil {
		return ""
	}
	return c.base.String()
}
func (c *Client) get(ctx context.Context, kind string, id int64, query url.Values, target any) error {
	switch kind {
	case "net", "netixlan", "ix", "fac":
	default:
		return errors.New("unsupported provider object")
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + kind
	if id > 0 {
		u.Path += "/" + strconv.FormatInt(id, 10)
	}
	query.Set("depth", "0")
	query.Set("limit", strconv.Itoa(maxProviderRows+1))
	u.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "MoeDove-IXP/1 public-read-only")
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxSnapshotBytes+1))
	if err != nil || len(raw) > MaxSnapshotBytes {
		return errors.New("provider response exceeded size limit or was incomplete")
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return errors.New("provider returned invalid JSON")
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("provider response omitted data array")
	}
	if err = json.Unmarshal(envelope.Data, target); err != nil {
		return errors.New("provider data did not match supported typed schema")
	}
	return nil
}
func (c *Client) Fetch(ctx context.Context, asn uint32, ixID, facilityID int64) (Snapshot, error) {
	var result Snapshot
	if c == nil {
		return result, errors.New("PeeringDB provider is disabled")
	}
	if asn == 0 || asn == 23456 || asn == 65535 || asn == 4294967295 || ixID < 0 || facilityID < 0 {
		return result, errors.New("invalid ASN or provider object ID")
	}
	c.mu.Lock()
	now := time.Now()
	if now.Before(c.nextFetch) {
		c.mu.Unlock()
		return result, errors.New("provider request budget reached; retry later")
	}
	c.nextFetch = now.Add(time.Duration(c.config.RequestIntervalSeconds) * time.Second)
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	networks := []Network{}
	if err := c.get(ctx, "net", 0, url.Values{"asn": {strconv.FormatUint(uint64(asn), 10)}, "status": {"ok"}, "fields": {"id,asn,name,website,irr_as_set,info_prefixes4,info_prefixes6,status"}}, &networks); err != nil {
		return result, err
	}
	if len(networks) != 1 || networks[0].ASN != asn {
		return result, errors.New("provider did not return exactly one matching ASN")
	}
	result = Snapshot{Version: 1, Provider: "peeringdb", FetchedAt: time.Now().UTC(), Network: networks[0], NetIXLANs: []NetIXLAN{}, Exchanges: []Exchange{}, Facilities: []Facility{}}
	query := url.Values{"net_id": {strconv.FormatInt(result.Network.ID, 10)}, "status__in": {"ok,not-operational"}, "fields": {"id,net_id,ix_id,ixlan_id,asn,name,ipaddr4,ipaddr6,speed,is_rs_peer,operational,status"}}
	if ixID > 0 {
		query.Set("ix_id", strconv.FormatInt(ixID, 10))
	}
	if err := c.get(ctx, "netixlan", 0, query, &result.NetIXLANs); err != nil {
		return result, err
	}
	if ixID > 0 {
		if err := c.get(ctx, "ix", ixID, url.Values{"fields": {"id,name,city,country,website,status"}}, &result.Exchanges); err != nil {
			return result, err
		}
		if len(result.Exchanges) != 1 || result.Exchanges[0].ID != ixID {
			return result, errors.New("provider exchange ID mismatch")
		}
	}
	if facilityID > 0 {
		if err := c.get(ctx, "fac", facilityID, url.Values{"fields": {"id,name,city,country,address1,address2,zipcode,website,status"}}, &result.Facilities); err != nil {
			return result, err
		}
		if len(result.Facilities) != 1 || result.Facilities[0].ID != facilityID {
			return result, errors.New("provider facility ID mismatch")
		}
	}
	return Normalize(result, time.Now())
}
func validPrefixCount(n *int64) bool { return n == nil || (*n >= 0 && *n <= 10000000) }
func validID(id int64) bool          { return id > 0 && id <= 9007199254740991 }
func text(raw string, limit int) bool {
	return len(raw) <= limit && !strings.ContainsAny(raw, "\x00\r")
}
func website(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && len(raw) <= 2048
}
func Normalize(s Snapshot, now time.Time) (Snapshot, error) {
	if s.Version != 1 || s.Provider != "peeringdb" || s.FetchedAt.IsZero() || s.FetchedAt.After(now.Add(5*time.Minute)) || s.FetchedAt.Before(now.Add(-7*24*time.Hour)) {
		return s, errors.New("snapshot version/provider/time is invalid or older than seven days")
	}
	n := s.Network
	if !validID(n.ID) || n.ASN == 0 || n.ASN == 23456 || n.ASN == 65535 || n.ASN == 4294967295 || strings.TrimSpace(n.Name) == "" || !text(n.Name, 2000) || !website(n.Website) || !text(n.IRRASSet, 2000) || !validPrefixCount(n.Prefixes4) || !validPrefixCount(n.Prefixes6) || n.Status != "ok" {
		return s, errors.New("invalid published network record")
	}
	if len(s.NetIXLANs) > maxProviderRows || len(s.Exchanges) > maxProviderRows || len(s.Facilities) > maxProviderRows {
		return s, errors.New("provider snapshot exceeds row limit")
	}
	seen := map[int64]bool{}
	for i, p := range s.NetIXLANs {
		if !validID(p.ID) || seen[p.ID] || !validID(p.IXID) || !validID(p.IXLANID) || p.NetID != n.ID || p.ASN != n.ASN || (p.Status != "ok" && p.Status != "not-operational") || p.Speed < 0 || p.Speed > 640000000 || !text(p.Name, 2000) {
			return s, errors.New("invalid network-to-IX binding")
		}
		seen[p.ID] = true
		for _, v := range []struct {
			raw  string
			ipv4 bool
		}{{p.IPv4, true}, {p.IPv6, false}} {
			if v.raw == "" {
				continue
			}
			ip, err := netip.ParseAddr(v.raw)
			if err != nil || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.Is4() != v.ipv4 {
				return s, errors.New("invalid external peering address")
			}
			if v.ipv4 {
				s.NetIXLANs[i].IPv4 = ip.String()
			} else {
				s.NetIXLANs[i].IPv6 = ip.String()
			}
		}
	}
	seen = map[int64]bool{}
	for _, p := range s.Exchanges {
		if !validID(p.ID) || seen[p.ID] || p.Status != "ok" || strings.TrimSpace(p.Name) == "" || !text(p.Name, 2000) || !text(p.City, 500) || !text(p.Country, 3) || !website(p.Website) {
			return s, errors.New("invalid exchange record")
		}
		seen[p.ID] = true
	}
	seen = map[int64]bool{}
	for _, p := range s.Facilities {
		if !validID(p.ID) || seen[p.ID] || p.Status != "ok" || strings.TrimSpace(p.Name) == "" || !text(p.Name, 2000) || !text(p.City, 500) || !text(p.Country, 3) || !text(p.Address1, 2000) || !text(p.Address2, 2000) || !text(p.Zipcode, 100) || !website(p.Website) {
			return s, errors.New("invalid facility record")
		}
		seen[p.ID] = true
	}
	if s.NetIXLANs == nil {
		s.NetIXLANs = []NetIXLAN{}
	}
	if s.Exchanges == nil {
		s.Exchanges = []Exchange{}
	}
	if s.Facilities == nil {
		s.Facilities = []Facility{}
	}
	sort.Slice(s.NetIXLANs, func(i, j int) bool { return s.NetIXLANs[i].ID < s.NetIXLANs[j].ID })
	sort.Slice(s.Exchanges, func(i, j int) bool { return s.Exchanges[i].ID < s.Exchanges[j].ID })
	sort.Slice(s.Facilities, func(i, j int) bool { return s.Facilities[i].ID < s.Facilities[j].ID })
	s.FetchedAt = s.FetchedAt.UTC()
	return s, nil
}
func Parse(raw []byte, now time.Time) (Snapshot, error) {
	var s Snapshot
	if len(raw) == 0 || len(raw) > MaxSnapshotBytes {
		return s, errors.New("snapshot must be 1 byte..8 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return s, errors.New("invalid typed provider snapshot")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return s, errors.New("snapshot must contain exactly one JSON object")
	}
	return Normalize(s, now)
}
func Canonical(s Snapshot) ([]byte, string, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, "", err
	}
	raw = append(raw, '\n')
	return raw, Checksum(raw), nil
}
func Checksum(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
