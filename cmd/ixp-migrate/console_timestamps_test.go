package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
)

func consoleTimestampRecord(t *testing.T, result export, table string) row {
	t.Helper()
	kind := "console-servers"
	if table == "consoleserverconnection" {
		kind = "console-connections"
	}
	if len(result.Resources[kind]) != 1 {
		t.Fatal("console resource missing")
	}
	return result.Resources[kind][0]
}
func TestConsoleTimestampCurrentAndAdaptedAliasesPreserveExactText(t *testing.T) {
	for _, table := range []string{"console_server", "consoleserverconnection"} {
		for _, fields := range [][2]string{{"created_at", "updated_at"}, {"created", "updated"}, {"created_at", "updated"}, {"created", "updated_at"}} {
			t.Run(table+"/"+fields[0]+"/"+fields[1], func(t *testing.T) {
				s := consoleFixture(t)
				source := s.Tables[table][0]
				source[fields[0]] = "1999-12-31 23:59:59.123456"
				source[fields[1]] = "2001-02-03T04:05:06.123456789+08:00"
				before, _ := json.Marshal(s)
				result, err := convert(s)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(s)
				if !bytes.Equal(before, after) {
					t.Fatal("source timestamps mutated")
				}
				r := consoleTimestampRecord(t, result, table)
				want := row{"sourceId": "1", "createdAt": source[fields[0]], "updatedAt": source[fields[1]]}
				if !reflect.DeepEqual(r["legacyTimestamps"], want) || r["createdAt"] != nil || r["updatedAt"] != nil || len(result.Warnings) != 0 {
					t.Fatal("source timestamps changed, discarded or became current lineage", r["legacyTimestamps"], result.Warnings)
				}
				var output, diagnostics bytes.Buffer
				if err = run([]string{"--strict", "--source-timezone", "UTC"}, bytes.NewReader(before), &output, &diagnostics); err != nil || diagnostics.Len() != 0 {
					t.Fatal("strict exact source conversion failed", err, diagnostics.String())
				}
				if !bytes.Contains(output.Bytes(), []byte("1999-12-31 23:59:59.123456")) {
					t.Fatal("timezone option invented UTC or lost source precision")
				}
			})
		}
	}
}

func TestConsoleTimestampMissingNullAndZeroRemainDistinct(t *testing.T) {
	for _, table := range []string{"console_server", "consoleserverconnection"} {
		for name, fields := range map[string]row{
			"oldest-no-columns": {},
			"created-null":      {"created_at": nil},
			"updated-null":      {"updated": nil},
			"both-null":         {"created_at": nil, "updated_at": nil},
			"zero-date":         {"created_at": "0000-00-00 00:00:00", "updated_at": "0000-00-00 00:00:00.000000"},
			"identical-aliases": {"created_at": "2000-01-01 00:00:00", "created": "2000-01-01 00:00:00", "updated_at": nil, "updated": nil},
		} {
			t.Run(table+"/"+name, func(t *testing.T) {
				s := consoleFixture(t)
				for key, value := range fields {
					s.Tables[table][0][key] = value
				}
				result, err := convert(s)
				if err != nil {
					t.Fatal(err)
				}
				r := consoleTimestampRecord(t, result, table)
				if len(fields) == 0 {
					if _, present := r["legacyTimestamps"]; present {
						t.Fatal("missing source columns invented source date metadata")
					}
					return
				}
				metadata, ok := r["legacyTimestamps"].(row)
				if !ok || metadata["sourceId"] != "1" {
					t.Fatal("missing source identity")
				}
				for _, pair := range [][2]string{{"created_at", "createdAt"}, {"created", "createdAt"}, {"updated_at", "updatedAt"}, {"updated", "updatedAt"}} {
					if value, present := fields[pair[0]]; present {
						got, exists := metadata[pair[1]]
						if !exists || got != value {
							t.Fatal("source null/zero/string altered", pair[0])
						}
					}
				}
				if name == "created-null" {
					if _, found := metadata["updatedAt"]; found {
						t.Fatal("missing update became explicit null")
					}
				}
				if name == "updated-null" {
					if _, found := metadata["createdAt"]; found {
						t.Fatal("missing creation became explicit null")
					}
				}
			})
		}
	}
}

