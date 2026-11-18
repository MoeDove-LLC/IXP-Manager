package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func noteRaw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func noteArchiveFixture() NoteArchive {
	return NoteArchive{Version: 1, Kind: NoteArchiveKind, Source: "ixp-manager-legacy", Access: "staff", SourceTimezone: "unknown", Notes: []NoteArchiveNote{{ID: NoteArchiveID("source", 1), LegacyID: 1, Visibility: "member", PrivacyKnown: true, Facts: NoteArchiveFacts{"id": noteRaw(1), "customer_id": noteRaw(999), "title": noteRaw("  original title \r\n"), "note": noteRaw("  original body\r\nsecond line  "), "private": noteRaw(false), "created": noteRaw(nil), "updated_at": noteRaw("2020-01-01 12:00:00")}}}, Users: []NoteArchiveUser{{ID: NoteArchiveID("user", 7), LegacyID: 7, Facts: NoteArchiveFacts{"id": noteRaw(7), "email": noteRaw("source@example.test"), "name": noteRaw("Source only")}}}, Preferences: []NoteArchivePreference{{ID: NoteArchiveID("modern-pref", 7), Format: "user.prefs", LegacyID: 7, LegacyUserID: 7, PrefsState: "json-string", NotesState: "object", Notes: NoteArchiveFacts{"global_notifs": noteRaw("default"), "note_watching": noteRaw(map[string]any{"1": "RAW OLD TIMESTAMP"}), "read_upto": noteRaw(nil)}, Facts: NoteArchiveFacts{}}, {ID: NoteArchiveID("legacy-pref", 7), Format: "user_pref", LegacyID: 7, LegacyUserID: 0, Facts: NoteArchiveFacts{"id": noteRaw(7), "user_id": noteRaw(nil), "attribute": noteRaw("customer-notes.watching.1"), "value": noteRaw("not an event timestamp"), "ix": noteRaw(0), "op": noteRaw("="), "expire": noteRaw(0)}}}, Warnings: []string{"source only"}}
}
func TestNoteArchivePreservesFactsAndSeparateFormats(t *testing.T) {
	a := noteArchiveFixture()
	raw, err := EncodeNoteArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadNoteArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	a.Checksum = NoteArchiveChecksum(a)
	if !reflect.DeepEqual(a, *read) {
		t.Fatal("source facts, whitespace, null or separate source formats changed")
	}
	if _, ok := read.Notes[0].Facts["created_at"]; ok || string(read.Notes[0].Facts["created"]) != "null" {
		t.Fatal("missing field was confused with source null")
	}
	if read.Preferences[1].LegacyUserID != 0 || read.Access != "staff" {
		t.Fatal("unresolved source identity gained a current grant")
	}
}
func TestNoteArchivePrivacyOnlyExplicitPublic(t *testing.T) {
	for _, value := range []any{false, 0, "0", "false"} {
		if v, known := NoteArchivePrivacy(noteRaw(value)); v != "member" || !known {
			t.Fatal("explicit public source flag lost", value)
		}
	}
	for _, value := range []any{true, 1, "1", "true"} {
		if v, known := NoteArchivePrivacy(noteRaw(value)); v != "internal" || !known {
			t.Fatal("explicit internal source flag lost", value)
		}
	}
	for _, value := range []any{nil, "unknown", " default ", "", "00", 2, []any{}, map[string]any{}} {
		if v, known := NoteArchivePrivacy(noteRaw(value)); v != "internal" || known {
			t.Fatal("unknown source flag became public or known", value)
		}
	}
	if v, known := NoteArchivePrivacy(nil); v != "internal" || known {
		t.Fatal("missing privacy became public")
	}
}
func TestNoteArchiveRejectsUnknownCredentialsAndInconsistentFacts(t *testing.T) {
	for _, mode := range []string{"access", "version", "unknown-fact", "multiword-fact", "password", "other-preference", "attribute", "association", "duplicate", "privacy", "watch-id", "watch-value"} {
		t.Run(mode, func(t *testing.T) {
			a := noteArchiveFixture()
			switch mode {
			case "access":
				a.Access = "public"
			case "version":
				a.Version = 2
			case "unknown-fact":
				a.Notes[0].Facts["unsupported"] = noteRaw("value")
			case "multiword-fact":
				a.Notes[0].Facts["id customer_id"] = noteRaw("must not match a combined whitelist substring")
			case "password":
				a.Users[0].Facts["password"] = noteRaw("DO-NOT-EXPORT")
			case "other-preference":
				a.Preferences[0].Notes["two_factor"] = noteRaw("DO-NOT-EXPORT")
			case "attribute":
				a.Preferences[1].Facts["attribute"] = noteRaw("mailinglist.secret")
			case "association":
				a.Preferences[0].LegacyUserID = 8
			case "duplicate":
				a.Notes = append(a.Notes, a.Notes[0])
			case "privacy":
				a.Notes[0].PrivacyKnown = false
			case "watch-id":
				a.Preferences[0].Notes["note_watching"] = noteRaw(map[string]any{"1.5": "date"})
			case "watch-value":
				a.Preferences[0].Notes["note_watching"] = noteRaw(map[string]any{"1": map[string]any{"nested": "no"}})
			}
			if _, err := EncodeNoteArchive(a); err == nil {
				t.Fatal("unsafe source envelope accepted", mode)
			}
		})
	}
}
func TestNoteArchiveReaderRejectsAmbiguousOrChangedJSON(t *testing.T) {
	raw, err := EncodeNoteArchive(noteArchiveFixture())
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1),
		bytes.Replace(raw, []byte("original body"), []byte("modified body"), 1),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"note_watching":{"1":"RAW OLD TIMESTAMP"}`), []byte(`"note_watching":{"1":"RAW OLD TIMESTAMP","1":"conflict"}`), 1),
		append([]byte(`{"nested":`), append(bytes.Repeat([]byte("["), 20), append([]byte("0"), append(bytes.Repeat([]byte("]"), 20), '}')...)...)...),
	}
	for i, value := range bad {
		if bytes.Equal(value, raw) {
			t.Fatalf("invalid fixture %d did not change", i)
		}
		if _, err := ReadNoteArchive(bytes.NewReader(value)); err == nil {
			t.Fatal("ambiguous source JSON accepted", i)
		}
	}
}

func TestNoteArchiveRejectsDeclaredRecordAndSourceByteExcess(t *testing.T) {
	if value, err := noteArchiveScalar(noteRaw("<>&"), 3); err != nil || value != "<>&" {
		t.Fatal("JSON escapes incorrectly reduced the original scalar byte limit", err)
	}
	if _, err := noteArchiveScalar(noteRaw("<>&"), 2); err == nil {
		t.Fatal("decoded scalar byte limit not enforced")
	}
	largeButBounded := map[string]any{}
	for i := 1; i <= 600; i++ {
		largeButBounded[fmt.Sprint(i)] = strings.Repeat("x", 2048)
	}
	if err := noteArchiveModern(NoteArchiveFacts{"note_watching": noteRaw(largeButBounded)}); err != nil {
		t.Fatal("valid raw watch values inside archive/count bounds were dropped", err)
	}
	a := noteArchiveFixture()
	a.Notes[0].Facts["note"] = noteRaw(strings.Repeat("x", NoteArchiveBodyLimit+1))
	if _, err := EncodeNoteArchive(a); err == nil {
		t.Fatal("oversized individual source body accepted")
	}
	a = noteArchiveFixture()
	a.Preferences[0].Notes["note_watching"] = noteRaw(map[string]any{"1": []any{"not scalar"}})
	if _, err := EncodeNoteArchive(a); err == nil {
		t.Fatal("nested watch source value accepted")
	}
	a = noteArchiveFixture()
	watch := map[string]any{}
	for i := 1; i <= NoteArchiveWatchLimit+1; i++ {
		watch[fmt.Sprint(i)] = "original"
	}
	a.Preferences[0].Notes["note_watching"] = noteRaw(watch)
	if _, err := EncodeNoteArchive(a); err == nil {
		t.Fatal("oversized watch map accepted")
	}
	a = noteArchiveFixture()
	a.Notes = make([]NoteArchiveNote, NoteArchiveRecordLimit+1)
	if err := ValidateNoteArchive(&a); err == nil {
		t.Fatal("oversized source record graph accepted")
	}
}

func TestNoteArchiveSourceJSONPreservesValidUnicodeAndRejectsReplacement(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"value":"\ud83d\ude80"}`), []byte(`{"value":"\uD800\uDC00"}`),
		[]byte(`{"value":"\uDBFF\uDFFF"}`), []byte(`{"value":"\uFFFD"}`),
		[]byte("{\"value\":\"�\"}"), []byte(`{"value":"\\ud800"}`),
		[]byte(`{"value":"\u005cud800"}`), []byte(`{"value":"quote\"\\literal"}`),
	} {
		if err := ValidateNoteArchiveSourceJSON(raw); err != nil {
			t.Fatalf("valid source string rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`{"value":"\ud800"}`), []byte(`{"value":"\udc00"}`),
		[]byte(`{"value":"\ud800\ud800"}`), []byte(`{"value":"\udc00\ud800"}`),
		[]byte(`{"value":"\ud800 \udc00"}`), []byte(`{"value":"\ud800\\udc00"}`),
		[]byte(`{"value":"\u12"}`), []byte(`{"\ud800":"key"}`),
		[]byte{'{', '"', 'v', '"', ':', '"', 0xff, '"', '}'},
		[]byte(`{"tables":{"user":[],"user":[]}}`), []byte(`{"na\u006de":1,"name":2}`),
	} {
		if err := ValidateNoteArchiveSourceJSON(raw); err == nil {
			t.Fatalf("source input requiring replacement or key loss accepted: %q", raw)
		}
	}
}

func TestNoteArchiveSourceJSONUsesDatabaseLimitAndOwnStructureBounds(t *testing.T) {
	raw := append(bytes.Repeat([]byte(" "), int(NoteArchiveLimit)+1), []byte(`{"tables":{}}`)...)
	if err := ValidateNoteArchiveSourceJSON(raw); err != nil {
		t.Fatal("complete source export was incorrectly constrained by package limit", err)
	}
	if err := strictNoteJSON(raw); err == nil {
		t.Fatal("archive JSON ignored its smaller package bound")
	}
	deep := []byte(strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34))
	if err := ValidateNoteArchiveSourceJSON(deep); err == nil {
		t.Fatal("over-deep source JSON accepted")
	}
	if err := validateNoteJSON([]byte(`[0,0,0]`), 100, 2, 4, 100); err != nil {
		t.Fatal("bounded test graph rejected", err)
	}
	if err := validateNoteJSON([]byte(`[0,0,0,0]`), 100, 2, 4, 100); err == nil {
		t.Fatal("node count limit not enforced")
	}
	if err := validateNoteJSON([]byte(`{"v":1}`), 5, 32, 8000000, 100); err == nil {
		t.Fatal("byte limit not enforced")
	}
}
func TestNoteArchiveWriteIsPrivateAtomicAndDoesNotFollowLinks(t *testing.T) {
	parent := t.TempDir()
	name := filepath.Join(parent, "notes.json")
	a := noteArchiveFixture()
	if err := WriteNoteArchive(name, a); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(name)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("archive not private", err)
	}
	before, _ := os.ReadFile(name)
	if err = WriteNoteArchive(name, a); err == nil {
		t.Fatal("existing archive overwritten")
	}
	after, _ := os.ReadFile(name)
	if !bytes.Equal(before, after) {
		t.Fatal("existing output changed")
	}
	link := filepath.Join(parent, "link.json")
	if err = os.Symlink(name, link); err != nil {
		t.Fatal(err)
	}
	if err = WriteNoteArchive(link, a); err == nil {
		t.Fatal("symlink output replaced")
	}
	linkedParent := filepath.Join(parent, "parent-link")
	if err = os.Symlink(parent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if err = WriteNoteArchive(filepath.Join(linkedParent, "unexpected.json"), a); err == nil {
		t.Fatal("symlink directory accepted")
	}
	for _, output := range []string{"", "-"} {
		if err = WriteNoteArchive(output, a); err == nil {
			t.Fatal("non-file output accepted")
		}
	}
	entries, _ := os.ReadDir(parent)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".note-source-") {
			t.Fatal("temporary source file retained")
		}
	}
}
