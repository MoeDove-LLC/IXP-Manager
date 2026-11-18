package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

type sessionObservation struct {
	PeerID            string    `json:"peerId"`
	RouterID          string    `json:"routerId"`
	State             string    `json:"state"`
	Status            string    `json:"status"`
	ObservedAt        time.Time `json:"observedAt"`
	IdentityVerified  bool      `json:"identityVerified"`
	ConfiguredEnabled *bool     `json:"configuredEnabled"`
	Monitor           *bool     `json:"monitor"`
}
type switchObservation struct {
	SwitchID       string    `json:"switchId"`
	Status         string    `json:"status"`
	LastObservedAt time.Time `json:"lastObservedAt"`
	LastSuccessAt  time.Time `json:"lastSuccessAt"`
	ErrorCode      string    `json:"errorCode"`
}
type probeResult struct {
	Code    int
	Message string
}

func unknown(message string) probeResult { return probeResult{3, "UNKNOWN - " + message} }

func runProbe(args []string, input io.Reader, output io.Writer) (int, error) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(output)
	server := fs.String("server", "", "trusted HTTPS control plane origin")
	kind := fs.String("kind", "", "bgp, router, switch or core (no direct packet probes)")
	id := fs.String("id", "", "actual peer, router, switch or core bundle ID")
	file := fs.String("token-file", "", "private monitoring:read, devices:read or core:read API token file")
	stdin := fs.Bool("token-stdin", false, "read read-only token from stdin")
	age := fs.Duration("max-age", 120*time.Second, "maximum observation age, 30..300 seconds; server stale status always wins")
	done, err := parse(fs, args)
	if done {
		return 0, err
	}
	if err != nil {
		fmt.Fprintln(output, "UNKNOWN - invalid probe flags")
		return 3, nil
	}
	if !identifier.MatchString(*id) || *age < 30*time.Second || *age > 5*time.Minute || (*kind != "bgp" && *kind != "router" && *kind != "switch" && *kind != "core") {
		fmt.Fprintln(output, "UNKNOWN - valid --kind, --id and bounded --max-age are required")
		return 3, nil
	}
	token, err := readToken(*file, *stdin, input)
	if err != nil {
		fmt.Fprintln(output, "UNKNOWN - private read-only API token unavailable")
		return 3, nil
	}
	result := probe(context.Background(), http.DefaultTransport, *server, token, *kind, *id, *age, time.Now())
	fmt.Fprintln(output, result.Message)
	return result.Code, nil
}

func probe(ctx context.Context, transport http.RoundTripper, server, token, kind, id string, maxAge time.Duration, now time.Time) probeResult {
	if !identifier.MatchString(id) || maxAge < 30*time.Second || maxAge > 5*time.Minute {
		return unknown("invalid target or freshness bound")
	}
	path := "/api/monitoring/summary"
	if kind == "core" {
		path = "/api/core/bundles/" + id + "/observations"
	} else if kind == "switch" {
		path = "/api/devices/status"
	} else if kind != "bgp" && kind != "router" {
		return unknown("unsupported observation kind")
	}
	raw, _, err := get(ctx, transport, server, path, token, 8<<20)
	if err != nil {
		return unknown("observation request failed; check trusted endpoint, token scope and availability")
	}
	if kind == "core" {
		return probeCore(raw, id, maxAge, now)
	}
	if kind == "switch" {
		var body struct {
			Items []switchObservation `json:"items"`
		}
		if err = decodeResponse(raw, &body); err != nil {
			return unknown("invalid switch observation response")
		}
		matches := []switchObservation{}
		for _, item := range body.Items {
			if item.SwitchID == id {
				matches = append(matches, item)
			}
		}
		if len(matches) != 1 {
			return unknown("switch observation missing or ambiguous")
		}
		s := matches[0]
		if s.Status == "error" || s.ErrorCode != "" {
			return unknown("switch collection failed; this does not establish device health")
		}
		if s.Status != "fresh" || !freshAt(s.LastObservedAt, now, maxAge) || !freshAt(s.LastSuccessAt, now, maxAge) || s.LastObservedAt.Before(s.LastSuccessAt) {
			return unknown("switch observations stale or unavailable")
		}
		return probeResult{0, "OK - fresh switch SNMP observation; component health is not implied"}
	}
	var body struct {
		Sessions []sessionObservation `json:"sessions"`
	}
	if err = decodeResponse(raw, &body); err != nil {
		return unknown("invalid BGP observation response")
	}
	items := []sessionObservation{}
	seen := map[string]bool{}
	for _, s := range body.Sessions {
		if (kind == "bgp" && s.PeerID == id) || (kind == "router" && s.RouterID == id) {
			if (s.ConfiguredEnabled != nil && !*s.ConfiguredEnabled) || (s.Monitor != nil && !*s.Monitor) {
				if kind == "bgp" {
					return unknown("BGP session is administratively disabled or not selected for monitoring")
				}
				continue
			}
			if !identifier.MatchString(s.PeerID) || !identifier.MatchString(s.RouterID) || seen[s.PeerID] {
				return unknown("ambiguous session observations")
			}
			seen[s.PeerID] = true
			items = append(items, s)
		}
	}
	if len(items) == 0 || (kind == "bgp" && len(items) != 1) {
		return unknown("no matching observed BGP session")
	}
	for _, s := range items {
		if s.Status != "fresh" || !s.IdentityVerified || !freshAt(s.ObservedAt, now, maxAge) || !knownBGPState(s.State) {
			return unknown("BGP observation stale, missing or not bound to the current peer identity")
		}
	}
	if kind == "router" {
		return probeResult{0, "OK - fresh identity-verified BGP observations; independent router daemon health is not measured"}
	}
	if items[0].State == "Established" {
		return probeResult{0, "OK - current BGP identity observed Established"}
	}
	return probeResult{2, "CRITICAL - current BGP identity observed " + items[0].State}
}

func knownBGPState(s string) bool {
	switch s {
	case "Established", "Idle", "Connect", "Active", "OpenSent", "OpenConfirm":
		return true
	}
	return false
}
func freshAt(at, now time.Time, maxAge time.Duration) bool {
	return !at.IsZero() && !at.Before(now.Add(-maxAge)) && !at.After(now.Add(30*time.Second))
}
func decodeResponse(raw []byte, value any) error {
	// Ignore API fields outside the typed observations, including the top-level
	// query time: it must never stand in for an actual sample timestamp.
	if err := json.Unmarshal(raw, value); err != nil {
		return errors.New("invalid observation JSON")
	}
	return nil
}
