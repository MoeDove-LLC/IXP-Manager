package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

const deploymentPlainLimit = encryptedArtifactPlainLimit
const deploymentCipherLimit = encryptedArtifactCipherLimit
const deploymentStorageLimit = encryptedArtifactStorageLimit
const deploymentCountLimit = 2000

func (a *App) deploymentDir() string    { return filepath.Join(filepath.Dir(a.store.path), "deployments") }
func deploymentAAD(d Deployment) string { return "deployment:" + d.ID + ":" + d.Checksum }
func (a *App) deploymentReviewToken(d Deployment) string {
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("deployment-review-v1\x00" + d.ID + "\x00" + d.Checksum))
	return hex.EncodeToString(mac.Sum(nil))
}
func deploymentCredentialSnapshot(s *State, routerID string) (map[string]string, map[string]bool) {
	revisions := map[string]string{}
	presence := map[string]bool{}
	for _, p := range s.Resources["peers"] {
		id := str(p, "id")
		c := s.PeerCredentials[id]
		if str(p, "routerId") == routerID && p["enabled"] == true && c.Revision != "" {
			revisions[id] = c.Revision
			presence[id] = c.Ciphertext != ""
		}
	}
	return revisions, presence
}
func validDeploymentMetadata(d Deployment) error {
	if d.RFC1997Mode != "" {
		if (d.RFC1997Mode != "native" && d.RFC1997Mode != "explicit") || d.RFC1997Passthrough == nil || !routing.RFC1997ControlSupported(d.Platform) {
			return errors.New("invalid deployment RFC1997 metadata")
		}
	} else if d.RFC1997Passthrough != nil {
		return errors.New("RFC1997 metadata lacks its interpretation mode")
	}
	if !identifier.MatchString(d.ID) || !identifier.MatchString(d.RouterID) || !externalDigest(d.Checksum) {
		return errors.New("invalid deployment identity/checksum")
	}
	known := false
	for _, capability := range routing.Capabilities() {
		if d.Platform == capability.Platform {
			known = true
			break
		}
	}
	if !known {
		return errors.New("unknown deployment platform")
	}
	role := d.RouterRole
	if role == "" {
		role = "route-server"
	}
	if role != "route-server" && role != "route-collector" && role != routing.RoleAS112 {
		return errors.New("invalid deployment router role")
	}
	if role == routing.RoleAS112 {
		if !routing.RoleSupported(d.Platform, role) || d.CollectorPolicy != "" || d.RFC1997Mode != "" {
			return errors.New("invalid AS112 deployment capability metadata")
		}
		if err := routing.AS112Service(routing.Router{AS112Service: d.AS112Service, AS112IPv4: d.AS112IPv4, AS112IPv6: d.AS112IPv6}).Validate(); err != nil {
			return err
		}
		if d.Platform != "junos" && (d.AS112NextHop4 != "" || d.AS112NextHop6 != "") {
			return errors.New("AS112 next-hop metadata requires Junos")
		}
	} else if d.AS112Service != "" || d.AS112IPv4 || d.AS112IPv6 || d.AS112NextHop4 != "" || d.AS112NextHop6 != "" {
		return errors.New("AS112 deployment settings require its role")
	}
	if role == "route-collector" {
		if !routing.RoleSupported(d.Platform, role) || (d.CollectorPolicy != "observe" && d.CollectorPolicy != "authorized-only") {
			return errors.New("invalid deployment collector policy or platform")
		}
	} else if d.CollectorPolicy != "" {
		return errors.New("route-server deployment cannot contain collector policy")
	}
	switch d.Status {
	case "draft", "validated", "approved":
	default:
		return errors.New("invalid deployment state")
	}
	if _, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err != nil {
		return errors.New("invalid deployment timestamp")
	}
	if d.ArtifactVersion < 0 || d.ArtifactVersion > 1 || len(d.Validation) > 2048 || len(d.CredentialRevisions) > 10000 || len(d.CredentialPresence) > 10000 {
		return errors.New("invalid deployment metadata bounds")
	}
	for id, revision := range d.CredentialRevisions {
		if !identifier.MatchString(id) || !credentialRevision.MatchString(revision) {
			return errors.New("invalid deployment credential revision")
		}
		if _, ok := d.CredentialPresence[id]; !ok {
			return errors.New("deployment credential presence missing")
		}
	}
	for id := range d.CredentialPresence {
		if _, ok := d.CredentialRevisions[id]; !ok {
			return errors.New("credential presence lacks revision")
		}
	}
	if d.Pruned && (d.Config != "" || d.ArtifactVersion != 1 || d.PrunedAt.IsZero() || !identifier.MatchString(d.PrunedBy) || d.ArtifactSize < 1 || d.ArtifactSize > deploymentCipherLimit || !externalDigest(d.ArtifactChecksum)) {
		return errors.New("invalid deployment pruning metadata")
	}
	return nil
}

