package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const noteArchiveSource = `{"tables":{
"cust_notes":[
 {"id":1,"customer_id":999,"title":"  raw title\r\n  ","note":"  body\r\nsecond line  ","private":null,"created":null,"updated":"2014-01-02"},
 {"id":2,"customer_id":null,"title":"","note":"","private":0,"created_at":"2020-01-01 12:00:00"}],
"user":[
 {"id":7,"name":"Original actor","email":"old@example.test","password":"PASSWORD-MUST-NOT-EXPORT","two_factor_secret":"2FA-MUST-NOT-EXPORT","prefs":{"notes":{"global_notifs":"default","customer_watching":{"999":"ORIGINAL WATCH VALUE"},"note_watching":{"1":null},"last_read":{"999":"OLD READ CURSOR"},"read_upto":42,"unknown":"UNKNOWN-NOTES-FIELD-OMITTED"},"tokens":{"value":"TOKEN-MUST-NOT-EXPORT"}}},
 {"id":8,"username":"No preference","prefs":null},
 {"id":9,"username":"Missing preference"}],
"user_pref":[
 {"id":1,"user_id":7,"attribute":"customer-notes.notify","value":"watched","ix":0,"op":"=","expire":0},
 {"id":2,"user_id":7,"attribute":"customer-notes.watching.1","value":"OLD BUG AFFECTED TIMESTAMP","ix":1,"op":"!=","expire":123},
 {"id":3,"user_id":null,"attribute":"customer-notes.999.last_read","value":null},
 {"id":4,"user_id":7,"attribute":"mailinglist.secret","value":"MAILING-SECRET-MUST-NOT-EXPORT"}]
}}`

func TestNoteArchiveConversionPreservesRawAndNeverImportsAuthorities(t *testing.T) {
	source := decodeSample(t, noteArchiveSource)
	a, err := buildNoteArchive(source, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Notes) != 2 || len(a.Users) != 3 || len(a.Preferences) != 6 || a.Access != "staff" {
		t.Fatal("empty/orphan note or original preference source lost")
	}
	var title, body string
	json.Unmarshal(a.Notes[0].Facts["title"], &title)
	json.Unmarshal(a.Notes[0].Facts["note"], &body)
	if title != "  raw title\r\n  " || body != "  body\r\nsecond line  " {
		t.Fatal("source whitespace or CRLF was normalized")
	}
	if a.Notes[0].PrivacyKnown || a.Notes[0].Visibility != "internal" || a.Notes[1].Visibility != "member" || !a.Notes[1].PrivacyKnown {
		t.Fatal("source privacy was guessed")
	}
	raw, err := migrate.EncodeNoteArchive(*a)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PASSWORD-MUST-NOT-EXPORT", "2FA-MUST-NOT-EXPORT", "TOKEN-MUST-NOT-EXPORT", "UNKNOWN-NOTES-FIELD-OMITTED", "MAILING-SECRET-MUST-NOT-EXPORT", `"password"`, `"two_factor_secret"`} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("unrelated account material leaked", secret)
		}
	}
	if !bytes.Contains(raw, []byte("OLD BUG AFFECTED TIMESTAMP")) || !bytes.Contains(raw, []byte(`"op":"!=`)) || !bytes.Contains(raw, []byte(`"expire":123`)) {
		t.Fatal("legacy timestamp/op/expiry was lost or interpreted")
	}
	if len(a.Warnings) < 4 {
		t.Fatal("missing/default/privacy uncertainties were not reported")
	}
	read, err := migrate.ReadNoteArchive(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(a, read) {
		t.Fatal("archive roundtrip changed selected source values", err)
	}
}

