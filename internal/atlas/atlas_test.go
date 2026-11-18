package atlas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureKey = "private-fixture-key-never-real"

func fixtureSpec() Spec {
	return Spec{Type: "traceroute", AF: 4, Target: "192.0.2.10", ProbeIDs: []int64{12, 11}, IsOneoff: true, StartAt: time.Now().UTC().Add(time.Minute).Truncate(time.Second), Description: "MoeDove Atlas " + strings.Repeat("a", 22) + "/" + strings.Repeat("b", 22) + "/" + strings.Repeat("c", 22)}
}
func fast(c *Client) { c.mu.Lock(); c.next = time.Time{}; c.mu.Unlock() }
func TestAtlasPrivateConfigurationAndSpecBounds(t *testing.T) {
	if c, err := NewClient(Config{}); err != nil || c != nil {
		t.Fatal("default provider not disabled")
	}
	for _, raw := range []string{"http://atlas.ripe.net", "https://example.net", "https://atlas.ripe.net.evil", "https://atlas.ripe.net/?key=secret", "https://user:pass@atlas.ripe.net", "http://localhost:1000"} {
		if _, err := NewClient(Config{Enabled: true, BaseURL: raw, APIKey: fixtureKey, AllowLoopbackHTTP: true}); err == nil {
			t.Fatal("untrusted provider origin accepted", raw)
		}
	}
	path := filepath.Join(t.TempDir(), "atlas.json")
	raw, _ := json.Marshal(Config{Enabled: true, APIKey: fixtureKey})
	os.WriteFile(path, raw, 0644)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("public key file accepted")
	}
	os.Chmod(path, 0600)
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	os.Symlink(path, path+".link")
	if _, err := LoadConfig(path + ".link"); err == nil {
		t.Fatal("key file symlink accepted")
	}
	s := fixtureSpec()
	normalized, e, err := NormalizeSpec(s, time.Now())
	if err != nil || e.Credits != 120 || e.Samples != 2 || normalized.ProbeIDs[0] != 11 || s.ProbeIDs[0] != 12 {
		t.Fatal(normalized, e, err)
	}
	for _, mutate := range []func(*Spec){func(s *Spec) { s.Target = "127.0.0.1" }, func(s *Spec) { s.Target = "10.0.0.1" }, func(s *Spec) { s.Target = "100.64.1.1" }, func(s *Spec) { s.Target = "https://example.net" }, func(s *Spec) { s.ProbeIDs = []int64{1, 1} }, func(s *Spec) { s.IsOneoff = false; s.IntervalSeconds = 1; s.DurationSeconds = 86400 }, func(s *Spec) { s.DurationSeconds = 100 }} {
		bad := fixtureSpec()
		mutate(&bad)
		if _, _, err := NormalizeSpec(bad, time.Now()); err == nil {
			t.Fatal("unbounded/unsafe spec accepted")
		}
	}
	s = fixtureSpec()
	s.Type = "ping"
	s.IsOneoff = false
	s.IntervalSeconds = 300
	s.DurationSeconds = 600
	_, e, err = NormalizeSpec(s, time.Now())
	if err != nil || e.Samples != 6 || e.Credits != 18 {
		t.Fatal(e, err)
	}
}
func TestAtlasCreateUsesExplicitPrivateBoundedPayload(t *testing.T) {
	var submitted map[string]any
	mode := "ok"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v2/measurements/" || r.Header.Get("Authorization") != "Key "+fixtureKey || r.URL.Query().Get("key") != "" {
			t.Error("wrong endpoint or key transport")
		}
		if r.Method != "POST" {
			t.Error("unexpected method")
		}
		_ = json.NewDecoder(r.Body).Decode(&submitted)
		switch mode {
		case "redirect":
			w.Header().Set("Location", "http://192.0.2.200/secret")
			w.WriteHeader(302)
		case "bad":
			w.WriteHeader(201)
			fmt.Fprint(w, `{"measurements":[]}`)
		case "server":
			w.WriteHeader(503)
			fmt.Fprint(w, fixtureKey)
		case "denied":
			w.WriteHeader(403)
			fmt.Fprint(w, fixtureKey)
		case "accepted200", "accepted202", "accepted204", "timeout408":
			code := 200
			switch mode {
			case "accepted202":
				code = 202
			case "accepted204":
				code = 204
			case "timeout408":
				code = 408
			}
			w.WriteHeader(code)
		default:
			w.WriteHeader(201)
			fmt.Fprint(w, `{"measurements":[123]}`)
		}
	}))
	defer server.Close()
	c, err := NewClient(Config{Enabled: true, BaseURL: server.URL, APIKey: fixtureKey, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	s := fixtureSpec()
	id, _, err := c.Create(context.Background(), s)
	if err != nil || id != 123 {
		t.Fatal(id, err)
	}
	definition := submitted["definitions"].([]any)[0].(map[string]any)
	selection := submitted["probes"].([]any)[0].(map[string]any)
	if definition["is_public"] != false || definition["auto_topup"] != false || definition["target_update_hours"] != float64(0) || definition["protocol"] != "ICMP" || definition["packets"] != float64(3) || definition["max_hops"] != float64(32) || selection["type"] != "probes" || selection["value"] != "11,12" || submitted["stop_time"] != nil {
		t.Fatal("unsafe create payload", submitted)
	}
	s.IsOneoff = false
	s.IntervalSeconds = 600
	s.DurationSeconds = 3600
	fast(c)
	if _, _, err = c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if submitted["stop_time"].(float64)-submitted["start_time"].(float64) != 3600 {
		t.Fatal("recurring stop bound missing")
	}
	for _, m := range []string{"redirect", "bad", "server", "denied", "accepted200", "accepted202", "accepted204", "timeout408"} {
		mode = m
		fast(c)
		before := calls
		_, _, err = c.Create(context.Background(), fixtureSpec())
		if err == nil || strings.Contains(err.Error(), fixtureKey) || calls != before+1 {
			t.Fatal("create error leaked/retried/followed redirect", m, err)
		}
		if m != "denied" && !IsUncertain(err) {
			t.Fatal("ambiguous creation not marked uncertain", m)
		}
	}
}
func TestAtlasTypedDiscoveryOwnershipAndStop(t *testing.T) {
	spec := fixtureSpec()
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch {
		case strings.HasSuffix(r.URL.Path, "probes/"):
			if r.URL.Query().Get("asn_v4") != "64501" || r.URL.Query().Get("status") != "1" || r.URL.Query().Get("page_size") != "200" {
				t.Error("unbounded discovery")
			}
			fmt.Fprint(w, `{"next":"https://untrusted.example/ignored","results":[{"id":11,"asn_v4":64501,"address_v4":"192.0.2.2","is_public":true,"is_anchor":false,"status":{"id":1},"last_connected":1700000000},{"id":12,"asn_v4":64502,"address_v4":"192.0.2.3","is_public":true,"status":{"id":1}}]}`)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": 123, "type": spec.Type, "af": 4, "target_ip": spec.Target, "description": spec.Description, "is_public": false, "is_oneoff": true, "start_time": spec.StartAt.Unix(), "status": map[string]any{"id": 2}})
		}
	}))
	defer server.Close()
	c, _ := NewClient(Config{Enabled: true, BaseURL: server.URL, APIKey: fixtureKey, AllowLoopbackHTTP: true})
	probes, _, err := c.Discover(context.Background(), 64501, 4, false)
	if err != nil || len(probes) != 1 || probes[0].ID != 11 {
		t.Fatal(probes, err)
	}
	fast(c)
	m, _, err := c.Measurement(context.Background(), 123)
	if err != nil || !Owns(m, spec) {
		t.Fatal(m, err)
	}
	m.IsPublic = true
	if Owns(m, spec) || !MatchesIntent(m, spec) {
		t.Fatal("privacy mismatch conflated with unowned measurement")
	}
	m.Description = "unrelated"
	if MatchesIntent(m, spec) {
		t.Fatal("foreign measurement treated owned")
	}
	fast(c)
	if err = c.Stop(context.Background(), 123); err != nil || path != "/api/v2/measurements/123/" {
		t.Fatal(err)
	}
}
func TestAtlasResultsPreserveLossAndMultipleHopResponses(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := fmt.Sprintf(`[{"msm_id":9,"prb_id":11,"type":"ping","af":4,"timestamp":%d,"dst_addr":"192.0.2.10","src_addr":"192.0.2.2","sent":3,"rcvd":0,"min":-1,"avg":-1,"max":-1},{"msm_id":9,"prb_id":12,"type":"traceroute","af":4,"timestamp":%d,"dst_addr":"192.0.2.10","result":[{"hop":1,"result":[{"from":"192.0.2.1","rtt":1.2},{"from":"192.0.2.4","rtt":1.8},{"x":"*"}]}]}]`, now.Unix(), now.Unix())
	rows, err := ParseResults([]byte(raw), 9, now.Add(-time.Minute), now)
	if err != nil || len(rows) != 2 {
		t.Fatal(err)
	}
	if rows[0].AvgRTT != nil || rows[0].MinRTT != nil || rows[0].Received == nil || *rows[0].Received != 0 {
		t.Fatal("packet loss fabricated as zero RTT")
	}
	replies := rows[1].Hops[0].Replies
	if len(replies) != 3 || !replies[2].Timeout || replies[2].RTT != nil || replies[0].Address == replies[1].Address {
		t.Fatal("multipath/timeout collapsed")
	}
	if _, err = ParseResults([]byte(raw), 10, now.Add(-time.Minute), now); err == nil {
		t.Fatal("wrong measurement ID accepted")
	}
	if _, err = ParseResults([]byte(raw), 9, now.Add(time.Second), now.Add(time.Hour)); err == nil {
		t.Fatal("out-of-window data accepted")
	}
	if _, err = ParseResults([]byte(`{}`), 9, now.Add(-time.Minute), now); err == nil {
		t.Fatal("unknown response became empty success")
	}
	var out strings.Builder
	fmt.Fprint(&out, Config{APIKey: fixtureKey})
	if strings.Contains(out.String(), fixtureKey) {
		t.Fatal("config logging leaked API key")
	}
}

