package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/routing"
	"github.com/moedove/ixp-manager/internal/telemetry"
)

const quaggaCollectorPassword = "native-quagga-test-password"

type quaggaCollectorManager struct {
	server                    *httptest.Server
	client                    *http.Client
	csrf                      string
	bindings, ingest, optedIn atomic.Int32
}

func openQuaggaCollectorManager(t *testing.T, dir string) *quaggaCollectorManager {
	t.Helper()
	const setupToken = "test-only-collector-setup-token"
	manager, err := app.New(app.Config{DataDir: filepath.Join(dir, "manager"), SetupToken: setupToken, SecureCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	f := &quaggaCollectorManager{}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/monitoring/bindings":
			f.bindings.Add(1)
			if r.URL.Query().Get("routerIdentity") == "quagga" {
				f.optedIn.Add(1)
			}
		case "/api/monitoring/ingest":
			f.ingest.Add(1)
		}
		manager.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	f.client = f.server.Client()
	f.client.Timeout = 5 * time.Second
	f.client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	session := f.request(t, "POST", "/api/setup", map[string]any{
		"name": "Quagga fixture admin", "email": "admin@example.test", "password": quaggaCollectorPassword,
		"ixpName": "Quagga fixture IXP", "asn": 64500, "setupToken": setupToken,
	}, "")
	f.csrf = collectorField(t, session, "csrfToken")
	return f
}

func collectorField(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	value, ok := body[key].(string)
	if !ok || value == "" {
		t.Fatalf("fixture response missing %s", key)
	}
	return value
}

func (f *quaggaCollectorManager) request(t *testing.T, method, path string, body map[string]any, revision string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-CSRF-Token", f.csrf)
	if revision != "" {
		req.Header.Set("If-Match", `"`+revision+`"`)
	}
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		t.Fatalf("fixture %s %s returned %d", method, path, res.StatusCode)
	}
	var reply map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func (f *quaggaCollectorManager) resource(t *testing.T, kind string, body map[string]any) string {
	t.Helper()
	return collectorField(t, f.request(t, "POST", "/api/resources/"+kind, body, ""), "id")
}

func collectorQuaggaFrame(kind byte, body ...byte) []byte {
	header := bytes.Repeat([]byte{255}, 16)
	header = binary.BigEndian.AppendUint16(header, uint16(len(body)+19))
	header = append(header, kind)
	return append(header, body...)
}

func readCollectorQuaggaFrame(conn *net.TCPConn, want byte) ([]byte, error) {
	var header [19]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(header[16:18]))
	if n < len(header) || n > 4096 || header[18] != want || !bytes.Equal(header[:16], bytes.Repeat([]byte{255}, 16)) {
		return nil, errors.New("unexpected fixture BGP frame")
	}
	data := make([]byte, n-len(header))
	_, err := io.ReadFull(conn, data)
	return data, err
}

func connectCollectorQuaggaPeer(t *testing.T, address, server string, ipv6 bool) *net.TCPConn {
	t.Helper()
	dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(address)}}
	connection, err := dialer.Dial("tcp", net.JoinHostPort(server, "1179"))
	if err != nil {
		t.Fatal(err)
	}
	conn := connection.(*net.TCPConn)
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	id := byte(1)
	if ipv6 {
		id = 2
	}
	body := []byte{4}
	body = binary.BigEndian.AppendUint16(body, 64501)
	body = binary.BigEndian.AppendUint16(body, 90)
	body = append(body, 192, 0, 2, id)
	if ipv6 {
		body = append(body, 8, 2, 6, 1, 4, 0, 2, 0, 1)
	} else {
		body = append(body, 0)
	}
	if _, err := conn.Write(collectorQuaggaFrame(1, body...)); err != nil {
		t.Fatal(err)
	}
	remote, err := readCollectorQuaggaFrame(conn, 1)
	if err != nil || len(remote) < 10 || remote[0] != 4 || binary.BigEndian.Uint16(remote[1:3]) != 64500 || !bytes.Equal(remote[5:9], []byte{192, 0, 2, 112}) {
		t.Fatal("fixture did not negotiate the actual Quagga OPEN identity", err)
	}
	if _, err := conn.Write(collectorQuaggaFrame(4)); err != nil {
		t.Fatal(err)
	}
	if _, err := readCollectorQuaggaFrame(conn, 4); err != nil {
		t.Fatal(err)
	}
	return conn
}

