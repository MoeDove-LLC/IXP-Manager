package traffic

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyNativeRRDReadUsesCopyAndClearsDaemonEnvironment(t *testing.T) {
	binary := os.Getenv("RRDTOOL_BIN")
	if binary == "" {
		t.Skip("set RRDTOOL_BIN to verify real offline RRDtool")
	}
	library := os.Getenv("RRDTOOL_LIBRARY_DIRECTORY")
	env, e := legacyRRDEnvironment(library)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "snapshot.rrd")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		if raw, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("synthetic RRDtool fixture failed: %v %s", e, raw)
		}
	}
	run("create", source, "--start", "1704066900", "--step", "300", "DS:in:GAUGE:600:U:U", "DS:out:GAUGE:600:U:U", "RRA:AVERAGE:0.5:1:24", "RRA:MAX:0.5:1:24")
	run("update", source, "1704067200:10:20", "1704067500:30:U", "1704067800:50:60", "1704068100:70:80")
	before, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("RRDCACHED_ADDRESS", "unix:"+filepath.Join(dir, "must-not-contact.sock"))
	options := RRDReadOptions{Binary: binary, LibraryDirectory: library, Root: dir, File: "snapshot.rrd", InDS: "in", OutDS: "out", From: time.Unix(1704067200, 0), To: time.Unix(1704068100, 0), StepSeconds: 300, Source: LegacySource{Name: "synthetic native fixture", Timezone: "UTC", Backend: "rrd", Unit: "bytes", EntityKind: "dataset", EntityID: "local-fixture"}}
	input, e := ExportLegacyRRD(context.Background(), options)
	if e != nil {
		t.Fatal(e)
	}
	batch, e := ParseLegacy(input)
	if e != nil || len(batch.Rows) != 3 || len(batch.Columns) != 4 {
		t.Fatalf("native typed output: %+v %v", batch, e)
	}
	for _, row := range batch.Rows {
		if row.In.Total != nil || row.Out.Total != nil || row.Window.BoundaryQuality != "export" || row.RateUnit != "bytes/s" {
			t.Fatal("native rate was relabeled a counter total")
		}
	}
	after, e := os.ReadFile(source)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("native read changed original RRD")
	}
	options.InDS = "in:CDEF:escape"
	if _, e = ExportLegacyRRD(context.Background(), options); e == nil {
		t.Fatal("native data-source injection accepted")
	}
	options.InDS = "in"
	options.File = "../snapshot.rrd"
	if _, e = ExportLegacyRRD(context.Background(), options); e == nil {
		t.Fatal("native source escaped trusted root")
	}
	options.File = "link.rrd"
	if e = os.Symlink(source, filepath.Join(dir, "link.rrd")); e != nil {
		t.Fatal(e)
	}
	if _, e = ExportLegacyRRD(context.Background(), options); e == nil {
		t.Fatal("native input symlink accepted")
	}
	options.File = "snapshot.rrd"
	options.Source.Unit = ""
	if _, e = ExportLegacyRRD(context.Background(), options); e == nil {
		t.Fatal("native rate unit was guessed")
	}
	if strings.Contains(input.Content, "must-not-contact") {
		t.Fatal("daemon environment leaked into source data")
	}
}
