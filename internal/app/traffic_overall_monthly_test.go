package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func monthlyOverallTestCapture(capturedAt time.Time, samples [][]any) []byte {
	data, _ := json.Marshal(Record{"version": 1, "source": "ixp001", "period": "year", "unit": "bits/s", "timezone": "UTC", "capturedAt": capturedAt, "samples": samples})
	return data
}

func TestMonthlyOverallTrafficUsesCapturedMaximaAndLeavesMissingMonthsOut(t *testing.T) {
	now := time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)
	epoch := func(y int, m time.Month, day, hour int) int64 {
		return time.Date(y, m, day, hour, 0, 0, 0, time.UTC).Unix()
	}
	const exactLarge = uint64(9007199254740993)
	data := monthlyOverallTestCapture(now.Add(24*time.Hour), [][]any{
		{epoch(2024, 2, 29, 13), 0, 0, 900, 950}, // future at request time
		{epoch(2024, 2, 29, 11), 30, 35, 80, 90},
		{epoch(2023, 8, 31, 23), 30, 35, 200, 300}, // older than six months
		{epoch(2023, 9, 1, 0), 0, 0, 0, nil},       // observed zero is a row
		{epoch(2023, 10, 25, 11), 44, 55, nil, nil},
		{epoch(2023, 12, 22, 11), nil, nil, exactLarge - 1, exactLarge},
		{epoch(2023, 12, 23, 11), nil, nil, uint64(600), nil},
	})
	feed, err := decodeMonthlyOverallTraffic(data)
	if err != nil {
		t.Fatal(err)
	}
	rows := feed.byMonth(now)
	if len(rows) != 3 || rows[0].Start != "2023-09-01T00:00:00Z" || rows[0].Max != 0 ||
		rows[1].Start != "2023-12-01T00:00:00Z" || rows[1].Max != exactLarge || rows[2].Max != 90 || rows[2].End != "2024-02-29T23:59:59Z" ||
		rows[2].EndTS != epoch(2024, 3, 1, 0)-1 || rows[2].StartTS != epoch(2024, 2, 1, 0) {
		t.Fatal("wrong monthly period, private source data, or fabricated/rounded rate", rows)
	}
	encoded, err := json.Marshal(rows[1])
	if err != nil || !strings.Contains(string(encoded), `"max":9007199254740993`) {
		t.Fatal("exact numeric API compatibility lost", err)
	}
	if got := feed.byMonth(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)); len(got) != 2 {
		t.Fatal("a month beginning exactly at the request boundary was emitted", got)
	}
}

func TestMonthlyOverallTrafficCaptureValidationAndExactReview(t *testing.T) {
	capture := time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)
	past := capture.Add(-time.Hour).Unix()
	base := monthlyOverallTestCapture(capture, [][]any{{past, nil, nil, 0, nil}})
	tests := map[string][]byte{
		"wrong source":        []byte(strings.Replace(string(base), `"source":"ixp001"`, `"source":"member-5"`, 1)),
		"wrong units":         []byte(strings.Replace(string(base), "bits/s", "bytes/s", 1)),
		"wrong timeframe":     []byte(strings.Replace(string(base), `"period":"year"`, `"period":"month"`, 1)),
		"non-UTC calendar":    []byte(strings.Replace(string(base), `"timezone":"UTC"`, `"timezone":"Europe/Dublin"`, 1)),
		"same source twice":   append([]byte(`{"source":"ixp001",`), base[1:]...),
		"case alias":          []byte(strings.Replace(string(base), `"version"`, `"Version"`, 1)),
		"unexpected field":    append([]byte(`{"inventoryId":"member-5",`), base[1:]...),
		"record is too short": monthlyOverallTestCapture(capture, [][]any{{past, nil, nil, 0}}),
		"extra record column": monthlyOverallTestCapture(capture, [][]any{{past, nil, nil, 0, 1, 2}}),
		"future record":       monthlyOverallTestCapture(capture, [][]any{{capture.Unix() + 1, 9, 10, 11, 12}}),
		"duplicated epoch":    monthlyOverallTestCapture(capture, [][]any{{past, nil, nil, 0, nil}, {past, 0, 0, 1, 1}}),
		"negative maximum":    monthlyOverallTestCapture(capture, [][]any{{past, 0, 0, -1, 0}}),
		"fractional maximum":  monthlyOverallTestCapture(capture, [][]any{{past, 0, 0, 1.1, 0}}),
		"string maximum":      monthlyOverallTestCapture(capture, [][]any{{past, 0, 0, "1", 0}}),
		"multiple documents":  append(append([]byte{}, base...), []byte(` {}`)...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeMonthlyOverallTraffic(raw); err == nil {
				t.Fatal("unverified capture accepted")
			}
		})
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "reviewed-capture.json")
	if err := os.WriteFile(path, base, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMonthlyOverallTraffic(path, strings.Repeat("0", 64), true); err == nil {
		t.Fatal("unreviewed SHA published")
	}
	if loaded, err := loadMonthlyOverallTraffic(path, digest(string(base)), true); err != nil || !loaded.published {
		t.Fatal("reviewed snapshot did not load", err)
	}
	alias := filepath.Join(dir, "symlink.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMonthlyOverallTraffic(alias, digest(string(base)), true); err == nil {
		t.Fatal("symlink to approved bytes accepted")
	}
	if loaded, err := loadMonthlyOverallTraffic("", "", false); err != nil || loaded != nil {
		t.Fatal("unchecked default source published", err)
	}
	if _, err := loadMonthlyOverallTraffic("", "", true); err == nil {
		t.Fatal("public feed enabled with no source evidence")
	}
}

