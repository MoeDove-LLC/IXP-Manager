package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

func (a *App) initAtlas(path string) error {
	config, err := atlas.LoadConfig(path)
	if err != nil {
		return err
	}
	client, err := atlas.NewClient(config)
	if err != nil {
		return err
	}
	a.atlasConfig = config
	if client != nil {
		a.atlasProvider = client
	}
	if err = a.initEncryptedArtifactDir("atlas"); err != nil {
		return err
	}
	state := a.store.Snapshot()
	if err = ValidateAtlasArtifacts(filepath.Dir(a.store.path), state, a.store.securityKey); err != nil {
		return err
	}
	if err = a.store.Update(func(s *State) error {
		if s.Atlas.InstanceID == "" {
			s.Atlas.InstanceID = token(16)
		}
		for ri := range s.Atlas.Runs {
			run := &s.Atlas.Runs[ri]
			for mi := range run.Items {
				m := &run.Items[mi]
				if m.Status == "creating" {
					m.Status = "uncertain"
					m.LastError = "creation-interrupted-no-automatic-retry"
				}
				if m.Status == "uncertain" {
					run.ExecutionAuthorizedAt = time.Time{}
				}
			}
			atlasRunStatus(run)
		}
		return nil
	}); err != nil {
		return err
	}
	return a.cleanupAtlasArtifacts()
}
func (a *App) closeAtlas() {
	a.atlasMu.Lock()
	defer a.atlasMu.Unlock()
	a.atlasClosed = true
	if closer, ok := a.atlasProvider.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
func (a *App) cleanupAtlasArtifacts() error {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	return a.cleanupEncryptedArtifactOrphansLocked("atlas", AtlasArtifactIDs(a.store.state))
}
func (a *App) writeAtlasArtifact(s *State, kind string, value any) (string, error) {
	if len(s.Atlas.Artifacts) >= 5000 {
		return "", errors.New("Atlas artifact metadata limit reached; prune terminal runs")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	id := token(16)
	sum := digest(string(raw))
	file, err := a.writeEncryptedArtifactLocked("atlas", id, sum, string(raw))
	if err != nil {
		return "", err
	}
	s.Atlas.Artifacts = append(s.Atlas.Artifacts, AtlasArtifact{ID: id, Kind: kind, Checksum: sum, File: file, CreatedAt: time.Now().UTC()})
	return id, nil
}
func atlasArtifact(s *State, id string) *AtlasArtifact {
	for i := range s.Atlas.Artifacts {
		if s.Atlas.Artifacts[i].ID == id {
			return &s.Atlas.Artifacts[i]
		}
	}
	return nil
}
func removeAtlasArtifactMetadata(s *State, id string) {
	if id == "" {
		return
	}
	for i, a := range s.Atlas.Artifacts {
		if a.ID == id {
			s.Atlas.Artifacts = append(s.Atlas.Artifacts[:i], s.Atlas.Artifacts[i+1:]...)
			return
		}
	}
}
func (a *App) readAtlasArtifactLocked(s *State, id string, value any) error {
	m := atlasArtifact(s, id)
	if m == nil {
		return errors.New("Atlas snapshot unavailable")
	}
	raw, err := a.readEncryptedArtifactLocked("atlas", id, m.Checksum, m.File)
	if err != nil {
		return err
	}
	if json.Unmarshal([]byte(raw), value) != nil {
		return errors.New("Atlas snapshot data invalid")
	}
	return nil
}
func atlasRun(s *State, id string) *AtlasRun {
	for i := range s.Atlas.Runs {
		if s.Atlas.Runs[i].ID == id {
			return &s.Atlas.Runs[i]
		}
	}
	return nil
}
func atlasItem(r *AtlasRun, id string) *AtlasMeasurement {
	if r == nil {
		return nil
	}
	for i := range r.Items {
		if r.Items[i].ID == id {
			return &r.Items[i]
		}
	}
	return nil
}
func atlasProbeSnapshot(s *State, member string, af int) *AtlasProbeSnapshot {
	for i := range s.Atlas.ProbeSnapshots {
		p := &s.Atlas.ProbeSnapshots[i]
		if p.MemberID == member && p.AF == af {
			return p
		}
	}
	return nil
}
func (a *App) atlasProbesLocked(s *State, member string, af int, now time.Time) ([]atlas.Probe, error) {
	p := atlasProbeSnapshot(s, member, af)
	if p == nil || now.Sub(p.FetchedAt) > time.Hour || p.FetchedAt.After(now.Add(30*time.Second)) {
		return nil, errors.New("discover fresh Atlas probes for each selected member and address family")
	}
	var body atlasProbeFile
	if err := a.readAtlasArtifactLocked(s, p.ID, &body); err != nil {
		return nil, err
	}
	return body.Probes, nil
}

func validateAtlasState(s *State) error {
	if s.Atlas.InstanceID == "" && len(s.Atlas.Runs) == 0 && len(s.Atlas.ProbeSnapshots) == 0 && len(s.Atlas.Artifacts) == 0 && len(s.Atlas.CreditBuckets) == 0 {
		return nil
	}
	if !credentialRevision.MatchString(s.Atlas.InstanceID) || len(s.Atlas.Runs) > 1000 || len(s.Atlas.ProbeSnapshots) > 400 || len(s.Atlas.Artifacts) > 5000 {
		return errors.New("invalid or unbounded Atlas metadata")
	}
	if len(s.Atlas.CreditBuckets) > 25 {
		return errors.New("Atlas credit reservation history exceeds its bound")
	}
	creditHours := map[int64]bool{}
	reserved := 0
	for _, bucket := range s.Atlas.CreditBuckets {
		if bucket.Hour <= 0 || bucket.Reserved < 1 || bucket.Reserved > 100000 || creditHours[bucket.Hour] {
			return errors.New("invalid Atlas credit reservation bucket")
		}
		creditHours[bucket.Hour] = true
		reserved += bucket.Reserved
	}
	if reserved > 100000 {
		return errors.New("Atlas credit reservation exceeds the local bound")
	}
	files := map[string]AtlasArtifact{}
	refs := map[string]bool{}
	for _, f := range s.Atlas.Artifacts {
		if !credentialRevision.MatchString(f.ID) || files[f.ID].ID != "" || !externalDigest(f.Checksum) || f.File.Version != 1 || f.File.Size <= 0 || f.File.Size > encryptedArtifactCipherLimit || !externalDigest(f.File.Checksum) || f.CreatedAt.IsZero() {
			return errors.New("invalid Atlas artifact metadata")
		}
		switch f.Kind {
		case "probes", "receipt", "status", "results":
		default:
			return errors.New("invalid Atlas artifact kind")
		}
		files[f.ID] = f
	}
	probes := map[string]bool{}
	for _, p := range s.Atlas.ProbeSnapshots {
		key := p.MemberID + string(rune(p.AF))
		if !identifier.MatchString(p.MemberID) || (p.AF != 4 && p.AF != 6) || p.ASN == 0 || p.Count < 0 || p.Count > 200 || p.FetchedAt.IsZero() || files[p.ID].Kind != "probes" || p.Checksum != files[p.ID].Checksum || probes[key] {
			return errors.New("invalid Atlas probe snapshot metadata")
		}
		probes[key] = true
		refs[p.ID] = true
	}
	seen := map[string]bool{}
	total := 0
	for _, r := range s.Atlas.Runs {
		if !credentialRevision.MatchString(r.ID) || seen[r.ID] || len(r.Items) < 1 || len(r.Items) > 90 || r.CreatedAt.IsZero() || r.Checksum != atlasRunChecksum(r) || !externalDigest(r.BaseChecksum) || r.EstimatedSamples < 1 || r.EstimatedSamples > atlas.MaxSamples || r.EstimatedCredits < 1 || r.EstimatedCredits > atlas.MaxEstimatedCredits {
			return errors.New("invalid Atlas run metadata")
		}
		seen[r.ID] = true
		total += len(r.Items)
		for _, m := range r.Items {
			if !credentialRevision.MatchString(m.ID) || seen[m.ID] || !identifier.MatchString(m.SourceMemberID) || !identifier.MatchString(m.TargetMemberID) || !atlas.ValidTarget(m.TargetAddress, r.Spec.AF) || len(m.ProbeIDs) < 1 || len(m.ProbeIDs) > atlas.MaxProbes {
				return errors.New("invalid Atlas measurement binding")
			}
			seen[m.ID] = true
			switch m.Status {
			case "draft", "approved", "creating", "uncertain", "scheduled", "running", "stopping", "stopped", "completed", "failed", "archived", "cancelled":
			default:
				return errors.New("invalid Atlas measurement status")
			}
			if m.OwnershipConfirmed && (m.ProviderID < 1 || m.IntentID == "" || m.Description == "" || m.ReceiptArtifactID == "" && !r.Pruned) {
				return errors.New("Atlas ownership lacks its creation evidence")
			}
			for _, ref := range []struct{ id, kind string }{{m.ReceiptArtifactID, "receipt"}, {m.StatusArtifactID, "status"}, {m.ResultsArtifactID, "results"}} {
				if ref.id != "" {
					if files[ref.id].Kind != ref.kind || refs[ref.id] {
						return errors.New("Atlas snapshot reference missing")
					}
					refs[ref.id] = true
				}
			}
		}
	}
	if total > 2000 {
		return errors.New("Atlas child measurement limit reached")
	}
	for id := range files {
		if !refs[id] {
			return errors.New("unreferenced Atlas artifact metadata")
		}
	}
	return nil
}
func ValidateAtlasArtifacts(dir string, s State, key []byte) error {
	if err := validateAtlasState(&s); err != nil {
		return err
	}
	if len(s.Atlas.Artifacts) == 0 {
		return nil
	}
	if len(key) != 32 {
		return errors.New("Atlas encrypted snapshots require the matching security.key")
	}
	a := &App{store: &Store{path: filepath.Join(dir, "state.json"), securityKey: key}}
	for _, f := range s.Atlas.Artifacts {
		var value json.RawMessage
		if err := a.readAtlasArtifactLocked(&s, f.ID, &value); err != nil {
			return errors.New("Atlas encrypted snapshot integrity failed")
		}
	}
	for _, p := range s.Atlas.ProbeSnapshots {
		var body atlasProbeFile
		if err := a.readAtlasArtifactLocked(&s, p.ID, &body); err != nil || len(body.Probes) != p.Count {
			return errors.New("Atlas probe snapshot does not match its metadata")
		}
		for _, probe := range body.Probes {
			if _, err := atlasProbeByID(body.Probes, probe.ID, p.AF, p.ASN); err != nil {
				return errors.New("Atlas probe snapshot scope mismatch")
			}
		}
	}
	for _, r := range s.Atlas.Runs {
		for _, m := range r.Items {
			if m.ReceiptArtifactID != "" {
				var receipt atlasReceiptFile
				if err := a.readAtlasArtifactLocked(&s, m.ReceiptArtifactID, &receipt); err != nil {
					return err
				}
				want, _ := json.Marshal(atlasSpec(r, m))
				got, _ := json.Marshal(receipt.Spec)
				if receipt.RunID != r.ID || receipt.MeasurementID != m.ID || receipt.ProviderID != m.ProviderID || receipt.IntentID != m.IntentID || string(want) != string(got) {
					return errors.New("Atlas creation receipt binding mismatch")
				}
			}
			if m.StatusArtifactID != "" {
				var status atlasStatusFile
				if err := a.readAtlasArtifactLocked(&s, m.StatusArtifactID, &status); err != nil {
					return err
				}
				if status.Observation.ID != m.ProviderID || !atlas.MatchesIntent(status.Observation, atlasSpec(r, m)) {
					return errors.New("Atlas provider observation binding mismatch")
				}
			}
			if m.ResultsArtifactID != "" {
				var results atlasResultFile
				if err := a.readAtlasArtifactLocked(&s, m.ResultsArtifactID, &results); err != nil {
					return err
				}
				if len(results.Results) != m.ResultCount || m.ResultCount > m.EstimatedSamples {
					return errors.New("Atlas result count mismatch")
				}
				for _, row := range results.Results {
					selected := false
					for _, id := range m.ProbeIDs {
						if id == row.ProbeID {
							selected = true
						}
					}
					unknownTargetError := row.TargetAddress == "" && row.Error == "probe-error"
					if !selected || row.MeasurementID != m.ProviderID || !unknownTargetError && row.TargetAddress != m.TargetAddress || row.Type != r.Spec.Type || row.AF != r.Spec.AF {
						return errors.New("Atlas result identity mismatch")
					}
				}
			}
		}
	}
	return nil
}