func startNativeCollectorQuagga(t *testing.T, dir string) {
	t.Helper()
	config, err := routing.RenderQuagga(routing.Router{RouterID: "192.0.2.112", ASN: 64500, LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112"}, []routing.Peer{
		{ID: "source-v4", Address: "192.0.2.1", ASN: 64501, Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"203.0.113.0/24"}},
		{ID: "source-v6", Address: "2001:db8::1", ASN: 64501, Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"2001:db8:1234::/48"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(dir, "bootstrap.conf")
	if err = os.WriteFile(bootstrap, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	w, err := routing.OpenQuaggaWatchdog(ctx, routing.QuaggaOptions{
		StateDir: "/scratch/state", Binary: "/native/bgpd", BinaryChecksum: "9f758d79ce302c188b42d252ebd3b51044f803767db55c048574e8d4b60eaf67",
		BootstrapFile: bootstrap, BootstrapChecksum: routing.Checksum(config), BGPPort: 1179, ExclusiveInstance: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error("Quagga fixture could not stop its own child", err)
		}
	})
	v4 := connectCollectorQuaggaPeer(t, "192.0.2.1", "192.0.2.112", false)
	connectCollectorQuaggaPeer(t, "2001:db8::1", "2001:db8::112", true)
	// One IPv4 UPDATE with IGP origin, AS_PATH 64501, direct next hop and
	// the exact allowed 203.0.113.0/24. IPv6 should have a known zero count.
	attrs := []byte{0x40, 1, 1, 0, 0x40, 2, 4, 2, 1, 0xfb, 0xf5, 0x40, 3, 4, 192, 0, 2, 1}
	body := binary.BigEndian.AppendUint16([]byte{0, 0}, uint16(len(attrs)))
	body = append(append(body, attrs...), 24, 203, 0, 113)
	if _, err := v4.Write(collectorQuaggaFrame(2, body...)); err != nil {
		t.Fatal(err)
	}
	for {
		rows, err := routing.ObserveQuaggaSessions(ctx, routing.QuaggaObservationOptions{Socket: "/scratch/state/bgpd.vty", RouterID: "192.0.2.112", RouterASN: 64500}, []string{"192.0.2.1", "2001:db8::1"})
		if err == nil && rows["192.0.2.1"].PrefixesKnown && rows["192.0.2.1"].ReceivedPrefixes == 1 && rows["2001:db8::1"].PrefixesKnown && rows["2001:db8::1"].ReceivedPrefixes == 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("native fixture never established both families with the expected counters", err, rows)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestQuaggaNativeCollectorOnceWithRealManager(t *testing.T) {
	if os.Getenv("QUAGGA_COLLECTOR_NATIVE") != "1" {
		t.Skip("requires explicit stock Quagga collector loopback fixture")
	}
	netIfaces, err := net.Interfaces()
	if err != nil || len(netIfaces) != 1 || netIfaces[0].Name != "lo" {
		t.Fatal("collector fixture requires an isolated loopback network", err)
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "CapEff:\t0000000000000000\n") || !strings.Contains(string(status), "CapBnd:\t0000000000000000\n") || !strings.Contains(string(status), "NoNewPrivs:\t1\n") {
		t.Fatal("collector fixture requires zero capabilities and no-new-privileges")
	}
	dir := t.TempDir()
	if err := os.Chmod("/scratch/state", 0700); err != nil {
		t.Fatal(err)
	}
	f := openQuaggaCollectorManager(t, dir)
	location := f.resource(t, "locations", map[string]any{"name": "Fixture DC"})
	switchID := f.resource(t, "switches", map[string]any{"name": "Fixture switch", "locationId": location})
	port := f.resource(t, "ports", map[string]any{"name": "fixture-port", "switchId": switchID, "status": "active", "speed": 1000})
	member := f.resource(t, "members", map[string]any{"name": "Fixture member", "asn": 64501, "status": "active"})
	vlan := f.resource(t, "vlans", map[string]any{"name": "Fixture peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	f.resource(t, "connections", map[string]any{"name": "Fixture connection", "memberId": member, "portId": port, "vlanId": vlan, "ipv4": "192.0.2.1", "ipv6": "2001:db8::1", "status": "active"})
	router := map[string]any{"name": "Fixture Quagga", "vlanId": vlan, "platform": "quagga", "routerId": "192.0.2.112", "asn": 64500, "localIpv4": "192.0.2.112", "localIpv6": "2001:db8::112"}
	created := f.request(t, "POST", "/api/resources/routers", router, "")
	appRouterID := collectorField(t, created, "id")
	revision := collectorField(t, created, "_etag")
	peer4 := f.resource(t, "peers", map[string]any{"name": "Fixture IPv4", "routerId": appRouterID, "memberId": member, "asn": 64501, "address": "192.0.2.1", "enabled": true, "maxPrefixes": 20, "allowedPrefixes": []string{"203.0.113.0/24"}})
	peer6 := f.resource(t, "peers", map[string]any{"name": "Fixture IPv6", "routerId": appRouterID, "memberId": member, "asn": 64501, "address": "2001:db8::1", "enabled": true, "maxPrefixes": 20, "allowedPrefixes": []string{"2001:db8:1234::/48"}})
	token := collectorField(t, f.request(t, "POST", "/api/account/tokens", map[string]any{"name": "Fixture collector", "currentPassword": quaggaCollectorPassword, "scopes": []string{"monitoring:write"}, "routerIds": []string{appRouterID}}, ""), "token")
	config, err := json.Marshal(telemetry.CollectorConfig{
		Endpoint: f.server.URL + "/api/monitoring/ingest", RouterID: appRouterID, Platform: "quagga",
		QuaggaSocket: "/scratch/state/bgpd.vty", Peers: map[string]string{peer4: "192.0.2.1", peer6: "2001:db8::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"config": filepath.Join(dir, "collector.json"), "token": filepath.Join(dir, "collector.token"), "ca": filepath.Join(dir, "manager-test-ca.pem")}
	for name, data := range map[string][]byte{"config": config, "token": []byte(token), "ca": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw})} {
		if err := os.WriteFile(paths[name], data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	caDir := filepath.Join(dir, "trusted-ca-dir")
	if err := os.Mkdir(caDir, 0700); err != nil {
		t.Fatal(err)
	}
	startNativeCollectorQuagga(t, dir)
	runCollector := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		collector := exec.CommandContext(ctx, "/cli/ixp-collector", "--once", "--config", paths["config"], "--token-file", paths["token"])
		collector.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "SSL_CERT_FILE=" + paths["ca"], "SSL_CERT_DIR=" + caDir, "GOMAXPROCS=2"}
		return collector.CombinedOutput()
	}
	out, err := runCollector()
	if err != nil || f.bindings.Load() != 1 || f.optedIn.Load() != 1 || f.ingest.Load() != 1 || !bytes.Contains(out, []byte("Uploaded 0 port counters and 2 session observations")) {
		t.Fatal("actual collector did not complete a singular HTTPS binding and ingestion", err)
	}
	summary := func() []any {
		rows, ok := f.request(t, "GET", "/api/monitoring/summary", nil, "")["sessions"].([]any)
		if !ok || len(rows) != 2 {
			t.Fatal("expected two monitor session rows")
		}
		return rows
	}
	want := map[string]float64{peer4: 1, peer6: 0}
	for _, row := range summary() {
		r := row.(map[string]any)
		count, exists := want[collectorField(t, r, "peerId")]
		if !exists || r["state"] != "Established" || r["status"] != "fresh" || r["identityVerified"] != true || r["receivedPrefixes"] != count {
			t.Fatal("actual monitor session missing accepted/known-zero observation", r)
		}
	}
	router["routerId"] = "192.0.2.113"
	f.request(t, "PUT", "/api/resources/routers/"+appRouterID, router, revision)
	out, err = runCollector()
	if err == nil || f.bindings.Load() != 2 || f.optedIn.Load() != 2 || f.ingest.Load() != 1 || !bytes.Contains(out, []byte("router identifier or local ASN does not match")) {
		t.Fatal("collector appeared to upload after fetching a changed router identity", err)
	}
	for _, row := range summary() {
		r := row.(map[string]any)
		if r["state"] != "unknown" || r["identityVerified"] != false || r["receivedPrefixes"] != nil {
			t.Fatal("pre-change observation was still treated as current", r)
		}
	}
	t.Log("quagga_collector_native=true actual_app_https=true scoped_binding_and_ingest=true actual_two_family_monitor_rows=true observed_positive_and_known_zero=true changed_local_identity_no_upload=true stale_display_removed=true")
}
