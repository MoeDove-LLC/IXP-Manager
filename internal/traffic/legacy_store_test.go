package traffic

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyStoreImmutableRoundTripRetentionAndGeneration(t *testing.T) {
	dir := t.TempDir()
	batch, e := ParseLegacy(legacyTableInput(t))
	if e != nil {
		t.Fatal(e)
	}
	store, e := OpenLegacy(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Put(batch, strings.Repeat("0", 64)); e == nil {
		t.Fatal("unreviewed checksum accepted")
	}
	meta, e := store.Put(batch, batch.Checksum)
	if e != nil {
		t.Fatal(e)
	}
	again, e := store.Put(batch, batch.Checksum)
	if e != nil || again.ImportedAt != meta.ImportedAt {
		t.Fatal("idempotent import changed immutable receipt")
	}
	if info, e := os.Stat(filepath.Join(dir, "batches", batch.ID+".json.gz")); e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("legacy artifact is not private")
	}
	if second, e := OpenLegacy(dir); e == nil {
		second.Close()
		t.Fatal("concurrent offline writer accepted")
	}
	returned, e := store.Get(batch.ID)
	if e != nil {
		t.Fatal(e)
	}
	returned.Rows[0].Notices[0] = "caller mutation"
	againBatch, e := store.Get(batch.ID)
	if e != nil || againBatch.Rows[0].Notices[0] == "caller mutation" {
		t.Fatal("Get leaked stored aliases")
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	replacement, e := OpenLegacy(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer replacement.Close()
	if _, e = store.Get(batch.ID); e == nil {
		t.Fatal("closed generation read a replacement store")
	}
	if _, e = store.Put(batch, batch.Checksum); e == nil {
		t.Fatal("closed generation wrote a replacement store")
	}
	if e = store.Delete(batch.ID, batch.Checksum); e == nil {
		t.Fatal("closed generation removed replacement state")
	}
	result, e := replacement.Query(LegacyQuery{BatchID: batch.ID, Period: "year"})
	if e != nil || len(result.Rows) != 1 || result.Rows[0].In.Total == nil || *result.Rows[0].In.Total != "36028797018963972" || !result.NoAggregation {
		t.Fatalf("original report lost: %+v %v", result, e)
	}
	files, e := LegacyListFiles(dir)
	if e != nil || len(files) != 2 {
		t.Fatalf("backup reference list %+v %v", files, e)
	}
	if e = replacement.Delete(batch.ID, strings.Repeat("0", 64)); e == nil {
		t.Fatal("changed retention checksum accepted")
	}
	if e = replacement.Delete(batch.ID, batch.Checksum); e != nil {
		t.Fatal(e)
	}
	if _, e = replacement.Get(batch.ID); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("deleted batch resurrected")
	}
	catalog, e := replacement.List()
	if e != nil || len(catalog.Batches) != 0 || catalog.Bytes != 0 {
		t.Fatal("explicit retention did not reclaim quota")
	}
}
func TestLegacyStoreMemberEvidenceFiltersBeforePagination(t *testing.T) {
	input := legacyTableInput(t)
	start := time.Date(2024, 5, 30, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	input.Windows = []LegacyDeclaredWindow{{RecordID: "101", Period: "day", Start: start, End: end, Evidence: "synthetic exact-window receipt"}}
	input.Owners = []LegacyOwnerEvidence{{EntityKind: "customer", EntityID: "42", MemberID: "m", MemberRevision: strings.Repeat("a", 64), ValidFrom: start, ValidTo: end, ReferenceSHA256: strings.Repeat("b", 64), Description: "synthetic historical ownership evidence"}}
	batch, e := ParseLegacy(input)
	if e != nil {
		t.Fatal(e)
	}
	store, e := OpenLegacy(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = store.Put(batch, batch.Checksum); e != nil {
		t.Fatal(e)
	}
	result, e := store.Query(LegacyQuery{BatchID: batch.ID, MemberID: "m", MemberRevision: strings.Repeat("a", 64), Limit: 1})
	if e != nil || result.TotalRows != 1 || len(result.Rows) != 1 || result.Rows[0].Window.Period != "day" {
		t.Fatalf("evidence filter/pagination %+v %v", result, e)
	}
	result, e = store.Query(LegacyQuery{BatchID: batch.ID, MemberID: "m", MemberRevision: strings.Repeat("c", 64)})
	if e != nil || result.TotalRows != 0 {
		t.Fatal("reused member ID received historical reports")
	}
	cut := start.Add(time.Hour)
	result, e = store.Query(LegacyQuery{BatchID: batch.ID, WindowFrom: &cut, WindowTo: &end})
	if e != nil || len(result.Rows) != 1 || *result.Rows[0].In.Total != "9007199254740993" || !result.Rows[0].Window.Start.Equal(start) {
		t.Fatal("known-window query prorated total or guessed unknown windows")
	}
}
func TestLegacyStoreQuotaOrphansCorruptionAndReadonlyValidation(t *testing.T) {
	dir := t.TempDir()
	store, e := OpenLegacy(dir)
	if e != nil {
		t.Fatal(e)
	}
	batch, e := ParseLegacy(legacyTableInput(t))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Put(batch, batch.Checksum); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "batches", batch.ID+".json.gz")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	orphan := filepath.Join(dir, "batches", strings.Repeat("f", 64)+".json.gz")
	if e = os.WriteFile(orphan, original, 0600); e != nil {
		t.Fatal(e)
	}
	if e = LegacyValidateFiles(dir); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(orphan); e != nil {
		t.Fatal("readonly validator cleaned unreferenced data")
	}
	store.Close()
	store, e = OpenLegacy(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(orphan); !os.IsNotExist(e) {
		t.Fatal("restart did not reclaim unreferenced immutable artifact")
	}
	other := legacyTableInput(t)
	other.Source.Name = "second synthetic source"
	another, e := ParseLegacy(other)
	if e != nil {
		t.Fatal(e)
	}
	realManifest := store.manifest
	store.manifest = legacyManifest{Version: 1, Batches: map[string]legacyFile{"quota-fixture": {Metadata: LegacyMetadata{Size: LegacyQuotaBytes}}}}
	if _, e = store.Put(another, another.Checksum); e == nil {
		t.Fatal("published-byte quota ignored")
	}
	store.manifest = realManifest
	store.Close()
	damaged := append([]byte(nil), original...)
	damaged[len(damaged)/2] ^= 0xff
	if e = os.WriteFile(path, damaged, 0600); e != nil {
		t.Fatal(e)
	}
	if e = LegacyValidateFiles(dir); e == nil {
		t.Fatal("corrupt batch accepted by readonly validation")
	}
	if broken, e := OpenLegacy(dir); e == nil {
		broken.Close()
		t.Fatal("corrupt immutable batch opened")
	}
}
func TestLegacyStoreRejectsOwnerForgeryAndSymlinkInputs(t *testing.T) {
	batch, e := ParseLegacy(legacyTableInput(t))
	if e != nil {
		t.Fatal(e)
	}
	row := &batch.Rows[0]
	row.StaffOnly = false
	row.MemberID = "current-owner"
	row.MemberRevision = strings.Repeat("a", 64)
	row.OwnerEvidence = &LegacyOwnerEvidence{EntityKind: row.EntityKind, EntityID: row.EntityID, MemberID: row.MemberID, MemberRevision: row.MemberRevision, ReferenceSHA256: strings.Repeat("b", 64), ValidFrom: time.Now().Add(-time.Hour), ValidTo: time.Now(), Description: "a current owner is not historical evidence"}
	if e = legacySeal(&batch); e != nil {
		t.Fatal(e)
	}
	if e = ValidateLegacyBatch(batch); e == nil {
		t.Fatal("fresh public hash laundered unknown-window/current-owner attribution")
	}
	dir := t.TempDir()
	store, e := OpenLegacy(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = store.Put(batch, batch.Checksum); e == nil {
		t.Fatal("typed store accepted forged authorization")
	}
	manifest, e := os.ReadFile(filepath.Join(dir, legacyManifestName))
	if e != nil {
		t.Fatal(e)
	}
	var state legacyManifest
	if e = json.Unmarshal(manifest, &state); e != nil || len(state.Batches) != 0 {
		t.Fatal("rejected batch modified checkpoint")
	}
	link := filepath.Join(t.TempDir(), "linked-store")
	if e = os.Symlink(dir, link); e != nil {
		t.Fatal(e)
	}
	if _, e = LegacyListFiles(link); e == nil {
		t.Fatal("readonly backup followed store symlink")
	}
}
