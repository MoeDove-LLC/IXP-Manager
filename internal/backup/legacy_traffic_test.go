package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/sflow"
	"github.com/moedove/ixp-manager/internal/traffic"
)

func TestLegacyTrafficAndApprovedReceiverBackupRoundTrip(t *testing.T) {
	f := makeTrafficBackupFixture(t)
	state, err := app.OpenStore(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Update(func(s *app.State) error {
		for _, p := range s.Resources["ports"] {
			if p["id"] == f.portA {
				p["snmpIfIndex"], p["snmpIfType"] = 1, 6
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{DataDir: f.dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "traffic-backup@example.net", "password": provisionBackupPassword})
	c.call("POST", "/api/resources/mac-addresses", app.Record{"name": "Approved source MAC", "connectionId": f.connectionA, "address": "02:00:00:00:00:01"})
	c.call("POST", "/api/resources/sflow-exporters", app.Record{"name": "Backup exporter", "switchId": f.switchID, "sourceAddress": "192.0.2.250", "agentAddress": "192.0.2.250", "subAgentId": 0, "vlanIds": []string{f.vlanID}, "enabled": true})
	r := c.call("POST", "/api/sflow/receivers", app.Record{"name": "Reviewed backup receiver", "spec": sflow.Receiver{MemberID: f.memberA, ConnectionIDs: []string{f.connectionA}, VLANIDs: []string{f.vlanID}, Address: "198.51.100.25", Port: 6343, Enabled: true}})
	path := "/api/sflow/receivers/" + r["id"].(string)
	review := c.call("POST", path+"/review", app.Record{"expectedRevision": r["revision"]})
	approved := c.call("POST", path+"/approve", app.Record{"expectedRevision": r["revision"], "reviewChecksum": review["reviewChecksum"], "acknowledgeHeaderData": true, "currentPassword": provisionBackupPassword})
	plan := c.request("GET", path+"/plan", nil)
	if plan.Code != 200 {
		t.Fatal(plan.Body.String())
	}
	input := traffic.LegacyInput{Format: traffic.LegacyTableJSON, Source: traffic.LegacySource{Name: "Synthetic historical report", Timezone: "UTC", Backend: "mrtg", Table: "traffic_daily"}, Content: `[{"id":101,"cust_id":42,"day":"2024-06-01","category":"bits","day_tot_in":"9007199254740993","day_tot_out":null}]`}
	preview := c.call("POST", "/api/traffic/legacy/preview", app.Record{"input": input})
	meta := c.call("POST", "/api/traffic/legacy/import", app.Record{"input": input, "expectedChecksum": preview["checksum"], "acknowledgeWindows": true, "currentPassword": provisionBackupPassword})
	queryPath := "/api/traffic/legacy/query?batchId=" + meta["id"].(string)
	before := c.call("GET", queryPath, nil)
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "legacy-and-receiver.tar.gz")
	if err = Create(f.dir, archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(archive, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := app.New(app.Config{DataDir: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	c = &provisionBackupClient{t: t, a: restored}
	c.call("POST", "/api/login", app.Record{"email": "traffic-backup@example.net", "password": provisionBackupPassword})
	after := c.call("GET", queryPath, nil)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("legacy precision, unknowns or original windows changed during restore")
	}
	receiver := c.call("GET", path, nil)
	if receiver["status"] != "approved" || receiver["revision"] != approved["revision"] || receiver["deliveryState"] != "unknown" {
		t.Fatal("approval lost or volatile delivery evidence invented", receiver)
	}
	newPlan := c.request("GET", path+"/plan", nil)
	if newPlan.Code != 200 || newPlan.Header().Get("X-SFlow-Receiver-Revision") != plan.Header().Get("X-SFlow-Receiver-Revision") {
		t.Fatal("restored receiver cannot produce its reviewed plan", newPlan.Body.String())
	}
	files, err := traffic.LegacyListFiles(filepath.Join(f.dir, "legacy-traffic"))
	if err != nil || len(files) != 2 {
		t.Fatal("immutable legacy references absent", files, err)
	}
	for _, name := range files {
		original, err := os.ReadFile(filepath.Join(f.dir, "legacy-traffic", name))
		if err != nil {
			t.Fatal(err)
		}
		copy, err := os.ReadFile(filepath.Join(destination, "legacy-traffic", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, copy) {
			t.Fatal("immutable legacy artifact changed", name)
		}
	}
	// Source preflight must reject corruption without repairing the source.
	batchPath := filepath.Join(f.dir, "legacy-traffic", "batches", meta["id"].(string)+".json.gz")
	broken, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatal(err)
	}
	broken[len(broken)/2] ^= 0xff
	if err = os.WriteFile(batchPath, broken, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Create(f.dir, filepath.Join(t.TempDir(), "invalid.tar.gz")); err == nil {
		t.Fatal("corrupt legacy batch backed up")
	}
	unchanged, err := os.ReadFile(batchPath)
	if err != nil || !bytes.Equal(broken, unchanged) {
		t.Fatal("backup preflight modified source")
	}
}

func TestLegacyBackupRejectsUnreferencedBatchWithValidOuterManifest(t *testing.T) {
	dir := seed(t)
	path := filepath.Join(t.TempDir(), "orphan.tar.gz")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	manifest := Manifest{Version: 1, CreatedAt: time.Now().UTC()}
	for _, name := range []string{"state.json", "security.key", "legacy-traffic/batches/" + strings.Repeat("a", 64) + ".json.gz"} {
		data := []byte("unreferenced legacy file")
		if !strings.HasPrefix(name, "legacy-traffic/") {
			data, err = os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(data); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, Entry{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, close := range []func() error{tw.Close, gz.Close, file.Close} {
		if err = close(); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err = Restore(path, destination); err == nil || !strings.Contains(err.Error(), "unreferenced observation artifact") {
		t.Fatal("orphan not rejected", err)
	}
	if _, err = os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("failed restore published destination")
	}
}
