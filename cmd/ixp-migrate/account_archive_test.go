package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const accountArchiveFixture = `{"tables":{"user":[
{"id":9007199254740993,"name":"Original member","username":"old-login","email":"Member@example.test","disabled":0,"created":"2020-01-02 03:04:05","custid":7,"password":"PASSWORD-EXCLUDED","passwordHash":"HASH-EXCLUDED","prefs":{"secret":"PREFS-EXCLUDED"},"extra_attributes":{"token":"EXTRA-EXCLUDED"}},
{"id":"2","name":"Disabled","email":"member@EXAMPLE.test","disabled":"1","created_at":"2020-01-02T03:04:05Z","totp_secret":"TOTP-EXCLUDED"},
{"id":3,"name":"Unknown state","email":null,"disabled":"UNKNOWN-STATE","created_at":null}],
"customer_to_users":[
{"id":10,"user_id":9007199254740993,"customer_id":7,"privs":1,"created_at":"2020-01-02T03:04:05Z","last_login_from":"IP-EXCLUDED"},
{"id":11,"user_id":9007199254740993,"customer_id":8,"privs":"2","created_at":"2020-01-02T03:04:05Z"},
{"id":12,"user_id":2,"customer_id":8,"privs":3,"created_at":"2020-01-02T03:04:05Z"},
{"id":13,"user_id":404,"customer_id":8,"privs":"UNKNOWN-PRIVILEGE","created_at":null}],
"user2fa":[{"secret":"MFA-EXCLUDED","recovery_codes":"RECOVERY-EXCLUDED"}],
"user_remember_tokens":[{"token":"REMEMBER-EXCLUDED"}],"api_keys":[{"apiKey":"API-EXCLUDED"}],
"cust":[{"id":7,"name":"ORG-LABEL-EXCLUDED"}]}}`

func TestAccountArchiveOriginalTablesAndSecretIsolation(t *testing.T) {
	a, err := buildAccountArchive(decodeSample(t, accountArchiveFixture), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := migrate.EncodeAccountArchive(*a)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PASSWORD-EXCLUDED", "HASH-EXCLUDED", "PREFS-EXCLUDED", "EXTRA-EXCLUDED", "TOTP-EXCLUDED", "IP-EXCLUDED", "MFA-EXCLUDED", "RECOVERY-EXCLUDED", "REMEMBER-EXCLUDED", "API-EXCLUDED", "ORG-LABEL-EXCLUDED"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("unselected source data leaked", secret)
		}
	}
	if len(a.Users) != 3 || len(a.Memberships) != 4 {
		t.Fatal("rows dropped/merged")
	}
	u := a.Users[2]
	if u.LegacyID != "9007199254740993" || string(u.Facts["id"]) != `"9007199254740993"` || string(u.Facts["custid"]) != `"7"` {
		t.Fatal("large identity/default member rounded", u.LegacyID)
	}
	if a.Memberships[0].SourceUserID != u.LegacyID || string(a.Memberships[0].Facts["user_id"]) != `"9007199254740993"` {
		t.Fatal("relationship identity rounded")
	}
	if migrate.AccountArchivePrivilege(a.Memberships[0]) != "member" || migrate.AccountArchivePrivilege(a.Memberships[1]) != "member-admin" || migrate.AccountArchivePrivilege(a.Memberships[2]) != "superuser" {
		t.Fatal("cross-organization source role collapsed")
	}
	if migrate.AccountArchiveUserState(a.Users[0]) != "disabled" || migrate.AccountArchiveUserState(a.Users[1]) != "unknown" {
		t.Fatal("source state activated")
	}
	if !bytes.Contains(raw, []byte("UNKNOWN-STATE")) || !bytes.Contains(raw, []byte("UNKNOWN-PRIVILEGE")) {
		t.Fatal("authorized source evidence lost")
	}
	findings, _ := json.Marshal(migrate.AccountArchiveReview(a))
	if bytes.Contains(findings, []byte("UNKNOWN-")) || bytes.Contains(findings, []byte("example.test")) {
		t.Fatal("review diagnostics exposed source values")
	}
	// The ordinary inventory path still never makes current accounts.
	ordinary, err := convert(decodeSample(t, accountArchiveFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Resources["users"]) != 0 {
		t.Fatal("source became current accounts")
	}
	warnings := strings.Join(ordinary.Warnings, "\n")
	if !strings.Contains(warnings, "table user:") || !strings.Contains(warnings, "table customer_to_users:") {
		t.Fatal("ordinary inventory silently dropped account migration")
	}
}