func TestNoteArchivePrefsPresenceAndJSONColumn(t *testing.T) {
	for _, test := range []struct {
		field        any
		present      bool
		prefs, notes string
	}{
		{nil, false, "missing", "missing"}, {nil, true, "null", "missing"},
		{map[string]any{"notes": nil}, true, "object", "null"},
		{[]any{}, true, "empty-array", "missing"},
		{map[string]any{"notes": []any{}}, true, "object", "empty-array"},
		{`{"notes":{"global_notifs":"all"},"other":{"password":"NOT-EXPORTED"}}`, true, "json-string", "object"},
		{`{"notes":{"note_watching":[],"customer_watching":{},"last_read":null}}`, true, "json-string", "object"},
		{`null`, true, "json-string", "missing"},
		{`[ ]`, true, "json-string", "missing"},
	} {
		r := row{"id": json.Number("1")}
		if test.present {
			r["prefs"] = test.field
		}
		a, err := buildNoteArchive(legacyExport{Tables: map[string][]row{"user": {r}}}, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		p := a.Preferences[0]
		if p.PrefsState != test.prefs || p.NotesState != test.notes {
			t.Fatal("source column or notes presence lost", p)
		}
		raw, _ := migrate.EncodeNoteArchive(*a)
		if bytes.Contains(raw, []byte("NOT-EXPORTED")) {
			t.Fatal("other preference branch retained")
		}
	}
}

func TestNoteArchiveRejectsDuplicateOrMalformedSource(t *testing.T) {
	for _, bad := range []string{
		`{"notes":{},"notes":{"global_notifs":"all"}}`,
		`{"notes":{"global_notifs":"none","global_notifs":"all"}}`,
		`{"notes":{"note_watching":{"1":"a","1":"b"}}}`,
		`{"notes":"invalid"}`, `{"notes":{"note_watching":[1,2]}}`,
		`{"notes":{"note_watching":{"01":"ambiguous"}}}`, `[1]`, `"invalid"`, `{"notes":{}} {}`,
		`{"notes":{"global_notifs":"\ud800"}}`, `{"notes":{"\ud800":"bad-key"}}`,
	} {
		_, err := buildNoteArchive(legacyExport{Tables: map[string][]row{"user": {{"id": json.Number("1"), "prefs": bad}}}}, "UTC")
		if err == nil {
			t.Fatal("ambiguous source preference JSON accepted", bad)
		}
	}
	s := decodeSample(t, noteArchiveSource)
	s.Tables["cust_notes"] = append(s.Tables["cust_notes"], s.Tables["cust_notes"][0])
	if _, err := buildNoteArchive(s, "UTC"); err == nil {
		t.Fatal("duplicate note identity accepted")
	}
	s = decodeSample(t, noteArchiveSource)
	s.Tables["user_pref"] = append(s.Tables["user_pref"], s.Tables["user_pref"][0])
	if _, err := buildNoteArchive(s, "UTC"); err == nil {
		t.Fatal("duplicate legacy pref identity accepted")
	}
	if _, err := buildNoteArchive(legacyExport{}, "UTC"); err == nil {
		t.Fatal("missing source tables accepted")
	}
	if _, err := buildNoteArchive(legacyExport{Tables: map[string][]row{}}, "UTC"); err == nil {
		t.Fatal("empty source archive accepted")
	}
}

func TestNoteArchiveSourceChecksumIsDeterministicAndNoLiveResourcePayload(t *testing.T) {
	s := decodeSample(t, noteArchiveSource)
	a, err := buildNoteArchive(s, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	s.Tables["cust_notes"][0], s.Tables["cust_notes"][1] = s.Tables["cust_notes"][1], s.Tables["cust_notes"][0]
	b, err := buildNoteArchive(s, "UTC")
	if err != nil || a.Checksum != b.Checksum {
		t.Fatal("source row order changed archive identity", err)
	}
	raw, _ := migrate.EncodeNoteArchive(*a)
	for _, key := range []string{`"resources":`, `"outbox":`, `"memberWatches":`, `"reads":`, `"security":`} {
		if strings.Contains(string(raw), key) {
			t.Fatal("source archive fabricated live state", key)
		}
	}
}

func TestNoteArchiveCLIBypassesInventoryAndPublishesOnlyPrivateFile(t *testing.T) {
	s := decodeSample(t, noteArchiveSource)
	// An unrelated inventory conversion would reject this malformed switch ID.
	s.Tables["switch"] = []row{{"id": "INVALID-INVENTORY-ID"}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "sources.json")
	var out, log bytes.Buffer
	args := []string{"--note-archive", "--source-timezone", "UTC", "--output", name}
	if err = run(args, bytes.NewReader(raw), &out, &log); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("private source archive was printed on stdout")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	a, err := migrate.ReadNoteArchive(bytes.NewReader(data))
	if err != nil || len(a.Notes) != 2 || len(a.Preferences) != 6 {
		t.Fatal("CLI lost archived source rows", err)
	}
	if st, err := os.Stat(name); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("CLI source output not private", err)
	}
	if err = run(args, bytes.NewReader(raw), &out, &log); err == nil {
		t.Fatal("CLI overwrote existing source archive")
	}
	after, _ := os.ReadFile(name)
	if !bytes.Equal(data, after) {
		t.Fatal("failed publication changed prior output")
	}
	for _, secret := range []string{"PASSWORD-MUST-NOT-EXPORT", "2FA-MUST-NOT-EXPORT", "TOKEN-MUST-NOT-EXPORT", "OLD BUG AFFECTED TIMESTAMP", "old@example.test"} {
		if strings.Contains(log.String(), secret) {
			t.Fatal("private source value leaked in CLI diagnostics", secret)
		}
	}
}

func TestNoteArchiveCLIStrictAndModeErrorsLeaveNoOutput(t *testing.T) {
	for _, extra := range [][]string{{"--strict"}, {"--patch-bundle"}, {"--docstore-bundle"}, {"--logo-bundle"}, {"--resources-only"}} {
		name := filepath.Join(t.TempDir(), "refused.json")
		args := append([]string{"--note-archive", "--output", name}, extra...)
		var out, log bytes.Buffer
		if err := run(args, strings.NewReader(noteArchiveSource), &out, &log); err == nil {
			t.Fatal("unsafe CLI mode accepted", extra)
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) || out.Len() != 0 {
			t.Fatal("refused CLI mode produced output", extra)
		}
	}
	var out, log bytes.Buffer
	if err := run([]string{"--note-archive"}, strings.NewReader(noteArchiveSource), &out, &log); err == nil || out.Len() != 0 {
		t.Fatal("private source package allowed stdout")
	}
}

func TestNoteArchiveCLIRejectsSourceReplacementAndDuplicateKeysBeforeDecode(t *testing.T) {
	base := `{"tables":{"cust_notes":[{"id":1,"customer_id":1,"private":1,"title":"source","note":BODY}]}}`
	for _, raw := range [][]byte{
		[]byte(strings.Replace(base, "BODY", `"\ud800"`, 1)),
		[]byte(strings.Replace(base, "BODY", `"\udc00"`, 1)),
		[]byte(strings.Replace(base, "BODY", "\""+string([]byte{0xff})+"\"", 1)),
		[]byte(strings.Replace(strings.Replace(base, "BODY", `"original"`, 1), `"private":1`, `"private":1,"private":0`, 1)),
		[]byte(`{"tables":{"cust_notes":[],"cust_notes":[]}}`),
	} {
		name := filepath.Join(t.TempDir(), "rejected.json")
		var out, log bytes.Buffer
		if err := run([]string{"--note-archive", "--source-timezone", "UTC", "--output", name}, bytes.NewReader(raw), &out, &log); err == nil {
			t.Fatalf("source requiring value replacement or duplicate-key loss accepted: %q", raw)
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) || out.Len() != 0 {
			t.Fatal("invalid source produced a seemingly valid output")
		}
	}
	for _, test := range []struct{ quoted, expected string }{{`"�"`, "�"}, {`"\ufffd"`, "�"}, {`"\ud83d\ude80"`, "🚀"}, {`"\\ud800"`, `\ud800`}} {
		name := filepath.Join(t.TempDir(), "source.json")
		var out, log bytes.Buffer
		input := strings.Replace(base, "BODY", test.quoted, 1)
		if err := run([]string{"--note-archive", "--source-timezone", "UTC", "--output", name}, strings.NewReader(input), &out, &log); err != nil {
			t.Fatalf("valid source Unicode rejected: %s: %v", test.quoted, err)
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := migrate.ReadNoteArchive(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		var body string
		if err := json.Unmarshal(archive.Notes[0].Facts["note"], &body); err != nil || body != test.expected {
			t.Fatal("valid source value changed", err)
		}
	}
}
