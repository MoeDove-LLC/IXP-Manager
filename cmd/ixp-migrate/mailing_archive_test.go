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

const mailingArchiveFixture = `{"tables":{"user":[
{"id":9007199254740993,"email":"old@example.test","password":"PASSWORD-OMITTED","prefs":{"mailinglist":{"members":"1","out":"0","unknown":"UNKNOWN-PRIVATE-SOURCE","boolean":true},"notes":{"global_notifs":"NOTES-OMITTED"},"secret":"SECRET-OMITTED"}},
{"id":2,"email":"OTHER-USER-OMITTED","prefs":[]},
{"id":3,"email":"historic@example.test","prefs":"{\"mailinglist\":{\"numeric\":0},\"tokens\":\"TOKENS-OMITTED\"}"}],
"user_pref":[{"id":4,"user_id":3,"attribute":"mailinglist.previous","value":"1","ix":null,"op":"=","expire":123},
{"id":5,"user_id":3,"attribute":"customer-notes.notify","value":"OLD-NOTES-OMITTED"}]}}`

func TestMailingArchiveActualSourceIsolationAndExplicitValues(t *testing.T) {
	a, err := buildMailingArchive(decodeSample(t, mailingArchiveFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Users) != 2 || len(a.Choices) != 6 || a.Access != "admin" {
		t.Fatal("wrong selected source counts", a)
	}
	raw, err := migrate.EncodeMailingArchive(*a)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PASSWORD-OMITTED", "SECRET-OMITTED", "TOKENS-OMITTED", "NOTES-OMITTED", "OLD-NOTES-OMITTED", "OTHER-USER-OMITTED"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("unrelated preference or identity leaked", secret)
		}
	}
	if !bytes.Contains(raw, []byte("UNKNOWN-PRIVATE-SOURCE")) {
		t.Fatal("authorized unknown source scalar lost")
	}
	for key, want := range map[string]string{"members": "subscribed", "out": "unsubscribed", "unknown": "unknown", "boolean": "unknown"} {
		_, got := migrate.MailingArchiveSelection(a, "9007199254740993", key)
		if got != want {
			t.Fatal(key, got, want)
		}
	}
	for key, want := range map[string]string{"numeric": "unsubscribed", "previous": "subscribed"} {
		_, got := migrate.MailingArchiveSelection(a, "3", key)
		if got != want {
			t.Fatal(key, got, want)
		}
	}
	copy, err := migrate.ReadMailingArchive(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(copy, a) {
		t.Fatal("source roundtrip changed values", err)
	}
	conflict := a.Choices[0]
	conflict.ID = "pref-10"
	conflict.Format = "user_pref"
	conflict.Facts = migrate.NoteArchiveFacts{"id": json.RawMessage("10"), "user_id": json.RawMessage(conflict.SourceUserID), "attribute": json.RawMessage(`"mailinglist.` + conflict.SourceKey + `"`), "value": conflict.Value}
	a.Choices = append(a.Choices, conflict)
	if _, desired := migrate.MailingArchiveSelection(a, conflict.SourceUserID, conflict.SourceKey); desired != "unknown" {
		t.Fatal("duplicate source rows were arbitrarily prioritized")
	}
}
func TestMailingArchiveRejectsMalformedDuplicateAndSecretEnvelope(t *testing.T) {
	for _, prefs := range []string{`{"mailinglist":{"members":"1","members":"0"}}`, `{"mailinglist":{"members":{"secret":"bad"}}}`, `{"mailinglist":{"members":"\ud800"}}`, `{"mailinglist":{"key.with.dot":"1"}}`, `{"mailinglist":true}`, `{"mailinglist":{}} {}`} {
		_, err := buildMailingArchive(legacyExport{Tables: map[string][]row{"user": {{"id": json.Number("1"), "prefs": prefs}}}})
		if err == nil {
			t.Fatal("unsafe source accepted", prefs)
		}
	}
	for _, source := range []string{`{"tables":{"users":[{"id":1}]}}`, `{"tables":{"user_preferences":[{"id":1}]}}`, `{"tables":{"user":[{"id":1,"prefs":{"mailinglist":{"members":1}}},{"id":1}]}}`} {
		if _, err := buildMailingArchive(decodeSample(t, source)); err == nil {
			t.Fatal("unknown schema or repeated identity accepted")
		}
	}
	var stdout, stderr bytes.Buffer
	duplicate := strings.Replace(mailingArchiveFixture, `"members":"1"`, `"members":"1","members":"0"`, 1)
	if err := run([]string{"--mailing-archive", "--output", filepath.Join(t.TempDir(), "bad.json")}, strings.NewReader(duplicate), &stdout, &stderr); err == nil {
		t.Fatal("top-level duplicate preference keys bypassed strict source parser")
	}
	a, _ := buildMailingArchive(decodeSample(t, mailingArchiveFixture))
	raw, _ := migrate.EncodeMailingArchive(*a)
	bad := bytes.Replace(raw, []byte(`"access":"admin"`), []byte(`"access":"staff"`), 1)
	if _, err := migrate.ReadMailingArchive(bytes.NewReader(bad)); err == nil {
		t.Fatal("tampered envelope accepted")
	}
	a.Users[0].Facts["password"] = json.RawMessage(`"bad"`)
	if _, err := migrate.EncodeMailingArchive(*a); err == nil {
		t.Fatal("unrelated source fact entered private archive")
	}
}
func TestMailingArchiveCLIPrivateNoClobberOrStdout(t *testing.T) {
	var out, errout bytes.Buffer
	for _, args := range [][]string{{"--mailing-archive"}, {"--mailing-archive", "--resources-only"}, {"--mailing-archive", "--strict", "--output", filepath.Join(t.TempDir(), "strict.json")}} {
		if err := run(args, strings.NewReader(mailingArchiveFixture), &out, &errout); err == nil || out.Len() != 0 {
			t.Fatal("private archive reached stdout or incompatible mode accepted", args)
		}
	}
	path := filepath.Join(t.TempDir(), "mailing.json")
	if err := run([]string{"--mailing-archive", "--output", path}, strings.NewReader(mailingArchiveFixture), &out, &errout); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 || out.Len() != 0 {
		t.Fatal("archive not private")
	}
	before, _ := os.ReadFile(path)
	if err := run([]string{"--mailing-archive", "--output", path}, strings.NewReader(mailingArchiveFixture), &out, &errout); err == nil {
		t.Fatal("existing evidence overwritten")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing evidence changed")
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--mailing-archive", "--output", link}, strings.NewReader(mailingArchiveFixture), &out, &errout); err == nil {
		t.Fatal("archive output symlink followed")
	}
}
