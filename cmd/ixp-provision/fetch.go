package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

type fetchOptions struct{ Server, Profile, Revision, Kind, Zone, SwitchID, RouterID, Output string }

func runFetch(args []string, input io.Reader, output io.Writer) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(output)
	var o fetchOptions
	fs.StringVar(&o.Server, "server", "", "trusted HTTPS control plane origin")
	fs.StringVar(&o.Profile, "profile", "", "expected provisioning profile ID")
	fs.StringVar(&o.Revision, "revision", "", "exact approved revision ID")
	fs.StringVar(&o.Kind, "kind", "", "expected kind: dns-arpa, nagios, smokeping or switch-core")
	fs.StringVar(&o.Zone, "zone", "", "expected DNS reverse zone, including final dot")
	fs.StringVar(&o.SwitchID, "switch", "", "expected physical switch ID for switch-core plans")
	fs.StringVar(&o.RouterID, "router", "", "expected router ID for as112-dns bundles")
	fs.StringVar(&o.Output, "output", "", "new private file; never overwrites existing paths")
	file := fs.String("token-file", "", "private profile-bound provisioning:pull token file")
	stdin := fs.Bool("token-stdin", false, "read pull token from stdin")
	done, err := parse(fs, args)
	if err != nil || done {
		return err
	}
	token, err := readToken(*file, *stdin, input)
	if err != nil {
		return err
	}
	sum, err := fetch(context.Background(), http.DefaultTransport, o, token)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		File      string `json:"file"`
		SHA256    string `json:"sha256"`
		Activated bool   `json:"activated"`
	}{o.Output, sum, false})
}

func serverURL(server string) (*url.URL, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("--server must be a trusted HTTPS origin without credentials, path, query or fragment")
	}
	return u, nil
}
func get(ctx context.Context, transport http.RoundTripper, server, path, token string, limit int64) ([]byte, http.Header, error) {
	u, err := serverURL(server)
	if err != nil {
		return nil, nil, err
	}
	if !bearerToken.MatchString(token) || len(token) > 256 {
		return nil, nil, errors.New("invalid API token")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u.Path, u.RawPath = path, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, errors.New("could not construct request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, errors.New("HTTPS request failed; check trusted server identity and connectivity")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("server returned HTTP %d; check access scope and current revision", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(raw)) > limit || len(raw) == 0 {
		return nil, nil, errors.New("server response empty, too large or unreadable")
	}
	return raw, res.Header, nil
}

func fetch(ctx context.Context, transport http.RoundTripper, o fetchOptions, token string) (string, error) {
	if !identifier.MatchString(o.Profile) || !identifier.MatchString(o.Revision) || o.Output == "" {
		return "", errors.New("--profile, --revision and --output are required")
	}
	if o.Kind != "dns-arpa" && o.Kind != "nagios" && o.Kind != "smokeping" && o.Kind != "switch-core" && o.Kind != "as112-dns" {
		return "", errors.New("unsupported provisioning kind")
	}
	if o.Kind == "as112-dns" && !identifier.MatchString(o.RouterID) || o.Kind != "as112-dns" && o.RouterID != "" {
		return "", errors.New("as112-dns fetch requires its expected --router; other kinds cannot use it")
	}
	if o.Kind == "dns-arpa" && o.Zone == "" {
		return "", errors.New("DNS fetch requires expected --zone")
	}
	if o.Kind == "switch-core" && !identifier.MatchString(o.SwitchID) {
		return "", errors.New("switch-core fetch requires expected --switch")
	}
	if o.Kind != "switch-core" && o.SwitchID != "" {
		return "", errors.New("--switch only applies to switch-core")
	}
	raw, headers, err := get(ctx, transport, o.Server, "/api/provisioning/"+o.Revision+"/config", token, provision.MaxArtifactBytes)
	if err != nil {
		return "", err
	}
	for key, want := range map[string]string{"X-Provisioning-Revision": o.Revision, "X-Provisioning-Profile-ID": o.Profile, "X-Provisioning-Kind": o.Kind} {
		if len(headers.Values(key)) != 1 || headers.Get(key) != want {
			return "", errors.New("download does not match the expected profile, revision or kind")
		}
	}
	sum := headers.Get("X-Config-SHA256")
	if len(headers.Values("X-Config-SHA256")) != 1 || !validChecksum(sum) || checksum(string(raw)) != sum {
		return "", errors.New("downloaded artifact failed its actual SHA-256 checksum")
	}
	if err = provision.ValidateArtifact(artifact(o.Kind, o.Zone, filepath.Base(o.Output), raw)); err != nil {
		return "", err
	}
	if o.Kind == "switch-core" {
		var identity struct {
			SwitchID string `json:"switchId"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.SwitchID != o.SwitchID || len(headers.Values("X-Provisioning-Switch-ID")) != 1 || headers.Get("X-Provisioning-Switch-ID") != o.SwitchID {
			return "", errors.New("core plan does not match the expected physical switch")
		}
	}
	if o.Kind == "as112-dns" {
		var identity struct {
			RouterID string `json:"routerId"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.RouterID != o.RouterID || len(headers.Values("X-Provisioning-Router-ID")) != 1 || headers.Get("X-Provisioning-Router-ID") != o.RouterID {
			return "", errors.New("AS112 bundle does not match the expected router")
		}
	}
	if err = writeNew(o.Output, raw); err != nil {
		return "", err
	}
	return sum, nil
}