func TestAccountArchiveRejectsAmbiguousSourceWithoutEcho(t *testing.T) {
	for name, source := range map[string]string{
		"missing-memberships":     `{"tables":{"user":[{"id":1}]}}`,
		"missing-users":           `{"tables":{"customer_to_users":[]}}`,
		"null-table":              `{"tables":{"user":null,"customer_to_users":[]}}`,
		"empty":                   `{"tables":{"user":[],"customer_to_users":[]}}`,
		"alias":                   `{"tables":{"user":[{"id":1}],"customer_to_users":[],"users":[]}}`,
		"duplicate-id":            `{"tables":{"user":[{"id":1},{"id":"1"}],"customer_to_users":[]}}`,
		"exponential-id":          `{"tables":{"user":[{"id":1e3}],"customer_to_users":[]}}`,
		"boolean-id":              `{"tables":{"user":[{"id":true}],"customer_to_users":[]}}`,
		"leading-zero-id":         `{"tables":{"user":[{"id":"01"}],"customer_to_users":[]}}`,
		"missing-reference":       `{"tables":{"user":[],"customer_to_users":[{"id":1,"user_id":null,"customer_id":2}]}}`,
		"duplicate-membership-id": `{"tables":{"user":[],"customer_to_users":[{"id":1,"user_id":2,"customer_id":3},{"id":1,"user_id":2,"customer_id":4}]}}`,
		"complex-name":            `{"tables":{"user":[{"id":1,"name":{"secret":"MUST-NOT-ECHO"}}],"customer_to_users":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildAccountArchive(decodeSample(t, source), "UTC")
			if err == nil || strings.Contains(err.Error(), "MUST-NOT-ECHO") {
				t.Fatal("ambiguous source accepted or echoed", err)
			}
		})
	}
	for _, source := range []string{
		`{"Tables":{"user":[{"id":1}],"customer_to_users":[]}}`,
		`{"tables":{"user":[{"id":1,"id":2}],"customer_to_users":[]}}`,
		`{"tables":{"user":[{"id":1,"name":"\ud800"}],"customer_to_users":[]}}`,
		accountArchiveFixture + `{}`,
	} {
		var out, diagnostics bytes.Buffer
		if err := run([]string{"--account-archive", "--output", filepath.Join(t.TempDir(), "bad.json")}, strings.NewReader(source), &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatal("strict source reader bypassed")
		}
	}
}

func TestAccountArchiveMissingEvidenceRemainsReviewable(t *testing.T) {
	a, err := buildAccountArchive(decodeSample(t, `{"tables":{"user":[{"id":1,"custid":9}],"customer_to_users":[]}}`), "unknown")
	if err != nil || len(a.Users) != 1 || len(a.Memberships) != 0 {
		t.Fatal("missing evidence rejected or invented relationship", err)
	}
	findings := migrate.AccountArchiveReview(a)
	if len(findings) != 6 {
		t.Fatalf("missing source evidence not reported: %+v", findings)
	}
	if _, known := migrate.AccountArchiveCreation(a.Users[0].Facts, a.SourceTimezone); known {
		t.Fatal("invented source creation")
	}
}

func TestAccountArchiveCLIPrivateAndIndependent(t *testing.T) {
	var out, diagnostics bytes.Buffer
	for _, flags := range [][]string{
		{"--account-archive"}, {"--account-archive", "--strict"}, {"--account-archive", "--mailing-archive"}, {"--account-archive", "--resources-only"},
		{"--account-archive", "--note-archive"}, {"--account-archive", "--audit-archive"}, {"--account-archive", "--peer-drafts", "--peer-limit-schema", "shared", "--peer-as-of", "2026-11-18"},
	} {
		if err := run(flags, strings.NewReader(accountArchiveFixture), &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatal("private output/mode bypassed", flags)
		}
	}
	output := filepath.Join(t.TempDir(), "accounts.json")
	args := []string{"--account-archive", "--input", "-", "--output", output, "--source-timezone", "UTC"}
	diagnostics.Reset()
	if err := run(args, strings.NewReader(accountArchiveFixture), &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 || out.Len() != 0 {
		t.Fatal("private artifact exposed")
	}
	raw, _ := os.ReadFile(output)
	if _, err := migrate.ReadAccountArchive(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "state-disabled") || !strings.Contains(diagnostics.String(), "email-collision") || strings.Contains(diagnostics.String(), "example.test") || strings.Contains(diagnostics.String(), "UNKNOWN-STATE") {
		t.Fatal("review diagnostics missing/redaction failure")
	}
	if err := run(args, strings.NewReader(accountArchiveFixture), &out, &diagnostics); err == nil {
		t.Fatal("overwrote archive")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(raw, after) {
		t.Fatal("archive changed")
	}
}