func TestMonthlyOverallTrafficSettingsPermissionsAndLegacyPaths(t *testing.T) {
	c, _ := setupClient(t)
	anon := &client{a: c.a, t: t}
	const private = "/api/v4/statistics/overall-by-month"
	const public = "/api/v4/public/statistics/overall-by-month"
	if w := anon.request(http.MethodGet, public, nil); w.Code != http.StatusNotFound {
		t.Fatal("default public statistics feed not disabled", w.Code)
	}
	if w := c.request(http.MethodGet, private, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatal("no source manufactured statistics", w.Code)
	}
	now := time.Now().UTC()
	const exactLarge = uint64(9007199254740993)
	var err error
	c.a.monthlyOverallTraffic, err = decodeMonthlyOverallTraffic(monthlyOverallTestCapture(now, [][]any{{now.Add(-time.Hour).Unix(), 0, 0, exactLarge, 1}}))
	if err != nil {
		t.Fatal(err)
	}
	if w := anon.request(http.MethodGet, private, nil); w.Code != http.StatusUnauthorized {
		t.Fatal("private aggregate capture accessible anonymously", w.Code)
	}
	if w := anon.request(http.MethodGet, public, nil); w.Code != http.StatusNotFound {
		t.Fatal("source approval accidentally granted public access", w.Code)
	}
	staff := c.request(http.MethodGet, private, nil)
	var rows []map[string]json.RawMessage
	if staff.Code != http.StatusOK || json.Unmarshal(staff.Body.Bytes(), &rows) != nil || len(rows) != 1 || len(rows[0]) != 5 || string(rows[0]["max"]) != fmt.Sprint(exactLarge) {
		t.Fatal("legacy JSON wire fields or exact max not retained", staff.Code, staff.Body.String())
	}
	member := c.create("members", Record{"name": "Fixture member", "asn": 64511, "status": "active"})
	c.create("users", Record{"name": "Fixture member", "role": "member", "memberId": str(member, "id"), "email": "monthly-member@example.test", "password": "member-yearly-request"})
	asMember := loginClient(t, c.a, Record{"email": "monthly-member@example.test", "password": "member-yearly-request"})
	if w := asMember.request(http.MethodGet, private, nil); w.Code != http.StatusForbidden {
		t.Fatal("tenant gained access to unpublished aggregate capture", w.Code)
	}
	token := str(c.ok(http.MethodPost, "/api/account/tokens", Record{"name": "overall read", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read"}}), "token")
	if w := bearerRequest(c.a, token, http.MethodGet, private, nil); w.Code != http.StatusOK || w.Body.String() != staff.Body.String() {
		t.Fatal("scoped staff API did not retain legacy monthly feed", w.Code)
	}
	writeToken := str(c.ok(http.MethodPost, "/api/account/tokens", Record{"name": "overall wrong scope", "currentPassword": "very-long-test-password", "scopes": []string{"resources:write"}}), "token")
	if w := bearerRequest(c.a, writeToken, http.MethodGet, private, nil); w.Code != http.StatusForbidden {
		t.Fatal("staff token without read scope opened capture", w.Code)
	}
	c.a.monthlyOverallTraffic.published = true
	for _, path := range []string{private, public} {
		w := anon.request(http.MethodGet, path+"?memberId=bogus&routerId=bogus", nil)
		if w.Code != http.StatusOK || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" || w.Body.String() != staff.Body.String() {
			t.Fatal("public graph was not the approved aggregate or varied with inventory parameters", w.Code)
		}
		if rejected := anon.request(http.MethodPost, path, nil); rejected.Code != http.StatusMethodNotAllowed || rejected.Header().Get("Allow") != "GET" {
			t.Fatal("public graph accepted an unrecognized method", rejected.Code)
		}
	}
}
