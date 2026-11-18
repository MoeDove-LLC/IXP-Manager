package migrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func accountTestFacts(raw string) NoteArchiveFacts {
	var facts NoteArchiveFacts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		panic(err)
	}
	return facts
}
func accountTestArchive() AccountArchive {
	return AccountArchive{Version: 1, Kind: AccountArchiveKind, Source: "ixp-manager-legacy", Access: "admin", SourceTimezone: "UTC",
		Users:       []AccountArchiveUser{{LegacyID: "9007199254740993", Facts: accountTestFacts(`{"id":"9007199254740993","name":"Original","email":"ORIGINAL@example.test","disabled":0,"created_at":"2020-01-02 03:04:05","custid":"7"}`)}},
		Memberships: []AccountArchiveMembership{{LegacyID: "8", SourceUserID: "9007199254740993", SourceCustomerID: "7", Facts: accountTestFacts(`{"id":"8","user_id":"9007199254740993","customer_id":"7","privs":2,"created_at":"2020-01-02T03:04:05Z"}`)}},
	}
}
func TestAccountArchiveRoundtripAndStrictBoundary(t *testing.T) {
	a := accountTestArchive()
	raw, err := EncodeAccountArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := ReadAccountArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	a.Checksum = AccountArchiveChecksum(a)
	if !reflect.DeepEqual(a, *copy) || len(AccountArchiveReview(copy)) != 0 {
		t.Fatal("source changed or valid evidence misclassified")
	}
	for name, mutate := range map[string]func(*AccountArchive){
		"secret":               func(a *AccountArchive) { a.Users[0].Facts["passwordHash"] = json.RawMessage(`"SECRET"`) },
		"noncanonical-fact":    func(a *AccountArchive) { a.Users[0].Facts["id username"] = json.RawMessage(`"SECRET"`) },
		"privilege-field":      func(a *AccountArchive) { a.Memberships[0].Facts["role"] = json.RawMessage(`"admin"`) },
		"duplicate-user":       func(a *AccountArchive) { a.Users = append(a.Users, a.Users[0]) },
		"duplicate-membership": func(a *AccountArchive) { a.Memberships = append(a.Memberships, a.Memberships[0]) },
		"rounded-id":           func(a *AccountArchive) { a.Users[0].Facts["id"] = json.RawMessage(`9007199254740993`) },
		"retargeted-user":      func(a *AccountArchive) { a.Memberships[0].SourceUserID = "9" },
		"retargeted-org":       func(a *AccountArchive) { a.Memberships[0].SourceCustomerID = "9" },
		"complex-label":        func(a *AccountArchive) { a.Users[0].Facts["name"] = json.RawMessage(`{"secret":"SECRET"}`) },
		"oversized-label":      func(a *AccountArchive) { a.Users[0].Facts["name"], _ = json.Marshal(strings.Repeat("x", 4097)) },
		"nil-users":            func(a *AccountArchive) { a.Users = nil },
		"missing-zone":         func(a *AccountArchive) { a.SourceTimezone = "" },
		"local-zone":           func(a *AccountArchive) { a.SourceTimezone = "Local" },
	} {
		t.Run(name, func(t *testing.T) {
			a := accountTestArchive()
			mutate(&a)
			if _, err := EncodeAccountArchive(a); err == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
	for name, bad := range map[string][]byte{
		"tampered":        bytes.Replace(raw, []byte(`"privs":2`), []byte(`"privs":1`), 1),
		"case-envelope":   bytes.Replace(raw, []byte(`"users"`), []byte(`"Users"`), 1),
		"case-row":        bytes.Replace(raw, []byte(`"legacyId"`), []byte(`"LegacyId"`), 1),
		"duplicate-key":   bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"trailing":        append(append([]byte{}, raw...), []byte(`{}`)...),
		"secret-envelope": bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"password":"SECRET"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadAccountArchive(bytes.NewReader(bad)); err == nil {
				t.Fatal("ambiguous or modified artifact accepted")
			}
		})
	}
}

func TestAccountArchiveReviewNeverMergesOrGrants(t *testing.T) {
	a := accountTestArchive()
	a.Users = append(a.Users, AccountArchiveUser{LegacyID: "2", Facts: accountTestFacts(`{"id":"2","email":"original@EXAMPLE.test","disabled":"1","created_at":null}`)}, AccountArchiveUser{LegacyID: "3", Facts: accountTestFacts(`{"id":"3","email":false,"disabled":"false","custid":"77"}`)})
	a.Memberships = append(a.Memberships,
		AccountArchiveMembership{LegacyID: "9", SourceUserID: "9007199254740993", SourceCustomerID: "7", Facts: accountTestFacts(`{"id":"9","user_id":"9007199254740993","customer_id":"7","privs":3}`)},
		AccountArchiveMembership{LegacyID: "10", SourceUserID: "2", SourceCustomerID: "42", Facts: accountTestFacts(`{"id":"10","user_id":"2","customer_id":"42","privs":"1"}`)},
		AccountArchiveMembership{LegacyID: "11", SourceUserID: "404", SourceCustomerID: "42", Facts: accountTestFacts(`{"id":"11","user_id":"404","customer_id":"42","privs":"UNKNOWN-PRIVATE"}`)})
	raw, err := EncodeAccountArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := ReadAccountArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(copy.Users) != 3 || len(copy.Memberships) != 4 {
		t.Fatal("source rows merged or dropped")
	}
	findings := AccountArchiveReview(copy)
	have := map[string]bool{}
	for _, finding := range findings {
		have[finding.Entity+":"+finding.LegacyID+":"+finding.Code] = true
	}
	for _, want := range []string{"user:9007199254740993:email-collision", "user:2:email-collision", "user:2:state-disabled", "user:3:state-unknown", "user:3:email-unusable", "user:3:memberships-missing", "user:3:default-membership-unresolved", "membership:8:membership-duplicate", "membership:9:privilege-superuser", "membership:10:user-state-disabled", "membership:11:user-unresolved", "membership:11:privilege-unknown"} {
		if !have[want] {
			t.Error("missing review", want)
		}
	}
	encoded, _ := json.Marshal(findings)
	if bytes.Contains(encoded, []byte("original")) || bytes.Contains(encoded, []byte("UNKNOWN-PRIVATE")) {
		t.Fatal("review leaked raw identity/unknown value")
	}
	for _, raw := range []string{`true`, `null`, `"admin"`, `4`, `"01"`} {
		if AccountArchivePrivilege(AccountArchiveMembership{Facts: NoteArchiveFacts{"privs": json.RawMessage(raw)}}) != "unknown" {
			t.Fatal("unknown privilege authorized", raw)
		}
	}
}

func TestAccountArchiveCreationEvidence(t *testing.T) {
	for _, tc := range []struct{ name, raw, zone, want string }{
		{"offset", `{"created_at":"2020-01-02T03:04:05+08:00"}`, "unknown", "2020-01-01T19:04:05Z"},
		{"legacy", `{"created":"2020-01-02 03:04:05"}`, "UTC", "2020-01-02T03:04:05Z"},
		{"microseconds", `{"created_at":"2020-01-02 03:04:05.123456"}`, "Asia/Shanghai", "2020-01-01T19:04:05.123456Z"},
		{"same", `{"created":"2020-01-02 03:04:05","created_at":"2020-01-02T03:04:05Z"}`, "UTC", "2020-01-02T03:04:05Z"},
		{"conflict", `{"created":"2020-01-02 03:04:05","created_at":"2021-01-02 03:04:05"}`, "UTC", ""},
		{"unknown-zone", `{"created_at":"2020-01-02 03:04:05"}`, "unknown", ""},
		{"null-column", `{"created":null,"created_at":"2020-01-02T03:04:05Z"}`, "UTC", ""},
		{"date-only", `{"created_at":"2020-01-02"}`, "UTC", ""},
		{"zero-date", `{"created_at":"0000-00-00 00:00:00"}`, "UTC", ""},
		{"dst-fold", `{"created_at":"2024-11-03 01:30:00"}`, "America/New_York", ""},
		{"dst-gap", `{"created_at":"2024-03-10 02:30:00"}`, "America/New_York", ""},
		{"half-hour-fold", `{"created_at":"2024-04-07 01:45:00"}`, "Australia/Lord_Howe", ""},
		{"half-hour-gap", `{"created_at":"2024-10-06 02:15:00"}`, "Australia/Lord_Howe", ""},
		{"skipped-day", `{"created_at":"2011-12-30 12:00:00"}`, "Pacific/Apia", ""},
		{"normal-summer", `{"created_at":"2024-06-01 12:00:00"}`, "America/New_York", "2024-06-01T16:00:00Z"},
		{"sql-precision-loss", `{"created_at":"2020-01-02 03:04:05.123456789123"}`, "UTC", ""},
		{"rfc-precision-loss", `{"created_at":"2020-01-02T03:04:05.123456789123Z"}`, "UTC", ""},
		{"invalid-offset", `{"created_at":"2020-01-02T03:04:05+24:00"}`, "UTC", ""},
		{"comma-fraction", `{"created_at":"2020-01-02T03:04:05,123Z"}`, "UTC", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AccountArchiveCreation(accountTestFacts(tc.raw), tc.zone)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("got %q %v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestAccountArchiveBounds(t *testing.T) {
	a := accountTestArchive()
	a.Users = make([]AccountArchiveUser, AccountArchiveRecordLimit+1)
	if _, err := EncodeAccountArchive(a); err == nil {
		t.Fatal("unbounded account rows accepted")
	}
	if _, err := ReadAccountArchive(strings.NewReader(strings.Repeat(" ", int(AccountArchiveLimit)+1))); err == nil {
		t.Fatal("unbounded archive bytes accepted")
	}
}

func TestAccountArchivePrivatePublication(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "accounts.json")
	a := accountTestArchive()
	if err := WriteAccountArchive(output, a); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("artifact not private")
	}
	before, _ := os.ReadFile(output)
	if err := WriteAccountArchive(output, a); err == nil {
		t.Fatal("clobbered existing archive")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("archive changed")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(output, link); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "-", link} {
		if err := WriteAccountArchive(name, a); err == nil {
			t.Fatal("unsafe output accepted")
		}
	}
	parentLink := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccountArchive(filepath.Join(parentLink, "escaped.json"), a); err == nil {
		t.Fatal("followed parent symlink")
	}
	unsafe := filepath.Join(dir, "writable")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	if err := WriteAccountArchive(filepath.Join(unsafe, "exposed.json"), a); err == nil {
		t.Fatal("used writable parent")
	}
}
