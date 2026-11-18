package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/sflow"
	"github.com/moedove/ixp-manager/internal/telemetry"
	"github.com/moedove/ixp-manager/internal/traffic"
)

type trafficBackupFixture struct {
	dir                                                                                                                    string
	at                                                                                                                     time.Time
	memberA, memberB, revisionA, revisionB, portA, portB, switchID, locationID, vlanID, connectionA, connectionB, routerID string
	flows                                                                                                                  []sflow.AttributedBatch
}

func trafficBackupRevision(t *testing.T, c *provisionBackupClient, id string) string {
	t.Helper()
	for _, raw := range c.call("GET", "/api/traffic/catalog", nil)["targets"].([]any) {
		row := raw.(map[string]any)
		if row["scope"] == "member" && row["id"] == id {
			revision, _ := row["memberRevision"].(string)
			if len(revision) == 64 {
				return revision
			}
		}
	}
	t.Fatalf("real API omitted member generation for %s", id)
	return ""
}
func makeTrafficBackupFixture(t *testing.T) trafficBackupFixture {
	t.Helper()
	f := trafficBackupFixture{dir: t.TempDir(), at: time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)}
	a, e := app.New(app.Config{DataDir: f.dir, SetupToken: "traffic-backup-setup"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Traffic backup admin", "email": "traffic-backup@example.net", "password": provisionBackupPassword, "ixpName": "Traffic backup fixture", "asn": 64500, "setupToken": "traffic-backup-setup"})
	loc := c.call("POST", "/api/resources/locations", app.Record{"name": "Traffic backup DC"})
	f.locationID = loc["id"].(string)
	sw := c.call("POST", "/api/resources/switches", app.Record{"name": "Traffic backup switch", "locationId": f.locationID})
	f.switchID = sw["id"].(string)
	aMember := c.call("POST", "/api/resources/members", app.Record{"name": "Traffic member A", "asn": 64501, "status": "active"})
	f.memberA = aMember["id"].(string)
	bMember := c.call("POST", "/api/resources/members", app.Record{"name": "Traffic member B", "asn": 64502, "status": "active"})
	f.memberB = bMember["id"].(string)
	vlan := c.call("POST", "/api/resources/vlans", app.Record{"name": "Traffic backup LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	f.vlanID = vlan["id"].(string)
	pA := c.call("POST", "/api/resources/ports", app.Record{"name": "Ethernet1", "switchId": f.switchID, "speed": 1000, "status": "active"})
	f.portA = pA["id"].(string)
	pB := c.call("POST", "/api/resources/ports", app.Record{"name": "Ethernet2", "switchId": f.switchID, "speed": 1000, "status": "active"})
	f.portB = pB["id"].(string)
	ca := c.call("POST", "/api/resources/connections", app.Record{"name": "Traffic A", "memberId": f.memberA, "portId": f.portA, "vlanId": f.vlanID, "ipv4": "192.0.2.10", "status": "active"})
	f.connectionA = ca["id"].(string)
	cb := c.call("POST", "/api/resources/connections", app.Record{"name": "Traffic B", "memberId": f.memberB, "portId": f.portB, "vlanId": f.vlanID, "ipv4": "192.0.2.11", "status": "active"})
	f.connectionB = cb["id"].(string)
	router := c.call("POST", "/api/resources/routers", app.Record{"name": "Traffic router", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": f.vlanID})
	f.routerID = router["id"].(string)
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	// The fixture observes a synthetic recent interval. Backdate the actual
	// inventory generation before asking the real API for its instance-key HMAC.
	state, e := app.OpenStore(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	e = state.Update(func(s *app.State) error {
		stamp := f.at.Add(-time.Hour).Format(time.RFC3339Nano)
		for _, m := range s.Resources["members"] {
			m["createdAt"] = stamp
		}
		for _, p := range s.Resources["ports"] {
			p["ownershipSince"] = stamp
		}
		for _, connection := range s.Resources["connections"] {
			connection["ownershipSince"] = stamp
			connection["bindingSince"] = stamp
		}
		return nil
	})
	state.Close()
	if e != nil {
		t.Fatal(e)
	}
	a, e = app.New(app.Config{DataDir: f.dir})
	if e != nil {
		t.Fatal(e)
	}
	c = &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "traffic-backup@example.net", "password": provisionBackupPassword})
	f.revisionA = trafficBackupRevision(t, c, f.memberA)
	f.revisionB = trafficBackupRevision(t, c, f.memberB)
	if f.revisionA == f.revisionB {
		t.Fatal("member generations collided")
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	observations, e := telemetry.Open(filepath.Join(f.dir, "telemetry"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = observations.Close() })
	sample := func(port, member, revision, connection, epoch, value, events string) telemetry.PortSample {
		return telemetry.PortSample{PortID: port, MemberID: member, OwnershipRevision: "binding-" + port, CounterEpoch: epoch, CounterBits: 64, LinkSpeedMbps: 1000, InOctets: value, OutOctets: "0", OperStatus: "up", Errors: &traffic.CounterPair{In: events, Out: "0", Bits: 32}, Dimensions: traffic.Dimensions{MemberRevision: revision, SwitchID: f.switchID, LocationID: f.locationID, ConnectionIDs: []string{connection}, OwnershipSince: f.at.Add(-time.Hour)}}
	}
	aPoint := func(epoch, value, events string) telemetry.PortSample {
		return sample(f.portA, f.memberA, f.revisionA, f.connectionA, epoch, value, events)
	}
	batches := []telemetry.Batch{
		{RouterID: f.routerID, ObservedAt: f.at, Ports: []telemetry.PortSample{aPoint("epoch-a", "0", "100"), sample(f.portB, f.memberB, f.revisionB, f.connectionB, "epoch-b", "10", "5")}},
		{RouterID: f.routerID, ObservedAt: f.at.Add(time.Minute), Ports: []telemetry.PortSample{aPoint("epoch-a", "9007199254740993", "123"), sample(f.portB, f.memberB, f.revisionB, f.connectionB, "epoch-b", "30", "9")}},
		{RouterID: f.routerID, ObservedAt: f.at.Add(61 * time.Second), Ports: []telemetry.PortSample{aPoint("epoch-restarted", "9007199254741993", "200")}},
		{RouterID: f.routerID, ObservedAt: f.at.Add(121 * time.Second), Ports: []telemetry.PortSample{aPoint("epoch-restarted", "9007199254742003", "201")}},
	}
	// The public writer checkpoints before event129. Real unchanged samples
	// trigger it without reaching into telemetry's private compaction internals.
	for i := 0; i < 125; i++ {
		batches = append(batches, telemetry.Batch{RouterID: f.routerID, ObservedAt: f.at.Add(time.Duration(122+i) * time.Second), Ports: []telemetry.PortSample{aPoint("epoch-restarted", "9007199254742003", "201")}})
	}
	for _, batch := range batches {
		if e = observations.Ingest(batch, batch.ObservedAt); e != nil {
			t.Fatal(e)
		}
	}
	if e = observations.Close(); e != nil {
		t.Fatal(e)
	}
	files, e := telemetry.ListFiles(filepath.Join(f.dir, "telemetry"))
	if e != nil {
		t.Fatal(e)
	}
	for _, tier := range []string{"5m", "1h", "1d"} {
		found := false
		for _, name := range files {
			found = found || strings.HasPrefix(name, "archive/"+tier+"-")
		}
		if !found {
			t.Fatalf("fixture lacks a real immutable %s partition: %v", tier, files)
		}
	}
	flowStore, e := sflow.Open(filepath.Join(f.dir, "sflow"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = flowStore.Close() })
	received := time.Now().UTC().Add(-time.Second)
	for i := 0; i < 2; i++ {
		id := []string{"traffic-flow-one", "traffic-flow-two"}[i]
		sum := sha256.Sum256([]byte(id))
		batch := sflow.AttributedBatch{ID: id, ReceivedAt: received.Add(time.Duration(i) * time.Millisecond), ExporterID: "backup-exporter", SwitchID: f.switchID, MappingRevision: strings.Repeat([]string{"a", "b"}[i], 22), Sequence: uint32(100 + i), UptimeMS: uint32(100000 + i*100), DatagramDigest: hex.EncodeToString(sum[:]), Flows: []sflow.AttributedFlow{{VLANID: f.vlanID, Family: 4, SourceMemberID: f.memberA, DestinationMemberID: f.memberB, SourceMemberRevision: f.revisionA, DestinationMemberRevision: f.revisionB, SourceConnectionID: f.connectionA, DestinationConnectionID: f.connectionB, EstimatedBytes: "18446744073709551615", EstimatedPackets: "400", SampleCount: 1, SampledTCP179Activity: true}}}
		classification, e := flowStore.IngestWithResult(batch)
		if e != nil || !classification.AcceptFlows {
			t.Fatalf("real sFlow ingestion: %+v %v", classification, e)
		}
		f.flows = append(f.flows, batch)
	}
	if e = flowStore.Close(); e != nil {
		t.Fatal(e)
	}
	if e = sflow.ValidateFiles(filepath.Join(f.dir, "sflow")); e != nil {
		t.Fatal(e)
	}
	return f
}

func TestTrafficBackupRestoresArchivesLowerBoundsMemberGenerationsAndFlowCursor(t *testing.T) {
	f := makeTrafficBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "traffic.tar.gz")
	if e := Create(f.dir, archive); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if e := Restore(archive, target); e != nil {
		t.Fatal(e)
	}
	a, e := app.New(app.Config{DataDir: target})
	if e != nil {
		t.Fatal(e)
	}
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "traffic-backup@example.net", "password": provisionBackupPassword})
	if trafficBackupRevision(t, c, f.memberA) != f.revisionA || trafficBackupRevision(t, c, f.memberB) != f.revisionB {
		t.Fatal("full restore changed instance-key member attribution")
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	observations, e := telemetry.Open(filepath.Join(target, "telemetry"))
	if e != nil {
		t.Fatal(e)
	}
	defer observations.Close()
	for _, resolution := range []string{traffic.FiveMinutes, traffic.Hour, traffic.Day} {
		step := traffic.Duration(resolution)
		q := traffic.Query{Scope: "member", ID: f.memberA, MemberID: f.memberA, MemberRevision: f.revisionA, Metric: traffic.Octets, Resolution: resolution, From: f.at.Truncate(step), To: f.at.Truncate(step).Add(step), ExpectedPortIDs: []string{f.portA}}
		r, e := observations.QueryTraffic(q, time.Now())
		if e != nil || r.Summary.In.Total == nil || *r.Summary.In.Total != "9007199254741003" {
			t.Fatalf("%s exact integer/epoch boundary lost: %+v %v", resolution, r.Summary.In, e)
		}
		q.Metric = traffic.Errors
		r, e = observations.QueryTraffic(q, time.Now())
		if e != nil || r.Summary.In.Total != nil || r.Summary.In.LowerBoundTotal == nil || *r.Summary.In.LowerBoundTotal != "24" || r.Summary.In.AverageRate != nil {
			t.Fatalf("%s lower bound became exact or lost: %+v %v", resolution, r.Summary.In, e)
		}
		q.Metric = traffic.Octets
		q.ID = f.memberB
		q.MemberID = f.memberB
		q.MemberRevision = f.revisionB
		q.ExpectedPortIDs = []string{f.portB}
		r, e = observations.QueryTraffic(q, time.Now())
		if e != nil || r.Summary.In.Total == nil || *r.Summary.In.Total != "20" {
			t.Fatalf("other member's exact counters changed: %+v %v", r.Summary.In, e)
		}
		q.ID = f.memberA
		q.MemberID = f.memberA
		q.MemberRevision = f.revisionB
		q.ExpectedPortIDs = []string{f.portA}
		r, e = observations.QueryTraffic(q, time.Now())
		if e != nil || r.Summary.In.Total != nil || r.Summary.In.LowerBoundTotal != nil {
			t.Fatal("reused/mismatched member generation received archived counters")
		}
	}
	raw := observations.Snapshot(time.Now()).Ports[f.portA]
	if raw.CounterEpoch != "epoch-restarted" || raw.OwnershipRevision != "binding-"+f.portA || raw.Dimensions.MemberRevision != f.revisionA {
		t.Fatal("raw epoch or immutable owner identity changed")
	}
	flows, e := sflow.Open(filepath.Join(target, "sflow"))
	if e != nil {
		t.Fatal(e)
	}
	defer flows.Close()
	query := sflow.Query{From: f.flows[0].ReceivedAt.Add(-time.Minute), To: time.Now().UTC().Add(time.Second), Resolution: "5m", MemberID: f.memberA, MemberRevision: f.revisionA, SwitchIDs: []string{f.switchID}, Limit: 100}
	assertFlows := func() {
		t.Helper()
		r, e := flows.Query(query)
		if e != nil || r.SourceKind != "sampled" || len(r.Items) != 2 {
			t.Fatalf("mapping epochs merged or sampled rows lost: %+v %v", r, e)
		}
		total := new(big.Int)
		for _, row := range r.Items {
			n, ok := new(big.Int).SetString(row.EstimatedBytes, 10)
			if !ok {
				t.Fatal("sample estimate stopped being decimal")
			}
			total.Add(total, n)
			if row.SampleCount != "1" || row.SourceMemberRevision != f.revisionA || row.DestinationMemberRevision != f.revisionB {
				t.Fatal("sample/attribution precision lost")
			}
		}
		if total.String() != "36893488147419103230" {
			t.Fatal("sampled estimates rounded or replayed", total)
		}
	}
	assertFlows()
	status, e := flows.Status()
	if e != nil || len(status) != 1 || status[0].Sequence != 101 || status[0].UptimeMS != 100100 || status[0].PendingRestart {
		t.Fatalf("cursor not restored: %+v %v", status, e)
	}
	replay := f.flows[1]
	replay.ID = "traffic-new-http-replay"
	replay.ReceivedAt = time.Now().UTC()
	classification, e := flows.IngestWithResult(replay)
	if e != nil || classification.AcceptFlows || classification.SequenceState != "duplicate" {
		t.Fatalf("restored cursor permitted a fresh-ID replay: %+v %v", classification, e)
	}
	assertFlows()
	query.MemberRevision = f.revisionB
	r, e := flows.Query(query)
	if e != nil || len(r.Items) != 0 {
		t.Fatal("one side's member ID matched the other side's generation")
	}
	query.MemberID = f.memberB
	r, e = flows.Query(query)
	if e != nil || len(r.Items) != 2 {
		t.Fatal("destination member lost its correctly attributed sample history")
	}
	t.Run("unreferenced-valid-partition", func(t *testing.T) {
		bad := rewriteTrafficBackup(t, archive, false)
		destination := filepath.Join(t.TempDir(), "bad-restore")
		if e := Restore(bad, destination); e == nil || !strings.Contains(e.Error(), "unreferenced observation artifact") {
			t.Fatalf("valid-manifest orphan not rejected as unreferenced: %v", e)
		}
		if _, e := os.Stat(destination); !os.IsNotExist(e) {
			t.Fatal("invalid restore published target")
		}
	})
	t.Run("tampered-partition-valid-outer-manifest", func(t *testing.T) {
		bad := rewriteTrafficBackup(t, archive, true)
		destination := filepath.Join(t.TempDir(), "bad-restore")
		if e := Restore(bad, destination); e == nil {
			t.Fatal("outer manifest concealed damaged referenced archive")
		}
		if _, e := os.Stat(destination); !os.IsNotExist(e) {
			t.Fatal("damaged restore published target")
		}
	})
}

// Rewrite only the backup envelope, recalculating every outer manifest entry.
// The immutable inner checkpoint still determines which bytes are authorized.
func rewriteTrafficBackup(t *testing.T, source string, tamper bool) string {
	t.Helper()
	f, e := os.Open(source)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	type file struct {
		name string
		data []byte
	}
	files := []file{}
	selected := -1
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		if header.Name == "manifest.json" {
			continue
		}
		files = append(files, file{header.Name, data})
		if selected < 0 && strings.HasPrefix(header.Name, "telemetry/archive/5m-") {
			selected = len(files) - 1
		}
	}
	if selected < 0 {
		t.Fatal("fixture missing immutable fine partition")
	}
	if tamper {
		files[selected].data = append([]byte(nil), files[selected].data...)
		files[selected].data[len(files[selected].data)/2] ^= 0xff
	} else {
		payload := append([]byte(nil), files[selected].data...)
		sum := sha256.Sum256(payload)
		files = append(files, file{"telemetry/archive/5m-2000-01-01-00-" + hex.EncodeToString(sum[:]) + ".json.gz", payload})
	}
	output := filepath.Join(t.TempDir(), "rewritten.tar.gz")
	out, e := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	writer := gzip.NewWriter(out)
	tw := tar.NewWriter(writer)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC()}
	for _, entry := range files {
		if e = tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(entry.data))}); e != nil {
			t.Fatal(e)
		}
		if _, e = tw.Write(entry.data); e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(entry.data)
		manifest.Files = append(manifest.Files, Entry{Name: entry.name, Size: int64(len(entry.data)), SHA256: hex.EncodeToString(sum[:])})
	}
	raw, e := json.Marshal(manifest)
	if e != nil {
		t.Fatal(e)
	}
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(tw, bytes.NewReader(raw)); e != nil {
		t.Fatal(e)
	}
	for _, close := range []func() error{tw.Close, writer.Close, out.Close} {
		if e = close(); e != nil {
			t.Fatal(e)
		}
	}
	return output
}
