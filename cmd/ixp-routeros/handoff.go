package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// A browser download contains no credentials and may be 0644. This reader is
// deliberately separate from privateRead: reviews, credentials and stages keep
// their existing exact-0600 rule.
func planningHandoffRead(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("handoff requires an absolute clean path")
	}
	check := func(info os.FileInfo) bool {
		if info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() < 1 || info.Size() > routeros.MaxPlanBytes {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		return ok && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
	}
	before, err := os.Lstat(path)
	if err != nil || !check(before) {
		return nil, errors.New("handoff must be an owned, single-link, non-writable-by-others regular file within 16 MiB")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("handoff unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !check(opened) || !os.SameFile(before, opened) {
		return nil, errors.New("handoff identity or permissions changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, routeros.MaxPlanBytes+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || !check(after) || len(raw) != int(opened.Size()) || after.Size() != opened.Size() || after.Mode() != opened.Mode() || !after.ModTime().Equal(opened.ModTime()) {
		clear(raw)
		return nil, errors.New("handoff changed during its bounded read")
	}
	return raw, nil
}

type handoffCredentialBundle struct {
	Credentials         map[string]credentialWire `json:"credentials,omitempty"`
	BaselineCredentials map[string]credentialWire `json:"baselineCredentials,omitempty"`
}

func handoffObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, false
		}
	}
	return object, true
}

// A missing/null authentication object is not an explicit request to clear a
// current key. Reject case aliases as well as omissions before Go's zero values
// can turn a malformed handoff into a different desired authentication state.
func planningHandoffShape(raw []byte) bool {
	root, ok := handoffObject(raw, "version", "routerId", "deploymentId", "reviewIdentifier", "desired", "intentSha256")
	if !ok {
		return false
	}
	desired, ok := handoffObject(root["desired"], "routerId", "asn", "localIpv4", "localIpv6", "peers")
	if !ok {
		return false
	}
	var peers []json.RawMessage
	if json.Unmarshal(desired["peers"], &peers) != nil || peers == nil {
		return false
	}
	for _, rawPeer := range peers {
		peer, ok := handoffObject(rawPeer, "id", "asn", "address", "maxPrefixes", "allowedPrefixes", "allowedRoutes", "authentication")
		if !ok {
			return false
		}
		auth, ok := handoffObject(peer["authentication"], "enabled", "revision")
		if !ok {
			return false
		}
		var enabled *bool
		if json.Unmarshal(auth["enabled"], &enabled) != nil || enabled == nil {
			return false
		}
		if routes, exists := peer["allowedRoutes"]; exists {
			var pairs []json.RawMessage
			if json.Unmarshal(routes, &pairs) != nil {
				return false
			}
			for _, pair := range pairs {
				if _, ok := handoffObject(pair, "prefix", "originAsn"); !ok {
					return false
				}
			}
		}
	}
	return true
}

func readHandoffCredentials(path string) (handoffCredentialBundle, error) {
	var bundle handoffCredentialBundle
	if path == "" {
		return bundle, nil
	}
	raw, err := privateRead(path)
	if err != nil {
		return bundle, err
	}
	defer clear(raw)
	root, ok := handoffObject(raw, "credentials", "baselineCredentials")
	if !ok || strictDecode(raw, &bundle) != nil {
		return handoffCredentialBundle{}, errors.New("private handoff credential bundle invalid")
	}
	if err := validateCredentialMaps(root); err != nil {
		return handoffCredentialBundle{}, err
	}
	return bundle, nil
}

func validateCredentialMaps(root map[string]json.RawMessage) error {
	for _, key := range []string{"credentials", "baselineCredentials"} {
		if data, exists := root[key]; exists {
			var values map[string]json.RawMessage
			if json.Unmarshal(data, &values) != nil {
				return errors.New("private credential map invalid")
			}
			for _, value := range values {
				if _, ok := handoffObject(value, "revision", "password"); !ok {
					return errors.New("private credential entry invalid")
				}
			}
		}
	}
	return nil
}

func handoffPlanIntent(review control.Review, handoff routeros.PlanningHandoff, bundle handoffCredentialBundle, timeout int, exclusive, identity bool) (planIntent, error) {
	if err := handoff.Validate(); err != nil {
		return planIntent{}, errors.New("handoff metadata or intent checksum invalid")
	}
	if handoff.RouterID != review.Snapshot.Target.RouterID {
		return planIntent{}, errors.New("handoff inventory router ID does not match the local reviewed target")
	}
	if !review.Snapshot.CrossChannelIdentityVerified {
		return planIntent{}, errors.New("handoff planning requires a strict local review with verified API/SSH identity")
	}
	d := handoff.Desired
	input := planIntent{ASN: d.ASN, RouterID: d.RouterID, LocalIPv4: d.LocalIPv4, LocalIPv6: d.LocalIPv6, Peers: []peerIntent{}, Options: routeros.PlanOptions{Bootstrap: review.Snapshot.NamespaceEmpty, AllowIdentityChange: identity}, TimeoutSeconds: timeout, ExclusiveAccessAcknowledged: exclusive, Credentials: bundle.Credentials, BaselineCredentials: bundle.BaselineCredentials}
	for _, p := range d.Peers {
		auth := p.Authentication
		input.Peers = append(input.Peers, peerIntent{ID: p.ID, ASN: p.ASN, Address: p.Address, MaxPrefixes: p.MaxPrefixes, AllowedPrefixes: p.AllowedPrefixes, AllowedRoutes: p.AllowedRoutes, Authentication: &auth})
	}
	return input, nil
}
