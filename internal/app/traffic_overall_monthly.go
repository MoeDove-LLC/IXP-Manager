package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const monthlyOverallTrafficLimit = 16 << 20

type monthlyOverallTrafficSample struct {
	at   int64
	peak uint64
}

// This feed is an exact, externally reviewed capture of the legacy overall
// graph. It is not aggregated from current inventory or per-port counters.
type monthlyOverallTrafficFeed struct {
	published bool
	samples   []monthlyOverallTrafficSample
}

type monthlyOverallTrafficRow struct {
	Start   string `json:"start"`
	StartTS int64  `json:"startTs"`
	End     string `json:"end"`
	EndTS   int64  `json:"endTs"`
	Max     uint64 `json:"max"`
}

func monthlyOverallTrafficRoute(path string) (publicAlias, matched bool) {
	switch path {
	case "/api/v4/statistics/overall-by-month":
		return false, true
	case "/api/v4/public/statistics/overall-by-month":
		return true, true
	}
	return false, false
}

func loadMonthlyOverallTraffic(path, expectedSHA string, publish bool) (*monthlyOverallTrafficFeed, error) {
	if path == "" && expectedSHA == "" && !publish {
		return nil, nil
	}
	if !externalDigest(expectedSHA) || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("monthly overall traffic requires a clean absolute snapshot path and reviewed SHA-256")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("monthly overall traffic snapshot path is unavailable or has a symlink")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("monthly overall traffic snapshot is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > monthlyOverallTrafficLimit {
		return nil, errors.New("monthly overall traffic snapshot must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, monthlyOverallTrafficLimit+1))
	after, inspect := file.Stat()
	if err != nil || inspect != nil || int64(len(data)) != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || digest(string(data)) != expectedSHA {
		return nil, errors.New("monthly overall traffic snapshot does not match the reviewed SHA-256")
	}
	feed, err := decodeMonthlyOverallTraffic(data)
	if err != nil {
		return nil, err
	}
	feed.published = publish
	return feed, nil
}

func decodeMonthlyOverallTraffic(data []byte) (*monthlyOverallTrafficFeed, error) {
	if len(data) < 1 || len(data) > monthlyOverallTrafficLimit {
		return nil, errors.New("monthly overall capture exceeds the supported bounds")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if start, err := d.Token(); err != nil || start != json.Delim('{') {
		return nil, errors.New("monthly overall capture must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, errors.New("invalid or duplicate monthly overall capture field")
		}
		switch name {
		case "version", "source", "period", "unit", "timezone", "capturedAt", "samples":
			var value json.RawMessage
			if err := d.Decode(&value); err != nil {
				return nil, errors.New("invalid monthly overall capture value")
			}
			fields[name] = value
		default:
			return nil, errors.New("unknown field in the monthly overall capture")
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF || len(fields) != 7 {
		return nil, errors.New("monthly overall capture requires one complete source declaration")
	}
	if string(fields["version"]) != "1" || !monthlyCaptureString(fields["source"], "ixp001") || !monthlyCaptureString(fields["period"], "year") ||
		!monthlyCaptureString(fields["unit"], "bits/s") || !monthlyCaptureString(fields["timezone"], "UTC") {
		return nil, errors.New("monthly overall capture must declare the legacy ixp001 yearly bits/s graph and UTC boundaries")
	}
	var at time.Time
	if err := json.Unmarshal(fields["capturedAt"], &at); err != nil || at.Unix() < 1 || at.Year() > 2199 {
		return nil, errors.New("monthly overall capture requires a valid RFC3339 capture time")
	}
	return decodeMonthlyOverallSamples(fields["samples"], uint64(at.Unix()))
}

func monthlyCaptureString(raw json.RawMessage, expected string) bool {
	var got string
	return json.Unmarshal(raw, &got) == nil && got == expected
}

func decodeMonthlyOverallSamples(raw json.RawMessage, capturedAt uint64) (*monthlyOverallTrafficFeed, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if start, err := d.Token(); err != nil || start != json.Delim('[') {
		return nil, errors.New("monthly overall capture requires a sample array")
	}
	feed := &monthlyOverallTrafficFeed{}
	seen := map[uint64]bool{}
	for d.More() {
		if len(seen) >= 20000 {
			return nil, errors.New("monthly overall capture exceeds the sample limit")
		}
		var encoded json.RawMessage
		if err := d.Decode(&encoded); err != nil || len(encoded) > 2048 {
			return nil, errors.New("invalid bounded monthly overall sample")
		}
		var row []*uint64
		if err := json.Unmarshal(encoded, &row); err != nil || len(row) != 5 || row[0] == nil || *row[0] == 0 || *row[0] > capturedAt || seen[*row[0]] {
			return nil, errors.New("monthly overall samples require five exact columns and distinct epochs no later than capture time")
		}
		seen[*row[0]] = true
		if row[3] != nil || row[4] != nil {
			var peak uint64
			for _, value := range row[3:] {
				if value != nil && *value > peak {
					peak = *value
				}
			}
			feed.samples = append(feed.samples, monthlyOverallTrafficSample{at: int64(*row[0]), peak: peak})
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim(']') || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("unterminated monthly overall sample array")
	}
	return feed, nil
}

func (f *monthlyOverallTrafficFeed) byMonth(now time.Time) []monthlyOverallTrafficRow {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -5, 0)
	peaks := map[int64]uint64{}
	for _, sample := range f.samples {
		at := time.Unix(sample.at, 0).UTC()
		if at.Before(first) || at.After(now) {
			continue
		}
		key := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
		if old, present := peaks[key]; !present || sample.peak > old {
			peaks[key] = sample.peak
		}
	}
	rows := []monthlyOverallTrafficRow{}
	for start := first; start.Before(now); start = start.AddDate(0, 1, 0) {
		if peak, present := peaks[start.Unix()]; present {
			end := start.AddDate(0, 1, 0).Add(-time.Second)
			rows = append(rows, monthlyOverallTrafficRow{Start: start.Format(time.RFC3339), StartTS: start.Unix(), End: end.Format(time.RFC3339), EndTS: end.Unix(), Max: peak})
		}
	}
	return rows
}

func (a *App) monthlyOverallTrafficPublished() bool {
	return a.monthlyOverallTraffic != nil && a.monthlyOverallTraffic.published
}

func (a *App) serveMonthlyOverallTraffic(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	publicAlias, matched := monthlyOverallTrafficRoute(r.URL.Path)
	published := a.monthlyOverallTrafficPublished()
	if !matched || publicAlias && !published {
		problem(w, 404, "public monthly overall traffic feed unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		problem(w, 405, "method not allowed")
		return
	}
	if !published {
		_, err := a.atlasActorCurrent(user, session, "admin", "operator", "viewer")
		if err != nil || session.API && !contains(session.Scopes, "resources:read") {
			problem(w, 403, "monthly overall traffic requires staff access or explicit public publication")
			return
		}
	}
	if a.monthlyOverallTraffic == nil {
		problem(w, 503, "reviewed monthly overall traffic snapshot not configured")
		return
	}
	respond(w, 200, a.monthlyOverallTraffic.byMonth(time.Now()))
}