// writeDeploymentArtifact requires Store.Update's mutex. Referencing metadata
// commits only after the immutable 0600 ciphertext is fully synced.
func (a *App) writeDeploymentArtifact(d *Deployment, config string) error {
	if err := validDeploymentMetadata(*d); err != nil {
		return err
	}
	metadata, err := a.writeEncryptedArtifactLocked("deployments", d.ID, d.Checksum, config)
	if err != nil {
		return err
	}
	d.ArtifactVersion = metadata.Version
	d.ArtifactSize = metadata.Size
	d.ArtifactChecksum = metadata.Checksum
	d.Config = ""
	return nil
}
func (a *App) readDeploymentArtifact(d Deployment) (string, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	return a.readDeploymentArtifactLocked(d)
}

// readDeploymentArtifactLocked requires the caller's store read/write lock.
func (a *App) readDeploymentArtifactLocked(d Deployment) (string, error) {
	if err := validDeploymentMetadata(d); err != nil {
		return "", err
	}
	if d.Pruned {
		return "", errors.New("deployment artifact has been explicitly pruned")
	}
	if d.Config != "" {
		return "", errors.New("inline deployment requires migration")
	}
	return a.readEncryptedArtifactLocked("deployments", d.ID, d.Checksum, encryptedArtifactFile{Version: d.ArtifactVersion, Size: d.ArtifactSize, Checksum: d.ArtifactChecksum})
}

