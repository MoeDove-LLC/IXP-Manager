package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

func atlasLimits() Record {
	return Record{"maxProbes": atlas.MaxProbes, "minIntervalSeconds": 300, "maxIntervalSeconds": 3600, "maxDurationSeconds": 86400, "maxSamples": atlas.MaxSamples, "maxEstimatedCredits": atlas.MaxEstimatedCredits, "maxActiveMeasurements": 100, "maxDailyEstimatedCredits": 100000, "maxMatrixMembers": 10, "maxMeasurementsPerRun": 90, "probeSnapshotSeconds": 3600, "approvalSeconds": 900}
}
func publicAtlasSnapshot(p AtlasProbeSnapshot, now time.Time) Record {
	return Record{"id": p.ID, "memberId": p.MemberID, "af": p.AF, "asn": p.ASN, "anchorOnly": p.AnchorOnly, "fetchedAt": p.FetchedAt, "count": p.Count, "checksum": p.Checksum, "truncated": p.Truncated, "stale": now.Sub(p.FetchedAt) > time.Hour}
}
func publicAtlasRun(r AtlasRun, detail bool) Record {
	hasResults := false
	remaining, remainingCredits, remainingSamples, uncertain := 0, 0, 0, 0
	items := []Record{}
	for _, m := range r.Items {
		if m.ResultCount > 0 {
			hasResults = true
		}
		if atlasUnattempted(m) {
			remaining++
			remainingCredits += m.EstimatedCredits
			remainingSamples += m.EstimatedSamples
		}
		if m.Status == "uncertain" || m.Status == "creating" {
			uncertain++
		}
		if detail {
			items = append(items, Record{"id": m.ID, "sourceMemberId": m.SourceMemberID, "targetMemberId": m.TargetMemberID, "targetAddress": m.TargetAddress, "probeIds": m.ProbeIDs, "status": m.Status, "providerId": m.ProviderID, "providerStatus": m.ProviderStatus, "ownershipConfirmed": m.OwnershipConfirmed, "estimatedCredits": m.EstimatedCredits, "estimatedSamples": m.EstimatedSamples, "lastSyncedAt": optionalTime(m.LastSyncedAt), "lastError": m.LastError, "hasResults": m.ResultCount > 0, "resultCount": m.ResultCount, "scheduledStart": optionalTime(m.ScheduledStart), "scheduledStop": optionalTime(m.ScheduledStop)})
		}
	}
	now := time.Now().UTC()
	expired := atlasApprovalExpired(r, now)
	out := Record{"id": r.ID, "name": r.Spec.Name, "mode": r.Spec.Mode, "vlanId": r.Spec.VLANID, "sourceMemberId": r.Spec.SourceMemberID, "targetMemberId": r.Spec.TargetMemberID, "type": r.Spec.Type, "af": r.Spec.AF, "isOneoff": r.Spec.IsOneoff, "intervalSeconds": r.Spec.IntervalSeconds, "durationSeconds": r.Spec.DurationSeconds, "startAt": optionalTime(r.Spec.StartAt), "isPublic": r.Spec.IsPublic, "status": r.Status, "checksum": r.Checksum, "estimatedCredits": r.EstimatedCredits, "estimatedSamples": r.EstimatedSamples, "remainingEstimatedCredits": remainingCredits, "remainingEstimatedSamples": remainingSamples, "approvalExpired": expired, "canReapprove": atlasRenewable(r, now), "resumeRequired": r.ResumeRequired, "createdAt": r.CreatedAt, "approvedAt": optionalTime(r.ApprovedAt), "measurementCount": len(r.Items), "remainingCount": remaining, "uncertainCount": uncertain, "hasResults": hasResults, "pruned": r.Pruned, "stopRequested": r.StopRequested, "canResume": remaining > 0 && !r.Pruned && !r.StopRequested && !r.ApprovedAt.IsZero() && !expired && (r.ResumeRequired || r.Status != "approved"), "notices": []string{"额度为固定3包/64字节规则的保守估算，不代表已消耗额度。", "测量结果可能延迟、丢失或有多个路径响应；无结果不表示健康。"}}
	if len(r.Items) == 1 {
		m := r.Items[0]
		out["targetAddress"] = m.TargetAddress
		out["probeIds"] = m.ProbeIDs
		out["providerId"] = m.ProviderID
		out["providerStatus"] = m.ProviderStatus
		out["lastSyncedAt"] = optionalTime(m.LastSyncedAt)
		out["lastError"] = m.LastError
	}
	if detail {
		out["spec"] = r.Spec
		out["items"] = items
	}
	return out
}
func (a *App) atlasRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	role := str(user, "role")
	if role != "admin" && role != "operator" && role != "viewer" {
		problem(w, 403, "Atlas requires a staff account")
		return
	}
	if session.API && (r.Method != "GET" || !contains(session.Scopes, "atlas:read")) {
		problem(w, 403, "Atlas API tokens are read-only and require atlas:read")
		return
	}
	if r.Method != "GET" && (session.API || role == "viewer") {
		problem(w, 403, "Atlas changes require a staff browser session")
		return
	}
	a.atlasMu.Lock()
	defer a.atlasMu.Unlock()
	if a.atlasClosed {
		problem(w, 503, "Atlas service is closed")
		return
	}
	roles := []string{"admin", "operator", "viewer"}
	if r.Method != "GET" && r.Method != "HEAD" {
		roles = []string{"admin", "operator"}
	}
	currentActor, actorErr := a.atlasActorCurrent(user, session, roles...)
	if actorErr != nil {
		problem(w, 403, actorErr.Error())
		return
	}
	user = currentActor
	role = str(user, "role")
	if r.Method != "GET" && r.Method != "HEAD" {
		defer func() { _ = a.cleanupAtlasArtifacts() }()
	}
	now := time.Now().UTC()
	if r.URL.Path == "/api/atlas" && r.Method == "GET" {
		s := a.store.Snapshot()
		runs := []Record{}
		for i := len(s.Atlas.Runs) - 1; i >= 0; i-- {
			runs = append(runs, publicAtlasRun(s.Atlas.Runs[i], false))
		}
		probes := []Record{}
		for _, p := range s.Atlas.ProbeSnapshots {
			probes = append(probes, publicAtlasSnapshot(p, now))
		}
		respond(w, 200, Record{"enabled": a.atlasProvider != nil, "provider": "ripe-atlas", "limits": atlasLimits(), "probeSnapshots": probes, "runs": runs})
		return
	}
	if r.URL.Path == "/api/atlas/probes" && r.Method == "GET" {
		af, _ := strconv.Atoi(r.URL.Query().Get("af"))
		member := r.URL.Query().Get("memberId")
		a.store.mu.RLock()
		s := &a.store.state
		p := atlasProbeSnapshot(s, member, af)
		var body atlasProbeFile
		var snapshot Record
		var err error
		if p != nil {
			snapshot = publicAtlasSnapshot(*p, now)
			err = a.readAtlasArtifactLocked(s, p.ID, &body)
		}
		a.store.mu.RUnlock()
		if p == nil {
			respond(w, 200, Record{"snapshot": nil, "items": []atlas.Probe{}, "stale": true})
			return
		}
		if err != nil {
			problem(w, 500, "Atlas probe snapshot integrity failed")
			return
		}
		respond(w, 200, Record{"snapshot": snapshot, "items": body.Probes, "stale": snapshot["stale"]})
		return
	}
	if r.URL.Path == "/api/atlas/probes/discover" && r.Method == "POST" {
		a.atlasDiscover(w, r, user, session, now)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/atlas/probes/") && r.Method == "DELETE" {
		a.atlasRemoveProbeSnapshot(w, r, user, session)
		return
	}
	if r.URL.Path == "/api/atlas/runs" && r.Method == "POST" {
		if a.atlasProvider == nil {
			problem(w, 503, "Atlas provider is disabled in local private configuration")
			return
		}
		var body AtlasProposal
		if !decode(w, r, &body) {
			return
		}
		var created AtlasRun
		err := a.store.Update(func(s *State) error {
			if len(s.Atlas.Runs) >= 1000 {
				return errors.New("Atlas run limit reached; prune and delete terminal history")
			}
			if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil {
				return err
			}
			p, items, base, credits, samples, err := a.atlasPlanLocked(s, body, now)
			if err != nil {
				return err
			}
			for i := range items {
				items[i].ID = token(16)
			}
			created = AtlasRun{ID: token(16), Spec: p, Items: items, Status: "draft", BaseChecksum: base, ProviderOrigin: a.atlasProvider.Origin(), EstimatedCredits: credits, EstimatedSamples: samples, CreatedAt: now, CreatedBy: str(user, "id")}
			created.Checksum = atlasRunChecksum(created)
			s.Atlas.Runs = append(s.Atlas.Runs, created)
			if err = validateAtlasState(s); err != nil {
				return err
			}
			audit(s, str(user, "id"), "atlas-proposal", "atlas", created.ID)
			return nil
		})
		if err != nil {
			if errors.Is(err, errAtlasAuthentication) {
				problem(w, 403, err.Error())
				return
			}
			problem(w, 400, err.Error())
			return
		}
		respond(w, 201, publicAtlasRun(created, true))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/atlas/runs/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/atlas/runs/") || len(parts) < 1 || len(parts) > 2 || !identifier.MatchString(parts[0]) {
		problem(w, 404, "Atlas route not found")
		return
	}
	s := a.store.Snapshot()
	run := atlasRun(&s, parts[0])
	if run == nil {
		problem(w, 404, "Atlas run not found")
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		respond(w, 200, publicAtlasRun(*run, true))
		return
	}
	if len(parts) == 2 && parts[1] == "results" && r.Method == "GET" {
		a.atlasResults(w, r, *run)
		return
	}
	if len(parts) == 1 && r.Method == "DELETE" {
		a.atlasRetain(w, r, user, session, *run, true)
		return
	}
	if len(parts) != 2 || r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	action := parts[1]
	if action == "prune" {
		a.atlasRetain(w, r, user, session, *run, false)
		return
	}
	localCancel := action == "stop" && atlasPurelyUnsent(*run)
	if !localCancel && (a.atlasProvider == nil || run.ProviderOrigin != a.atlasProvider.Origin()) {
		problem(w, 503, "Atlas provider is disabled or differs from this run's bound origin")
		return
	}
	if action == "refresh" {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		if err := a.atlasWorkOneRefresh(r.Context(), run.ID, now); err != nil { /* metadata retains a safe diagnostic */
		}
		s = a.store.Snapshot()
		respond(w, 200, publicAtlasRun(*atlasRun(&s, run.ID), true))
		return
	}
	if role != "admin" {
		problem(w, 403, "Atlas approval/execution/stop requires an administrator browser session")
		return
	}
	if action == "approve" {
		a.atlasApprove(w, r, user, session, *run, now)
		return
	}
	if action != "execute" && action != "resume" && action != "stop" {
		problem(w, 404, "Atlas action not found")
		return
	}
	var body struct {
		Checksum string `json:"checksum"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Checksum != run.Checksum {
		problem(w, 409, "Atlas review checksum changed")
		return
	}
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			return err
		}
		current := atlasRun(s, run.ID)
		if current == nil || current.Pruned || current.Checksum != body.Checksum || str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("Atlas authority or proposal changed")
		}
		if action == "stop" {
			current.StopRequested = true
			current.ExecutionAuthorizedAt = time.Time{}
			for i := range current.Items {
				if current.Items[i].Status == "approved" || current.Items[i].Status == "draft" {
					current.Items[i].Status = "cancelled"
				}
			}
		} else {
			if current.StopRequested || current.ApprovedAt.IsZero() || atlasApprovalExpired(*current, now) {
				return errors.New("Atlas approval expired or run already stopped")
			}
			if action == "execute" && (current.Status != "approved" || current.ResumeRequired) {
				return errors.New("use explicit resume only for unattempted remaining measurements")
			}
			if err := a.atlasCurrentLocked(s, current, now); err != nil {
				return err
			}
			remaining := 0
			for _, m := range current.Items {
				if m.Status == "approved" && atlasUnattempted(m) {
					remaining++
				}
			}
			if remaining == 0 {
				return errors.New("no unattempted approved measurements remain")
			}
			current.ExecutionAuthorizedAt = now
			if action == "resume" {
				current.ResumeRequired = false
			}
		}
		atlasRunStatus(current)
		audit(s, str(user, "id"), "atlas-"+action, "atlas", run.ID)
		return nil
	})
	if err != nil {
		if errors.Is(err, errAtlasAuthentication) {
			problem(w, 403, err.Error())
			return
		}
		problem(w, 409, err.Error())
		return
	}
	if !localCancel {
		_ = a.atlasWorkOne(r.Context(), run.ID, now)
	}
	s = a.store.Snapshot()
	respond(w, 202, publicAtlasRun(*atlasRun(&s, run.ID), true))
}
func atlasPurelyUnsent(run AtlasRun) bool {
	for _, m := range run.Items {
		if !m.AttemptedAt.IsZero() || m.IntentID != "" || m.ProviderID != 0 || m.OwnershipConfirmed {
			return false
		}
		if m.Status != "draft" && m.Status != "approved" && m.Status != "cancelled" {
			return false
		}
	}
	return true
}
func atlasUnattempted(m AtlasMeasurement) bool {
	return (m.Status == "draft" || m.Status == "approved") && m.AttemptedAt.IsZero() && m.IntentID == "" && m.ProviderID == 0 && !m.OwnershipConfirmed && m.ReceiptArtifactID == "" && m.StatusArtifactID == "" && m.ResultsArtifactID == ""
}
func atlasApprovalExpired(run AtlasRun, now time.Time) bool {
	return !run.ApprovedAt.IsZero() && (now.Sub(run.ApprovedAt) > 15*time.Minute || run.ApprovedAt.After(now.Add(30*time.Second)))
}
func atlasRenewable(run AtlasRun, now time.Time) bool {
	if run.ApprovedAt.IsZero() || run.StopRequested || run.Pruned || !run.Spec.StartAt.IsZero() && !run.Spec.StartAt.After(now) {
		return false
	}
	remaining := false
	for _, m := range run.Items {
		if m.Status == "uncertain" || m.Status == "creating" {
			return false
		}
		remaining = remaining || atlasUnattempted(m)
	}
	return remaining
}
func (a *App) atlasDiscover(w http.ResponseWriter, r *http.Request, user Record, session Session, now time.Time) {
	if a.atlasProvider == nil {
		problem(w, 503, "Atlas provider is disabled in local private configuration")
		return
	}
	var body struct {
		MemberID   string `json:"memberId"`
		AF         int    `json:"af"`
		AnchorOnly bool   `json:"anchorOnly"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.AF != 4 && body.AF != 6 {
		problem(w, 400, "Atlas address family must be4 or6")
		return
	}
	s := a.store.Snapshot()
	_, asn, err := atlasMember(&s, body.MemberID)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if _, err = a.atlasActorCurrent(user, session, "admin", "operator"); err != nil {
		problem(w, 403, err.Error())
		return
	}
	probes, raw, err := a.atlasProvider.Discover(r.Context(), asn, body.AF, body.AnchorOnly)
	if err != nil {
		problem(w, 502, atlas.PublicError(err))
		return
	}
	var snapshot AtlasProbeSnapshot
	var page struct {
		Next  *string `json:"next"`
		Count int     `json:"count"`
	}
	_ = json.Unmarshal(raw, &page)
	err = a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil {
			return err
		}
		_, current, e := atlasMember(s, body.MemberID)
		if e != nil || current != asn {
			return errors.New("Atlas member scope changed during discovery")
		}
		role := str(find(s, "users", str(user, "id")), "role")
		if role != "admin" && role != "operator" {
			return errors.New("Atlas discovery permission changed")
		}
		id, e := a.writeAtlasArtifact(s, "probes", atlasProbeFile{Probes: probes, Raw: raw})
		if e != nil {
			return e
		}
		file := atlasArtifact(s, id)
		snapshot = AtlasProbeSnapshot{ID: id, MemberID: body.MemberID, AF: body.AF, ASN: asn, AnchorOnly: body.AnchorOnly, FetchedAt: now, Count: len(probes), Truncated: page.Next != nil && *page.Next != "" || page.Count > len(probes), Checksum: file.Checksum}
		if previous := atlasProbeSnapshot(s, body.MemberID, body.AF); previous != nil {
			removeAtlasArtifactMetadata(s, previous.ID)
			*previous = snapshot
		} else {
			s.Atlas.ProbeSnapshots = append(s.Atlas.ProbeSnapshots, snapshot)
		}
		if e = validateAtlasState(s); e != nil {
			return e
		}
		audit(s, str(user, "id"), "atlas-discover", "members", body.MemberID)
		return nil
	})
	if err != nil {
		if errors.Is(err, errAtlasAuthentication) {
			problem(w, 403, err.Error())
			return
		}
		problem(w, 409, "Atlas probe discovery could not be saved; check member scope and private storage")
		return
	}
	_ = a.cleanupAtlasArtifacts()
	respond(w, 200, Record{"snapshot": publicAtlasSnapshot(snapshot, now), "items": probes, "stale": false})
}
func (a *App) atlasApprove(w http.ResponseWriter, r *http.Request, user Record, session Session, run AtlasRun, now time.Time) {
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry later")
		return
	}
	var body struct {
		Checksum           string `json:"checksum"`
		AcknowledgeCredits bool   `json:"acknowledgeCredits"`
		AcknowledgePublic  bool   `json:"acknowledgePublic"`
		CurrentPassword    string `json:"currentPassword"`
		Code               string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	authenticated := false
	err := a.reauth(str(user, "id"), body.CurrentPassword, body.Code, func(s *State, _ *SecurityUser) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			return err
		}
		authenticated = true
		current := atlasRun(s, run.ID)
		if current == nil || current.Pruned || current.StopRequested || current.Checksum != body.Checksum || str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("Atlas proposal or administrator authority changed")
		}
		reviewNow := now
		if actual := time.Now().UTC(); actual.After(reviewNow) {
			reviewNow = actual
		}
		renew := !current.ApprovedAt.IsZero()
		if renew && !atlasRenewable(*current, reviewNow) {
			return errors.New("only unattempted remaining items may be reapproved; reconcile uncertain items first, or create a new proposal if its fixed start time has elapsed")
		}
		if !renew && current.Status != "draft" {
			return errors.New("only a draft or unattempted remainder can be approved")
		}
		if !body.AcknowledgeCredits || current.Spec.IsPublic && !body.AcknowledgePublic {
			return errors.New("explicit total credits and public disclosure acknowledgment required")
		}
		if err := a.atlasCurrentLocked(s, current, reviewNow); err != nil {
			return err
		}
		current.ApprovedAt = reviewNow
		current.ApprovedBy = str(user, "id")
		current.ExecutionAuthorizedAt = time.Time{}
		current.ResumeRequired = renew
		remainingCredits, remainingSamples := 0, 0
		for i := range current.Items {
			if atlasUnattempted(current.Items[i]) {
				current.Items[i].Status = "approved"
				current.Items[i].LastError = ""
				remainingCredits += current.Items[i].EstimatedCredits
				remainingSamples += current.Items[i].EstimatedSamples
			} else if !renew {
				return errors.New("draft contains an already-attempted item")
			}
		}
		atlasRunStatus(current)
		action := "atlas-approve"
		if renew {
			action = "atlas-reapprove"
		}
		auditDetails(s, str(user, "id"), action, "atlas", run.ID, Record{"checksum": current.Checksum, "estimatedCredits": remainingCredits, "estimatedSamples": remainingSamples, "totalEstimatedCredits": current.EstimatedCredits, "totalEstimatedSamples": current.EstimatedSamples, "isPublic": current.Spec.IsPublic})
		return nil
	})
	if err != nil {
		code := 403
		if authenticated {
			code = 409
		}
		problem(w, code, err.Error())
		return
	}
	s := a.store.Snapshot()
	respond(w, 200, publicAtlasRun(*atlasRun(&s, run.ID), true))
}
