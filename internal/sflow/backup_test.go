package sflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReadOnlyBackupHooksDoNotCreateOrRewrite(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	files, err := ListFiles(missing)
	if err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
	if err = ValidateFiles(missing); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only hooks created missing directory")
	}
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Ingest(testAttributed("backup-durable")); err != nil {
		t.Fatal(err)
	}
	crashStore(s)
	beforeSnapshot, _ := os.ReadFile(filepath.Join(dir, "flows.snapshot.json"))
	beforeWAL, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	beforeInfo, _ := os.Stat(filepath.Join(dir, "flows.wal"))
	beforeEntries, _ := os.ReadDir(dir)
	files, err = ListFiles(dir)
	if err != nil || !reflect.DeepEqual(files, []string{"flows.snapshot.json", "flows.wal"}) {
		t.Fatal(files, err)
	}
	if err = ValidateFiles(dir); err != nil {
		t.Fatal(err)
	}
	afterSnapshot, _ := os.ReadFile(filepath.Join(dir, "flows.snapshot.json"))
	afterWAL, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	afterInfo, _ := os.Stat(filepath.Join(dir, "flows.wal"))
	afterEntries, _ := os.ReadDir(dir)
	if !bytes.Equal(beforeSnapshot, afterSnapshot) || !bytes.Equal(beforeWAL, afterWAL) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) || len(beforeEntries) != len(afterEntries) {
		t.Fatal("backup validation mutated source bytes or files")
	}
	f, err := os.OpenFile(filepath.Join(dir, "flows.wal"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"sequence":`)
	f.Close()
	torn, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	if err = ValidateFiles(dir); !errors.Is(err, ErrTornWALTail) {
		t.Fatal("torn tail not explicitly reported", err)
	}
	afterWAL, _ = os.ReadFile(filepath.Join(dir, "flows.wal"))
	if !bytes.Equal(torn, afterWAL) {
		t.Fatal("read-only validation truncated source tail")
	}
}

func TestReadOnlyBackupRejectsCorruptionAndUnsafePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Ingest(testAttributed("snapshot"))
	s.Close()
	path := filepath.Join(dir, "flows.snapshot.json")
	original, _ := os.ReadFile(path)
	var disk flowDiskSnapshot
	json.Unmarshal(original, &disk)
	for key, row := range disk.State.Rows {
		row.SourceMemberRevision = "invalid"
		disk.State.Rows[key] = row
		break
	}
	disk.Checksum = digest(disk.State)
	changed, _ := json.Marshal(disk)
	os.WriteFile(path, changed, 0600)
	if ValidateFiles(dir) == nil {
		t.Fatal("invalid row ACL metadata accepted despite valid file checksum")
	}
	os.WriteFile(path, original, 0600)
	if err = ValidateFiles(dir); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "source-link")
	os.Symlink(dir, alias)
	if _, err = ListFiles(alias); err == nil {
		t.Fatal("directory symlink accepted")
	}
	os.Rename(path, path+".original")
	os.Symlink(path+".original", path)
	if _, err = ListFiles(dir); err == nil {
		t.Fatal("snapshot symlink or unexpected file accepted")
	}
	os.Remove(path)
	os.Rename(path+".original", path)
	os.WriteFile(filepath.Join(dir, "unreferenced.json"), []byte(`{}`), 0600)
	if _, err = ListFiles(dir); err == nil {
		t.Fatal("unknown persistent data silently omitted")
	}
}

func TestReadOnlyBackupDetectsSnapshotCursorWALContradiction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Ingest(testAttributed("cursor-evidence"))
	wal, _ := os.ReadFile(filepath.Join(dir, "flows.wal"))
	s.Close()
	os.WriteFile(filepath.Join(dir, "flows.wal"), wal, 0600)
	if err = ValidateFiles(dir); err != nil {
		t.Fatal("snapshot-before-truncate recovery rejected", err)
	}
	path := filepath.Join(dir, "flows.snapshot.json")
	raw, _ := os.ReadFile(path)
	var disk flowDiskSnapshot
	json.Unmarshal(raw, &disk)
	for id, c := range disk.State.Cursors {
		last := *c.LastDatagram
		last.Sequence++
		c.LastDatagram = &last
		disk.State.Cursors[id] = c
	}
	disk.Checksum = digest(disk.State)
	raw, _ = json.Marshal(disk)
	os.WriteFile(path, raw, 0600)
	if ValidateFiles(dir) == nil {
		t.Fatal("snapshot accepted contradictory committed WAL cursor")
	}
}

func TestLastTCP179TimeOnlyAdvancesWithActivity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sflow")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := testAttributed("tcp-activity")
	first.ReceivedAt = time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Second)
	if first.ReceivedAt.After(time.Now().Add(30 * time.Second)) {
		t.Skip("fixture boundary")
	}
	if err = s.Ingest(first); err != nil {
		t.Fatal(err)
	}
	ordinary := testAttributed("ordinary-packet")
	ordinary.ReceivedAt = first.ReceivedAt.Add(time.Second)
	ordinary.Flows[0].SampledTCP179Activity = false
	if err = s.Ingest(ordinary); err != nil {
		t.Fatal(err)
	}
	q := queryNow("member-a")
	q.To = time.Now().UTC().Add(30 * time.Second)
	r, err := s.Query(q)
	if err != nil || len(r.Items) != 1 {
		t.Fatal(r, err)
	}
	row := r.Items[0]
	if row.LastTCP179At == nil || !row.LastTCP179At.Equal(first.ReceivedAt) || !row.LastReceivedAt.Equal(ordinary.ReceivedAt) || row.TCP179Samples != "1" {
		t.Fatal("ordinary sample fabricated last TCP activity", row)
	}
	*row.LastTCP179At = first.ReceivedAt.Add(time.Hour)
	r, _ = s.Query(q)
	if !r.Items[0].LastTCP179At.Equal(first.ReceivedAt) {
		t.Fatal("query returned mutable stored TCP activity pointer")
	}
	newer := testAttributed("tcp-activity-newer")
	newer.ReceivedAt = ordinary.ReceivedAt.Add(time.Second)
	if err = s.Ingest(newer); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Query(q)
	if !r.Items[0].LastTCP179At.Equal(newer.ReceivedAt) || r.Items[0].TCP179Samples != "2" {
		t.Fatal("actual activity failed to advance timestamp")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _ = s.Query(q)
	if r.Items[0].LastTCP179At == nil || !r.Items[0].LastTCP179At.Equal(newer.ReceivedAt) {
		t.Fatal("TCP activity time lost on restore")
	}
}