func TestConsoleTimestampInvalidOrConflictingSourcesNeverWriteOutput(t *testing.T) {
	invalid := []any{"", "2000-02-30 12:00:00", "2000-01-01", "0000-00-00 00:00:00.000001", "2000-01-01 00:00:00.1234567", "2000-01-01T00:00:00", "2000-01-01T00:00:00.1234567891Z", "SECRET-INVALID-SOURCE", true, json.Number("0"), map[string]any{"secret": "SECRET-INVALID-SOURCE"}}
	for _, table := range []string{"console_server", "consoleserverconnection"} {
		for _, field := range []string{"created_at", "created", "updated_at", "updated"} {
			for _, value := range invalid {
				s := consoleFixture(t)
				s.Tables[table][0][field] = value
				raw, _ := json.Marshal(s)
				for _, strict := range []bool{false, true} {
					var output, diagnostics bytes.Buffer
					path := filepath.Join(t.TempDir(), "invalid.json")
					args := []string{"--output", path}
					if strict {
						args = append(args, "--strict")
					}
					err := run(args, bytes.NewReader(raw), &output, &diagnostics)
					if err == nil || output.Len() != 0 {
						t.Fatal("invalid source timestamp accepted", table, field, strict)
					}
					if !strings.Contains(err.Error(), table+" id 1") || !strings.Contains(err.Error(), field) {
						t.Fatal("timestamp diagnostic cannot identify correction", err)
					}
					if strings.Contains(err.Error()+diagnostics.String(), "SECRET-INVALID-SOURCE") {
						t.Fatal("timestamp diagnostic echoed raw source")
					}
					if _, e := os.Stat(path); !os.IsNotExist(e) {
						t.Fatal("invalid timestamp published an import artifact")
					}
				}
			}
		}
		for _, pair := range [][2]string{{"created_at", "created"}, {"updated_at", "updated"}} {
			for _, values := range [][2]any{{"2000-01-01 00:00:00", "2001-01-01 00:00:00"}, {nil, "2001-01-01 00:00:00"}, {"2000-01-01T00:00:00Z", "2000-01-01T08:00:00+08:00"}} {
				s := consoleFixture(t)
				s.Tables[table][0][pair[0]], s.Tables[table][0][pair[1]] = values[0], values[1]
				if _, err := convert(s); err == nil || !strings.Contains(err.Error(), "conflicting "+pair[0]+" and "+pair[1]) {
					t.Fatal("conflicting aliases silently prioritized", err)
				}
			}
		}
	}
}

func TestConsoleTimestampDoesNotInferUndocumentedDateFields(t *testing.T) {
	s := consoleFixture(t)
	for _, table := range []string{"console_server", "consoleserverconnection"} {
		for _, field := range []string{"lastupdated", "last_updated", "firstseen", "lastseen"} {
			s.Tables[table][0][field] = "2000-01-01 00:00:00"
		}
	}
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"console_server", "consoleserverconnection"} {
		if _, present := consoleTimestampRecord(t, result, table)["legacyTimestamps"]; present {
			t.Fatal("undocumented date field became a source timestamp")
		}
	}
	warnings := strings.Join(result.Warnings, "\n")
	for _, field := range []string{"lastupdated", "last_updated", "firstseen", "lastseen"} {
		if !strings.Contains(warnings, field) {
			t.Fatal("unknown timestamp silently consumed", field)
		}
	}
	raw, _ := json.Marshal(s)
	var output, diagnostics bytes.Buffer
	if err = run([]string{"--strict"}, bytes.NewReader(raw), &output, &diagnostics); err == nil || output.Len() != 0 {
		t.Fatal("strict accepted unmapped timestamp aliases")
	}
}

func TestConsoleTimestampLargeSourceIDsAndRealImportPreserveNewLineage(t *testing.T) {
	s := consoleFixture(t)
	s.Tables["console_server"][0]["id"] = json.Number("9007199254740993")
	s.Tables["consoleserverconnection"][0]["id"] = json.Number("9007199254740995")
	s.Tables["consoleserverconnection"][0]["console_server_id"] = json.Number("9007199254740993")
	s.Tables["console_server"][0]["created_at"] = "1999-12-31 23:59:59"
	s.Tables["console_server"][0]["updated_at"] = nil
	s.Tables["consoleserverconnection"][0]["created"] = "0000-00-00 00:00:00"
	s.Tables["consoleserverconnection"][0]["updated"] = "2001-01-01T01:02:03.123456Z"
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	if consoleTimestampRecord(t, result, "console_server")["legacyTimestamps"].(row)["sourceId"] != "9007199254740993" || consoleTimestampRecord(t, result, "consoleserverconnection")["legacyTimestamps"].(row)["sourceId"] != "9007199254740995" {
		t.Fatal("source identity rounded")
	}
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &migrationDNSClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Console time import", "email": "console-time@example.test", "password": "console-time-import-password", "ixpName": "Console time IXP", "asn": 64599, "setupToken": a.BootstrapToken()})
	raw, _ := json.Marshal(result)
	payload := app.Record{}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/import", payload)
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state app.State
	if err = json.Unmarshal(stored, &state); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"console-servers", "console-connections"} {
		current := state.Resources[kind][0]
		metadata, ok := current["legacyTimestamps"].(map[string]any)
		if !ok {
			t.Fatal("source timestamps lost on real import")
		}
		before, _ := json.Marshal(result.Resources[kind][0]["legacyTimestamps"])
		after, _ := json.Marshal(metadata)
		if !bytes.Equal(before, after) {
			t.Fatal("source evidence changed through import")
		}
		created, _ := current["createdAt"].(string)
		if created == "" || strings.HasPrefix(created, "1999-") || strings.HasPrefix(created, "0000-") {
			t.Fatal("old source date replaced current entity creation")
		}
	}
}
