package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyNoteAndTagPrivacyFlagsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    any
		missing  bool
		internal bool
		warning  bool
	}{
		{"zero", json.Number("0"), false, false, false},
		{"one", json.Number("1"), false, true, false},
		{"false", false, false, false, false},
		{"true", true, false, true, false},
		{"string-zero", "0", false, false, false},
		{"string-one", "1", false, true, false},
		{"string-false", "false", false, false, false},
		{"string-true", "true", false, true, false},
		{"missing", nil, true, true, true},
		{"null", nil, false, true, true},
		{"unknown", "unknown", false, true, true},
		{"empty", "", false, true, true},
		{"other-integer", json.Number("2"), false, true, true},
		{"negative", json.Number("-1"), false, true, true},
		{"fraction", json.Number("0.1"), false, true, true},
		{"array", []any{false}, false, true, true},
		{"object", map[string]any{"value": false}, false, true, true},
		{"whitespace", " 0", false, true, true},
		{"wrong-case", "FALSE", false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			note := row{"id": 1, "customer_id": 1, "title": "Source note", "note": "Internal note content"}
			tag := row{"id": 1, "tag": "Source tag"}
			if !test.missing {
				note["private"] = test.value
				tag["internal_only"] = test.value
			}
			raw, err := json.Marshal(legacyExport{Tables: map[string][]row{
				"cust":       {{"id": 1, "name": "Source member", "autsys": 64501, "status": 1}},
				"cust_notes": {note}, "cust_tag": {tag},
				"cust_to_cust_tag": {{"customer_id": 1, "customer_tag_id": 1}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			var output, log bytes.Buffer
			if err = run(nil, bytes.NewReader(raw), &output, &log); err != nil {
				t.Fatal(err)
			}
			var exported export
			if err = json.Unmarshal(output.Bytes(), &exported); err != nil {
				t.Fatal(err)
			}
			if len(exported.Resources["notes"]) != 1 || len(exported.Resources["tags"]) != 1 {
				t.Fatal("privacy fallback discarded records")
			}
			visibility := "member"
			if test.internal {
				visibility = "internal"
			}
			if exported.Resources["notes"][0]["visibility"] != visibility || exported.Resources["tags"][0]["internal"] != test.internal {
				t.Fatal("source privacy was widened")
			}
			if exported.Resources["notes"][0]["body"] != "Internal note content" {
				t.Fatal("privacy fallback changed note")
			}
			if test.warning {
				if !strings.Contains(log.String(), "private is missing or unknown; imported as internal") || !strings.Contains(log.String(), "internal_only is missing or unknown; imported as internal") {
					t.Fatal("missing explicit privacy review warning")
				}
			} else if log.Len() != 0 {
				t.Fatal("known source flag unexpectedly warned", log.String())
			}
			output.Reset()
			log.Reset()
			err = run([]string{"--strict"}, bytes.NewReader(raw), &output, &log)
			if test.warning && (err == nil || output.Len() != 0) {
				t.Fatal("strict conversion accepted ambiguous privacy")
			}
			if !test.warning && err != nil {
				t.Fatal("strict conversion rejected known flag", err)
			}
		})
	}
}
