package migrate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMailingArchiveConsentNeverUsesPHPTruthiness(t *testing.T) {
	for _, test := range []struct{ value, desired string }{{`"1"`, "subscribed"}, {`1`, "subscribed"}, {`"0"`, "unsubscribed"}, {`0`, "unsubscribed"}, {`true`, "unknown"}, {`false`, "unknown"}, {`null`, "unknown"}, {`"yes"`, "unknown"}, {`"01"`, "unknown"}, {`" 1 "`, "unknown"}, {`1.0`, "unknown"}, {`1e0`, "unknown"}, {`-0`, "unknown"}, {`[]`, "unknown"}, {`{}`, "unknown"}} {
		if got := MailingArchiveDesired(json.RawMessage(test.value)); got != test.desired {
			t.Fatal("source scalar was coerced", test, got)
		}
	}
}
func TestMailingArchiveStrictEnvelopeAndSourceBounds(t *testing.T) {
	archive := MailingArchive{Version: 1, Kind: MailingArchiveKind, Source: "ixp-manager-legacy", Access: "admin", Users: []MailingArchiveUser{{LegacyID: "1", Facts: NoteArchiveFacts{"id": json.RawMessage("1")}}}, Choices: []MailingArchiveChoice{{ID: "user-1-members", SourceUserID: "1", SourceKey: "members", Format: "user.prefs", Value: json.RawMessage(`"0"`)}}}
	raw, err := EncodeMailingArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), []byte(`{}`)...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), bytes.Replace(raw, []byte(`"access":"admin"`), []byte(`"access":"admin","password":"hidden"`), 1)} {
		if _, err := ReadMailingArchive(bytes.NewReader(bad)); err == nil {
			t.Fatal("ambiguous or unrelated archive fields accepted")
		}
	}
	for _, value := range []json.RawMessage{json.RawMessage(`"` + strings.Repeat("x", 4097) + `"`), json.RawMessage(`{"password":"hidden"}`), json.RawMessage(`"\ud800"`)} {
		archive.Choices[0].Value = value
		if _, err := EncodeMailingArchive(archive); err == nil {
			t.Fatal("invalid or oversized selected value accepted")
		}
	}
	archive.Choices[0].Value = json.RawMessage(`"0"`)
	archive.Users[0].LegacyID = "9007199254740993"
	archive.Users[0].Facts["id"] = json.RawMessage("9007199254740993")
	archive.Choices[0].SourceUserID = "9007199254740993"
	archive.Choices[0].ID = "user-9007199254740993-members"
	raw, err = EncodeMailingArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := ReadMailingArchive(bytes.NewReader(raw))
	if err != nil || copy.Users[0].LegacyID != "9007199254740993" {
		t.Fatal("source account precision lost", err)
	}
}
