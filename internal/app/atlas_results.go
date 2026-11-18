package app

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

func (a *App) atlasWorkOneRefresh(ctx context.Context, runID string, now time.Time) error {
	s := a.store.Snapshot()
	r := atlasRun(&s, runID)
	if r == nil || r.Pruned {
		return errors.New("Atlas run unavailable")
	}
	var selected *AtlasMeasurement
	for i := range r.Items {
		m := &r.Items[i]
		if m.IntentID != "" && (selected == nil || m.LastSyncedAt.Before(selected.LastSyncedAt)) {
			selected = m
		}
	}
	if selected == nil {
		return nil
	}
	return a.atlasRefreshOne(ctx, runID, selected.ID, now)
}
func (a *App) atlasResults(w http.ResponseWriter, r *http.Request, run AtlasRun) {
	if run.Pruned {
		problem(w, 410, "Atlas result snapshots were explicitly pruned")
		return
	}
	want := r.URL.Query().Get("measurementId")
	if want != "" && atlasItem(&run, want) == nil {
		problem(w, 404, "Atlas child measurement not found")
		return
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		problem(w, 503, "store is closed")
		return
	}
	all := []atlas.Result{}
	directions := []Record{}
	snapshots := []Record{}
	var single any
	vlanAddresses := map[string]bool{}
	if run.Spec.VLANID != "" {
		for _, c := range a.store.state.Resources["connections"] {
			if str(c, "vlanId") == run.Spec.VLANID {
				for _, key := range []string{"ipv4", "ipv6"} {
					if ip := str(c, key); ip != "" {
						if address, err := netip.ParseAddr(ip); err == nil {
							vlanAddresses[address.String()] = true
						}
					}
				}
			}
		}
	}
	for _, m := range run.Items {
		if want != "" && m.ID != want {
			continue
		}
		rows := []atlas.Result{}
		var latest time.Time
		seen := map[string]bool{}
		if m.ResultsArtifactID != "" {
			var body atlasResultFile
			if err := a.readAtlasArtifactLocked(&a.store.state, m.ResultsArtifactID, &body); err != nil {
				problem(w, 500, "Atlas result snapshot integrity failed")
				return
			}
			rows = body.Results
			f := atlasArtifact(&a.store.state, m.ResultsArtifactID)
			snapshot := Record{"id": f.ID, "measurementId": m.ID, "checksum": f.Checksum, "fetchedAt": f.CreatedAt, "count": len(rows)}
			snapshots = append(snapshots, snapshot)
			single = snapshot
		}
		for _, row := range rows {
			if row.Timestamp.After(latest) {
				latest = row.Timestamp
			}
			for _, hop := range row.Hops {
				for _, reply := range hop.Replies {
					if vlanAddresses[reply.Address] {
						seen[reply.Address] = true
					}
				}
			}
		}
		addresses := []string{}
		for address := range seen {
			addresses = append(addresses, address)
		}
		sort.Strings(addresses)
		directions = append(directions, Record{"id": m.ID, "providerId": m.ProviderID, "sourceMemberId": m.SourceMemberID, "targetMemberId": m.TargetMemberID, "status": m.Status, "resultCount": len(rows), "hasResults": len(rows) > 0, "lastObservedAt": optionalTime(latest), "currentVLANAddressesSeen": addresses})
		all = append(all, rows...)
	}
	if len(snapshots) != 1 {
		single = nil
	}
	respond(w, 200, Record{"items": all, "directions": directions, "snapshots": snapshots, "snapshot": single, "hasResults": len(all) > 0, "truncated": false, "status": run.Status})
}
func (a *App) atlasRetain(w http.ResponseWriter, r *http.Request, user Record, session Session, run AtlasRun, remove bool) {
	if str(user, "role") != "admin" {
		problem(w, 403, "Atlas retention requires an administrator browser session")
		return
	}
	var body struct {
		Checksum    string `json:"checksum"`
		Reason      string `json:"reason"`
		Acknowledge bool   `json:"acknowledge"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Checksum != run.Checksum || !body.Acknowledge || len(body.Reason) < 3 || len(body.Reason) > 500 || strings.ContainsAny(body.Reason, "\r\n\x00") {
		problem(w, 400, "exact checksum, retention reason and explicit acknowledgment required")
		return
	}
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			return err
		}
		current := atlasRun(s, run.ID)
		if current == nil || current.Checksum != body.Checksum || str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("Atlas retention authority or version changed")
		}
		for _, m := range current.Items {
			if !atlasTerminal(m.Status) {
				return errors.New("stop and verify every child measurement before pruning; uncertain creations cannot be discarded")
			}
		}
		if remove {
			if !current.Pruned {
				return errors.New("prune snapshots before deleting terminal metadata")
			}
			auditDetails(s, str(user, "id"), "atlas-delete-history", "atlas", run.ID, Record{"checksum": current.Checksum, "spec": current.Spec, "estimatedCredits": current.EstimatedCredits, "reason": body.Reason})
			for i := range s.Atlas.Runs {
				if s.Atlas.Runs[i].ID == run.ID {
					s.Atlas.Runs = append(s.Atlas.Runs[:i], s.Atlas.Runs[i+1:]...)
					break
				}
			}
			return nil
		}
		for i := range current.Items {
			m := &current.Items[i]
			for _, id := range []string{m.ReceiptArtifactID, m.StatusArtifactID, m.ResultsArtifactID} {
				removeAtlasArtifactMetadata(s, id)
			}
			m.ReceiptArtifactID, m.StatusArtifactID, m.ResultsArtifactID = "", "", ""
			m.ResultCount = 0
		}
		current.Pruned = true
		current.PrunedAt = time.Now().UTC()
		atlasRunStatus(current)
		auditDetails(s, str(user, "id"), "atlas-prune", "atlas", run.ID, Record{"checksum": current.Checksum, "reason": body.Reason})
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
	if err = a.cleanupAtlasArtifacts(); err != nil {
		problem(w, 500, "retention marker saved; private snapshot cleanup will resume on restart")
		return
	}
	respond(w, 200, Record{"ok": true})
}

func (a *App) atlasRemoveProbeSnapshot(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if str(user, "role") != "admin" {
		problem(w, 403, "Atlas cache retention requires an administrator browser session")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/atlas/probes/")
	if !identifier.MatchString(id) {
		problem(w, 404, "Atlas probe snapshot not found")
		return
	}
	var body struct {
		Checksum    string `json:"checksum"`
		Reason      string `json:"reason"`
		Acknowledge bool   `json:"acknowledge"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if !body.Acknowledge || len(body.Reason) < 3 || len(body.Reason) > 500 || strings.ContainsAny(body.Reason, "\r\n\x00") {
		problem(w, 400, "retention reason and acknowledgment required")
		return
	}
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			return err
		}
		if str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("Atlas cache retention authority changed")
		}
		for i, p := range s.Atlas.ProbeSnapshots {
			if p.ID == id {
				if p.Checksum != body.Checksum {
					return errors.New("probe snapshot checksum changed")
				}
				removeAtlasArtifactMetadata(s, id)
				s.Atlas.ProbeSnapshots = append(s.Atlas.ProbeSnapshots[:i], s.Atlas.ProbeSnapshots[i+1:]...)
				auditDetails(s, str(user, "id"), "atlas-prune-probes", "atlas", id, Record{"memberId": p.MemberID, "af": p.AF, "checksum": p.Checksum, "reason": body.Reason})
				return nil
			}
		}
		return errors.New("Atlas probe snapshot not found")
	})
	if err != nil {
		if errors.Is(err, errAtlasAuthentication) {
			problem(w, 403, err.Error())
			return
		}
		problem(w, 409, err.Error())
		return
	}
	if err = a.cleanupAtlasArtifacts(); err != nil {
		problem(w, 500, "cache marker saved; cleanup resumes on restart")
		return
	}
	respond(w, 200, Record{"ok": true})
}