// MigrateDeploymentArtifacts runs under the caller's offline store lock (or
// during App.New before serving). It preserves published bytes, IDs and hashes.
func MigrateDeploymentArtifacts(store *Store) error {
	a := &App{store: store}
	if err := a.validateSecurityEncryption(); err != nil {
		return err
	}
	return a.initDeploymentArtifacts()
}
func (a *App) initDeploymentArtifacts() error {
	if err := a.initEncryptedArtifactDir("deployments"); err != nil {
		return err
	}
	state := a.store.Snapshot()
	if len(state.Deployments) > deploymentCountLimit {
		return errors.New("deployment history exceeds 2000 revision limit")
	}
	needsMigration := false
	for _, d := range state.Deployments {
		if d.Config != "" {
			if d.ArtifactVersion != 0 {
				return errors.New("encrypted deployment unexpectedly retains inline plaintext")
			}
			needsMigration = true
		}
	}
	if needsMigration {
		if err := a.store.Update(func(s *State) error {
			for i := range s.Deployments {
				d := &s.Deployments[i]
				if d.Platform == "" {
					d.Platform = "bird2"
				}
				if err := validDeploymentMetadata(*d); err != nil {
					return err
				}
				if d.Config == "" {
					continue
				}
				if d.Pruned {
					return errors.New("pruned deployment unexpectedly contains inline plaintext")
				}
				plain := d.Config
				if routing.Checksum(plain) != d.Checksum {
					return errors.New("legacy deployment checksum mismatch")
				}
				path := filepath.Join(a.deploymentDir(), d.ID+".enc")
				if _, err := os.Lstat(path); err == nil {
					// Recover a prior crash after the ciphertext write but before state commit.
					info, err := os.Lstat(path)
					if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > deploymentCipherLimit {
						return errors.New("invalid migration artifact")
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					candidate := *d
					candidate.Config = ""
					candidate.ArtifactVersion = 1
					candidate.ArtifactSize = int64(len(raw))
					candidate.ArtifactChecksum = digest(string(raw))
					recovered, err := a.readDeploymentArtifactLocked(candidate)
					if err != nil || recovered != plain {
						return errors.New("existing migration artifact does not match legacy bytes")
					}
					*d = candidate
				} else if !os.IsNotExist(err) {
					return err
				} else {
					if err := a.writeDeploymentArtifact(d, plain); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if a.store.closed {
		return errors.New("store closed during deployment initialization")
	}
	state = cloneState(a.store.state)
	seen := map[string]bool{}
	for _, d := range state.Deployments {
		if seen[d.ID] {
			return errors.New("duplicate deployment revision")
		}
		seen[d.ID] = true
		if err := validDeploymentMetadata(d); err != nil {
			return err
		}
		if d.Pruned {
			continue
		}
		if _, err := a.readDeploymentArtifactLocked(d); err != nil {
			return err
		}
	}
	for _, d := range state.Deployments {
		if d.Pruned {
			if _, err := a.removePrunedDeployment(d); err != nil {
				return err
			}
		}
	}
	return a.cleanupDeploymentOrphansLocked(state)
}

// The deployment directory is exclusively managed by this application. Complete
// unreferenced encrypted revisions and private staging files are uncommitted
// outputs; removing them makes failed/crashed creation recover quota safely.
func (a *App) cleanupDeploymentOrphansLocked(state State) error {
	ids := make([]string, 0, len(state.Deployments))
	for _, d := range state.Deployments {
		ids = append(ids, d.ID)
	}
	return a.cleanupEncryptedArtifactOrphansLocked("deployments", ids)
}
func (a *App) deploymentMetadata(d Deployment) Record {
	role := d.RouterRole
	if role == "" {
		role = "route-server"
	}
	view := Record{"id": d.ID, "routerId": d.RouterID, "routerRole": role, "collectorPolicy": d.CollectorPolicy, "platform": d.Platform, "status": d.Status, "checksum": a.deploymentReviewToken(d), "checksumKind": "hmac-review-token", "createdAt": d.CreatedAt, "validation": d.Validation, "approvedBy": d.ApprovedBy, "pruned": d.Pruned, "prunedAt": optionalTime(d.PrunedAt), "artifactUnavailable": d.Pruned}
	if role == routing.RoleAS112 {
		view["as112Service"], view["as112IPv4"], view["as112IPv6"] = d.AS112Service, d.AS112IPv4, d.AS112IPv6
		view["as112NextHop4"], view["as112NextHop6"] = d.AS112NextHop4, d.AS112NextHop6
		view["as112DNSHealthChecked"] = false
	}
	if d.RFC1997Passthrough != nil {
		view["rfc1997Passthrough"] = *d.RFC1997Passthrough
		view["rfc1997Mode"] = d.RFC1997Mode
	}
	return view
}
func (a *App) deploymentDetail(d Deployment) (Record, error) {
	view := a.deploymentMetadata(d)
	if d.Pruned {
		return view, nil
	}
	config, err := a.readDeploymentArtifact(d)
	if err != nil {
		return nil, err
	}
	redacted, err := routing.RedactConfig(d.Platform, config)
	if err != nil {
		return nil, errors.New("safe configuration preview unavailable")
	}
	view["config"] = redacted
	view["redacted"] = true
	return view, nil
}
func latestApprovedDeployment(s *State, routerID string) string {
	candidates := map[string]Deployment{}
	latestID := ""
	latest := ""
	for _, d := range s.Deployments {
		if d.RouterID == routerID && d.Status == "approved" && !d.Pruned {
			candidates[d.ID] = d
			if d.CreatedAt > latest {
				latest = d.CreatedAt
				latestID = d.ID
			}
		}
	}
	for i := len(s.Audit) - 1; i >= 0; i-- {
		event := s.Audit[i]
		if event.Action == "approve" && event.Resource == "deployments" {
			if _, ok := candidates[event.ResourceID]; ok {
				return event.ResourceID
			}
		}
	}
	return latestID
}
func (a *App) deploymentPrunable(s *State, d Deployment) bool {
	if d.Pruned {
		return true
	}
	if find(s, "routers", d.RouterID) == nil {
		return true
	}
	if d.Status != "approved" {
		return true
	}
	if latestApprovedDeployment(s, d.RouterID) == d.ID {
		return false
	}
	config, err := a.renderRouter(s, d.RouterID)
	if err == nil && routing.Checksum(config) == d.Checksum {
		for _, other := range s.Deployments {
			if other.ID != d.ID && other.RouterID == d.RouterID && other.Status == "approved" && !other.Pruned && other.Checksum == d.Checksum {
				return true
			}
		}
		return false
	}
	return true
}
func (a *App) removePrunedDeployment(d Deployment) (int64, error) {
	if !d.Pruned {
		return 0, errors.New("deployment is not marked for pruning")
	}
	if err := validDeploymentMetadata(d); err != nil {
		return 0, err
	}
	return a.removeEncryptedArtifactLocked("deployments", d.ID, d.Checksum, encryptedArtifactFile{Version: d.ArtifactVersion, Size: d.ArtifactSize, Checksum: d.ArtifactChecksum})
}
func publicCredentialChanges(base, after Deployment) []CredentialChange {
	ids := map[string]bool{}
	for id := range base.CredentialRevisions {
		ids[id] = true
	}
	for id := range after.CredentialRevisions {
		ids[id] = true
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	out := []CredentialChange{}
	for _, id := range keys {
		beforeRev, afterRev := base.CredentialRevisions[id], after.CredentialRevisions[id]
		beforeSet, afterSet := base.CredentialPresence[id], after.CredentialPresence[id]
		if beforeRev == afterRev && beforeSet == afterSet {
			continue
		}
		change := "revision"
		switch {
		case !beforeSet && afterSet:
			change = "added"
		case beforeSet && !afterSet:
			change = "removed"
		case beforeSet && afterSet:
			change = "rotated"
		}
		out = append(out, CredentialChange{PeerID: id, Change: change, BeforeRevision: beforeRev, AfterRevision: afterRev, BeforeConfigured: beforeSet, AfterConfigured: afterSet})
	}
	return out
}
