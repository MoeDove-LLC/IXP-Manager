package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

func (a *App) atlasTick(ctx context.Context, now time.Time) error {
	if !a.atlasMu.TryLock() {
		return nil
	}
	defer a.atlasMu.Unlock()
	if a.atlasClosed || a.atlasProvider == nil {
		return nil
	}
	defer func() { _ = a.cleanupAtlasArtifacts() }()
	return a.atlasWorkOne(ctx, "", now)
}
func atlasTerminal(status string) bool {
	switch status {
	case "stopped", "completed", "failed", "archived", "cancelled":
		return true
	}
	return false
}
func (a *App) atlasWorkOne(ctx context.Context, runID string, now time.Time) error {
	if a.atlasClosed || a.atlasProvider == nil {
		return errors.New("Atlas provider is disabled or closed")
	}
	s := a.store.Snapshot()
	for _, r := range s.Atlas.Runs {
		if runID != "" && r.ID != runID || r.Pruned || r.ProviderOrigin != a.atlasProvider.Origin() {
			continue
		}
		for _, m := range r.Items {
			if m.Status == "creating" {
				return a.store.Update(func(s *State) error {
					current := atlasRun(s, r.ID)
					item := atlasItem(current, m.ID)
					if item != nil && item.Status == "creating" {
						item.Status = "uncertain"
						item.LastError = "creation-interrupted-no-automatic-retry"
						current.ExecutionAuthorizedAt = time.Time{}
						atlasRunStatus(current)
					}
					return nil
				})
			}
		}
		for _, m := range r.Items {
			if m.OwnershipConfirmed && !atlasTerminal(m.Status) && (runID != "" || m.StopAttemptedAt.IsZero() || !now.Before(m.NextSyncAt)) && (r.StopRequested || !m.ScheduledStop.IsZero() && !now.Before(m.ScheduledStop)) {
				return a.atlasStopOne(ctx, r.ID, m.ID, now)
			}
		}
		for _, m := range r.Items {
			if !r.StopRequested && !r.ExecutionAuthorizedAt.IsZero() && m.Status == "approved" {
				return a.atlasCreateOne(ctx, r.ID, m.ID, now)
			}
		}
		for _, m := range r.Items {
			resultGrace := m.ScheduledStart.Add(time.Duration(r.Spec.DurationSeconds)*time.Second + time.Hour)
			if (m.Status == "uncertain" || m.OwnershipConfirmed && (!atlasTerminal(m.Status) || m.ResultCount < m.EstimatedSamples && now.Before(resultGrace))) && (runID != "" || !now.Before(m.NextSyncAt)) {
				return a.atlasRefreshOne(ctx, r.ID, m.ID, now)
			}
		}
	}
	return nil
}
func (a *App) atlasCreateOne(ctx context.Context, runID, itemID string, now time.Time) error {
	var selected AtlasMeasurement
	var run AtlasRun
	err := a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		if r == nil || r.Pruned || r.StopRequested || r.ExecutionAuthorizedAt.IsZero() || r.ProviderOrigin != a.atlasProvider.Origin() {
			return errors.New("Atlas execution is not authorized")
		}
		m := atlasItem(r, itemID)
		if m == nil || m.Status != "approved" || !m.AttemptedAt.IsZero() {
			return errors.New("Atlas item already attempted; never repeat a creation POST")
		}
		if str(find(s, "users", r.ApprovedBy), "role") != "admin" || now.Sub(r.ApprovedAt) > 15*time.Minute || r.ApprovedAt.After(now.Add(30*time.Second)) {
			return errors.New("Atlas approval expired or approving administrator lost authority")
		}
		active, reservedToday := 0, 0
		for _, previousRun := range s.Atlas.Runs {
			for _, previous := range previousRun.Items {
				switch previous.Status {
				case "creating", "uncertain", "scheduled", "running", "stopping":
					active++
				}
			}
		}
		hour := now.Unix() / 3600
		buckets := []AtlasCreditBucket{}
		for _, bucket := range s.Atlas.CreditBuckets {
			if bucket.Hour >= hour-24 {
				buckets = append(buckets, bucket)
				reservedToday += bucket.Reserved
			}
		}
		if active >= 100 || reservedToday+m.EstimatedCredits > 100000 {
			return errors.New("Atlas local limit reached:100 active/uncertain measurements or100000 estimated credits reserved over24 hours")
		}
		if err := a.atlasCurrentLocked(s, r, now); err != nil {
			return err
		}
		start := r.Spec.StartAt
		if start.IsZero() {
			start = now.Add(time.Minute)
		}
		if start.Before(now.Add(-time.Minute)) {
			return errors.New("scheduled start elapsed; create a new reviewed plan")
		}
		m.IntentID = token(16)
		foundBucket := false
		for i := range buckets {
			if buckets[i].Hour == hour {
				buckets[i].Reserved += m.EstimatedCredits
				foundBucket = true
			}
		}
		if !foundBucket {
			if len(buckets) >= 25 {
				return errors.New("Atlas credit reservation clock window is inconsistent; wait for clock recovery before creating measurements")
			}
			buckets = append(buckets, AtlasCreditBucket{Hour: hour, Reserved: m.EstimatedCredits})
		}
		s.Atlas.CreditBuckets = buckets
		m.Description = fmt.Sprintf("MoeDove Atlas %s/%s/%s", s.Atlas.InstanceID, r.ID, m.IntentID)
		m.Status = "creating"
		m.AttemptedAt = now
		m.ScheduledStart = start
		m.NextSyncAt = now.Add(time.Minute)
		if !r.Spec.IsOneoff {
			m.ScheduledStop = start.Add(time.Duration(r.Spec.DurationSeconds) * time.Second)
		}
		atlasRunStatus(r)
		selected = *m
		run = *r
		audit(s, r.ApprovedBy, "atlas-create-intent", "atlas", m.ID)
		return validateAtlasState(s)
	})
	if err != nil {
		_ = a.store.Update(func(s *State) error {
			r := atlasRun(s, runID)
			m := atlasItem(r, itemID)
			if m != nil && m.Status == "approved" && m.AttemptedAt.IsZero() {
				m.LastError = "execution-admission-failed"
				r.ExecutionAuthorizedAt = time.Time{}
			}
			return nil
		})
		return err
	}
	id, raw, providerErr := a.atlasProvider.Create(ctx, atlasSpec(run, selected))
	if providerErr != nil {
		_ = a.store.Update(func(s *State) error {
			r := atlasRun(s, runID)
			m := atlasItem(r, itemID)
			if m == nil || m.Status != "creating" || m.IntentID != selected.IntentID {
				return errors.New("Atlas creation generation changed")
			}
			m.LastError = atlas.PublicError(providerErr)
			m.Status = "failed"
			if atlas.IsUncertain(providerErr) {
				m.Status = "uncertain"
			}
			r.ExecutionAuthorizedAt = time.Time{}
			atlasRunStatus(r)
			audit(s, r.ApprovedBy, "atlas-create-"+m.Status, "atlas", m.ID)
			return nil
		})
		return providerErr
	}
	// Keep the provider receipt before the confirming read. A timeout in the
	// latter is recoverable without another paid POST.
	err = a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		if r == nil {
			return errors.New("Atlas run disappeared")
		}
		m := atlasItem(r, itemID)
		if m == nil || m.Status != "creating" || m.IntentID != selected.IntentID {
			return errors.New("Atlas creation generation changed")
		}
		artifact, err := a.writeAtlasArtifact(s, "receipt", atlasReceiptFile{RunID: runID, MeasurementID: itemID, ProviderID: id, IntentID: selected.IntentID, Spec: atlasSpec(run, selected), Response: raw})
		if err != nil {
			return err
		}
		m.ProviderID = id
		m.ReceiptArtifactID = artifact
		m.Status = "uncertain"
		m.LastError = "awaiting-ownership-verification"
		atlasRunStatus(r)
		return nil
	})
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	return a.atlasRefreshOne(ctx, runID, itemID, now)
}
func (a *App) atlasRefreshOne(ctx context.Context, runID, itemID string, now time.Time) error {
	s := a.store.Snapshot()
	r := atlasRun(&s, runID)
	if r == nil || r.Pruned || r.ProviderOrigin != a.atlasProvider.Origin() {
		return errors.New("Atlas run unavailable or provider changed")
	}
	m := atlasItem(r, itemID)
	if m == nil || m.IntentID == "" {
		return errors.New("Atlas item has no creation intent")
	}
	spec := atlasSpec(*r, *m)
	var observed atlas.Measurement
	var raw []byte
	var err error
	if m.ProviderID > 0 {
		observed, raw, err = a.atlasProvider.Measurement(ctx, m.ProviderID)
	} else {
		var matches []atlas.Measurement
		matches, raw, err = a.atlasProvider.Find(ctx, m.Description)
		if err == nil {
			if len(matches) != 1 || !atlas.MatchesIntent(matches[0], spec) {
				err = errors.New("Atlas creation remains uncertain; no unique matching owned measurement")
			} else {
				observed = matches[0]
			}
		}
	}
	if err == nil && !atlas.MatchesIntent(observed, spec) {
		err = errors.New("provider measurement does not match the persisted creation intent")
	}
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	err = a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		item := atlasItem(r, itemID)
		if item == nil || item.IntentID != m.IntentID {
			return errors.New("Atlas item changed")
		}
		// Reconciliation binds only an unguessable persisted intent, never an ID
		// supplied by an administrator or an arbitrary public measurement.
		if item.ReceiptArtifactID == "" {
			receipt, e := a.writeAtlasArtifact(s, "receipt", atlasReceiptFile{RunID: runID, MeasurementID: itemID, ProviderID: observed.ID, IntentID: m.IntentID, Spec: spec, Response: raw})
			if e != nil {
				return e
			}
			item.ReceiptArtifactID = receipt
		}
		artifact, e := a.writeAtlasArtifact(s, "status", atlasStatusFile{Observation: observed, Raw: raw})
		if e != nil {
			return e
		}
		removeAtlasArtifactMetadata(s, item.StatusArtifactID)
		item.StatusArtifactID = artifact
		item.ProviderID = observed.ID
		item.OwnershipConfirmed = true
		item.ProviderStatus = observed.Status
		item.Status = observed.Status
		if r.Spec.IsOneoff && observed.Status == "stopped" {
			item.Status = "completed"
		}
		item.LastSyncedAt = now
		item.NextSyncAt = now.Add(time.Minute)
		item.LastError = ""
		if !atlas.Owns(observed, spec) {
			item.LastError = "provider-specification-mismatch-stop-requested"
			r.StopRequested = true
			r.ExecutionAuthorizedAt = time.Time{}
			for i := range r.Items {
				if r.Items[i].Status == "approved" {
					r.Items[i].Status = "cancelled"
				}
			}
		}
		atlasRunStatus(r)
		return nil
	})
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	if !atlas.Owns(observed, spec) && !atlasTerminal(observed.Status) {
		// Queue one bounded stop operation for the worker. A provider may take
		// time to acknowledge stopping; never recurse into repeated DELETEs.
		return a.cleanupAtlasArtifacts()
	}
	if now.Before(m.ScheduledStart) {
		return a.cleanupAtlasArtifacts()
	}
	results, resultsRaw, err := a.atlasProvider.Results(ctx, observed.ID, m.ScheduledStart.Add(-time.Minute), now)
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	if len(results) > m.EstimatedSamples || len(results) < m.ResultCount {
		return a.atlasRefreshError(runID, itemID, now, errors.New("result count exceeds the reviewed bound or regressed"))
	}
	allowed := map[int64]bool{}
	for _, id := range m.ProbeIDs {
		allowed[id] = true
	}
	for _, row := range results {
		unknownTargetError := row.TargetAddress == "" && row.Error == "probe-error"
		if row.MeasurementID != observed.ID || !allowed[row.ProbeID] || row.AF != r.Spec.AF || row.Type != r.Spec.Type || !unknownTargetError && row.TargetAddress != m.TargetAddress {
			return a.atlasRefreshError(runID, itemID, now, errors.New("results do not match the selected probes, target or measurement type"))
		}
	}
	err = a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		item := atlasItem(r, itemID)
		if item == nil || !item.OwnershipConfirmed || item.ProviderID != observed.ID || item.IntentID != m.IntentID {
			return errors.New("Atlas result generation changed")
		}
		artifact, e := a.writeAtlasArtifact(s, "results", atlasResultFile{Results: results, Raw: resultsRaw})
		if e != nil {
			return e
		}
		removeAtlasArtifactMetadata(s, item.ResultsArtifactID)
		item.ResultsArtifactID = artifact
		item.ResultCount = len(results)
		return nil
	})
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	return a.cleanupAtlasArtifacts()
}
func (a *App) atlasRefreshError(runID, itemID string, now time.Time, cause error) error {
	err := a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		if r == nil {
			return errors.New("Atlas run unavailable")
		}
		m := atlasItem(r, itemID)
		if m == nil {
			return errors.New("Atlas measurement unavailable")
		}
		m.LastError = atlas.PublicError(cause)
		m.NextSyncAt = now.Add(time.Minute)
		if !m.OwnershipConfirmed {
			m.Status = "uncertain"
			r.ExecutionAuthorizedAt = time.Time{}
		}
		atlasRunStatus(r)
		return nil
	})
	return errors.Join(cause, err)
}
func (a *App) atlasStopOne(ctx context.Context, runID, itemID string, now time.Time) error {
	s := a.store.Snapshot()
	r := atlasRun(&s, runID)
	if r == nil || r.Pruned || r.ProviderOrigin != a.atlasProvider.Origin() {
		return errors.New("Atlas run unavailable")
	}
	m := atlasItem(r, itemID)
	if m == nil || !m.OwnershipConfirmed || m.ProviderID < 1 || m.ReceiptArtifactID == "" {
		return errors.New("Atlas stop requires this instance's verified creation evidence")
	}
	observed, _, err := a.atlasProvider.Measurement(ctx, m.ProviderID)
	if err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	if !atlas.MatchesIntent(observed, atlasSpec(*r, *m)) {
		return a.atlasRefreshError(runID, itemID, now, errors.New("Atlas stop identity mismatch"))
	}
	if observed.Status == "stopped" || observed.Status == "failed" || observed.Status == "archived" {
		return a.atlasRefreshOne(ctx, runID, itemID, now)
	}
	err = a.store.Update(func(s *State) error {
		r := atlasRun(s, runID)
		item := atlasItem(r, itemID)
		if item == nil || !item.OwnershipConfirmed || item.ProviderID != m.ProviderID {
			return errors.New("Atlas ownership changed")
		}
		item.Status = "stopping"
		item.StopAttemptedAt = now
		item.NextSyncAt = now.Add(time.Minute)
		atlasRunStatus(r)
		audit(s, r.ApprovedBy, "atlas-stop-intent", "atlas", m.ID)
		return nil
	})
	if err != nil {
		return err
	}
	if err = a.atlasProvider.Stop(ctx, m.ProviderID); err != nil {
		return a.atlasRefreshError(runID, itemID, now, err)
	}
	// DELETE acknowledgment alone is not proof of a terminal provider state.
	return a.atlasRefreshOne(ctx, runID, itemID, now)
}