func TestAtlasIPv6ProbeDiscoveryCanonicalizesAddresses(t *testing.T) {
	raw := []byte(`{"results":[{"id":41,"asn_v6":64501,"address_v6":"2001:DB8:0000:0000::4","country_code":"NL","is_public":true,"status":{"id":1}},{"id":42,"asn_v6":64501,"address_v6":"fe80::1","is_public":true,"status":{"id":1}}]}`)
	probes, err := parseProbes(raw, 64501, 6, false)
	if err != nil || len(probes) != 1 || probes[0].AddressV6 != "2001:db8::4" {
		t.Fatal("valid IPv6 probe normalization failed", probes, err)
	}
}

func TestAtlasOfficialProbeErrorsDoNotInventObservedTargets(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw := fmt.Sprintf(`[{"msm_id":99,"prb_id":1,"af":4,"type":"ping","timestamp":%d,"avg":0,"result":[{"error":"dns resolution failed"}]},{"msm_id":99,"prb_id":2,"af":6,"type":"traceroute","timestamp":%d,"result":[{"error":"name resolution failed"}]},{"msm_id":99,"prb_id":3,"af":4,"type":"traceroute","timestamp":%d,"dst_addr":"192.0.2.10","result":[{"hop":1,"error":"send failed"},{"hop":2,"result":[{"from":"192.0.2.1","rtt":1.2,"err":"H"}]}]}]`, now.Unix(), now.Unix(), now.Unix())
	rows, err := ParseResults([]byte(raw), 99, now.Add(-time.Minute), now)
	if err != nil || len(rows) != 3 {
		t.Fatal("legitimate errors discarded the batch", err)
	}
	for _, row := range rows[:2] {
		if row.TargetAddress != "" || row.Error != "probe-error" || row.AvgRTT != nil {
			t.Fatal("planned target/success latency invented for a probe error")
		}
	}
	if len(rows[1].Hops) != 0 || rows[2].Hops[0].Error != "probe-error" {
		t.Fatal("init/hop error shape lost")
	}
	reply := rows[2].Hops[1].Replies[0]
	if reply.Error != "icmp-error" || reply.RTT != nil || reply.Address != "192.0.2.1" {
		t.Fatal("ICMP error shown as a successful timed reply")
	}
	bad := fmt.Sprintf(`[{"msm_id":99,"prb_id":3,"af":4,"type":"traceroute","timestamp":%d,"dst_addr":"192.0.2.10","result":[{"hop":1,"result":[{"x":"*","from":"192.0.2.1","rtt":1.2}]}]}]`, now.Unix())
	if _, err := ParseResults([]byte(bad), 99, now.Add(-time.Minute), now); err == nil {
		t.Fatal("contradictory timeout and success accepted")
	}
}
