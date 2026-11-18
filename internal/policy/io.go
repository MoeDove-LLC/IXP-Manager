package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxSnapshotBytes = 128 << 20

// ParseROAExport imports VRPs from a trusted, independently validating cache
// (e.g. Routinator JSON). This is NOT certificate-chain/ROA signature validation.
// Generation metadata is mandatory so fetching an old document does not renew
// its validity merely because HTTP retrieval happened now.
func ParseROAExport(reader io.Reader, source string, ttl time.Duration, now time.Time) (ROASnapshot, error) {
	var out ROASnapshot
	if ttl <= 0 || ttl > 24*time.Hour {
		return out, errors.New("TTL must be positive and at most 24 hours")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxSnapshotBytes+1))
	if err != nil {
		return out, err
	}
	if len(data) > MaxSnapshotBytes {
		return out, errors.New("ROA export exceeds 128 MiB")
	}
	var raw struct {
		Metadata struct {
			Generated     json.Number `json:"generated"`
			GeneratedTime string      `json:"generatedTime"`
		} `json:"metadata"`
		ROAs *[]struct {
			Prefix    string          `json:"prefix"`
			MaxLength *int            `json:"maxLength"`
			ASN       json.RawMessage `json:"asn"`
		} `json:"roas"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return out, err
	}
	if raw.ROAs == nil {
		return out, errors.New("validated export must contain a roas array")
	}
	generated := time.Time{}
	if raw.Metadata.Generated != "" {
		seconds, e := raw.Metadata.Generated.Int64()
		if e != nil {
			return out, errors.New("invalid generation timestamp")
		}
		generated = time.Unix(seconds, 0).UTC()
	}
	if raw.Metadata.GeneratedTime != "" {
		timestamp, e := time.Parse(time.RFC3339, raw.Metadata.GeneratedTime)
		if e != nil {
			return out, e
		}
		if !generated.IsZero() && generated.Unix() != timestamp.Unix() {
			return out, errors.New("generation timestamps disagree")
		}
		generated = timestamp
	}
	if generated.IsZero() || generated.After(now.Add(5*time.Minute)) || !now.Before(generated.Add(ttl)) {
		return out, errors.New("upstream validated export is stale or lacks trustworthy generation metadata")
	}
	out = ROASnapshot{Version: 1, FetchedAt: now.UTC(), ExpiresAt: generated.Add(ttl).UTC(), Source: source, ROAs: []ROA{}}
	if out.ExpiresAt.After(now.Add(ttl)) {
		out.ExpiresAt = now.Add(ttl)
	}
	if len(*raw.ROAs) > MaxROAs {
		return ROASnapshot{}, errors.New("ROA export exceeds record limit")
	}
	for _, record := range *raw.ROAs {
		p, e := prefix(record.Prefix)
		if e != nil {
			return ROASnapshot{}, e
		}
		token := string(record.ASN)
		if strings.HasPrefix(token, `"`) {
			if e = json.Unmarshal(record.ASN, &token); e != nil {
				return ROASnapshot{}, e
			}
			token = strings.TrimPrefix(strings.ToUpper(token), "AS")
		}
		n, e := strconv.ParseUint(token, 10, 32)
		if e != nil {
			return ROASnapshot{}, errors.New("ROA ASN must be an integer or AS<number>")
		}
		length := p.Bits()
		if record.MaxLength != nil {
			length = *record.MaxLength
		}
		out.ROAs = append(out.ROAs, ROA{Prefix: p.String(), MaxLength: length, ASN: uint32(n)})
	}
	if _, err = NewROAIndex(out, now); err != nil {
		return ROASnapshot{}, err
	}
	return out, nil
}
func FetchROAs(ctx context.Context, endpoint string, ttl time.Duration, allowHTTP bool) (ROASnapshot, error) {
	var empty ROASnapshot
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return empty, errors.New("explicit HTTPS export URL without embedded credentials/query required; HTTP needs explicit operator opt-in")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("ROA export redirects are disabled; specify the final trusted URL")
	}}
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return empty, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return empty, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return empty, fmt.Errorf("ROA source returned HTTP %d", response.StatusCode)
	}
	return ParseROAExport(response.Body, u.String(), ttl, time.Now().UTC())
}

// WriteSnapshot atomically publishes private JSON; it never writes router
// configuration, modifies approved prefixes, or advances deployment state.
func WriteSnapshot(path string, value any) error {
	if path == "" || path == "-" {
		return errors.New("snapshot output must be a filesystem path")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > MaxSnapshotBytes {
		return errors.New("snapshot exceeds 128 MiB")
	}
	data = append(data, '\n')
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return errors.New("snapshot output is not a regular file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".policy-snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func ReadJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxSnapshotBytes {
		return errors.New("JSON input exceeds 128 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return errors.New("one JSON document required")
	}
	return nil
}
