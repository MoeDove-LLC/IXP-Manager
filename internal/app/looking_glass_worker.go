package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/lookingglass"
)

func lookingGlassWorker(session Session, user Record, routerID string) bool {
	return session.API && contains(session.Scopes, "looking-glass:run") && contains(session.RouterIDs, routerID) && (str(user, "role") == "admin" || str(user, "role") == "operator")
}
func lookingGlassLeaseOwner(session Session) string {
	return digest(session.UserID + "\x00" + session.TokenID)
}

func (a *App) claimLookingGlass(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	var body lookingglass.ClaimRequest
	if !decode(w, r, &body) {
		return
	}
	if !lookingGlassWorker(session, user, body.RouterID) {
		problem(w, 403, "router-bound looking-glass runner token required")
		return
	}
	var lease *lookingglass.Lease
	err := a.store.Update(func(s *State) error {
		actor, err := lookingGlassActor(s, user, session)
		if err != nil {
			return err
		}
		if !lookingGlassWorker(session, actor, body.RouterID) {
			return errors.New("runner permission changed")
		}
		if find(s, "routers", body.RouterID) == nil {
			return errors.New("router not found")
		}
		now := time.Now().UTC()
		expireLookingGlass(s, now)
		for _, q := range s.LookingGlassQueries {
			if q.Query.RouterID == body.RouterID && q.Status == "running" && q.LeaseUntil.After(now) {
				return nil
			}
		}
		for i := range s.LookingGlassQueries {
			q := &s.LookingGlassQueries[i]
			if q.Query.RouterID != body.RouterID || q.Status != "queued" {
				continue
			}
			if !lookingGlassBound(s, *q) {
				q.Status = "failed"
				q.ErrorCode = "target-changed"
				q.CompletedAt = now
				q.Revision = token(16)
				continue
			}
			requester := find(s, "users", q.RequesterID)
			if requester == nil || str(requester, "role") == "member" && (!contains(membershipIDs(requester), q.MemberID) || !lookingGlassMemberEligible(s, find(s, "peers", q.Query.PeerID))) {
				q.Status = "cancelled"
				q.CompletedAt = now
				q.ErrorCode = "request-access-changed"
				q.Revision = token(16)
				continue
			}
			raw := token(32)
			until := now.Add(lookingGlassLeaseTTL)
			if until.After(q.ExpiresAt) {
				until = q.ExpiresAt
			}
			q.Status = "running"
			q.StartedAt = now
			q.Attempts++
			q.LeaseHash = digest(raw)
			q.LeaseOwner = lookingGlassLeaseOwner(session)
			q.LeaseUntil = until
			q.Revision = token(16)
			// Member visibility is a capture-time fact, not a later reassignment.
			q.MemberVisible = q.MemberVisible && lookingGlassMemberEligible(s, find(s, "peers", q.Query.PeerID))
			lease = &lookingglass.Lease{ID: q.ID, Token: raw, ExpiresAt: until, Query: q.Query}
			break
		}
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, lookingglass.ClaimResponse{Lease: lease})
}

func (a *App) completeLookingGlass(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	var body lookingglass.Completion
	if !decode(w, r, &body) {
		return
	}
	body.Result.ObservedAt = body.Result.ObservedAt.UTC()
	if !lookingGlassWorker(session, user, body.Result.Query.RouterID) {
		problem(w, 403, "router-bound looking-glass runner token required")
		return
	}
	if len(body.LeaseToken) > 128 || !identifier.MatchString(body.LeaseID) {
		problem(w, 400, "invalid diagnostic lease")
		return
	}
	if err := lookingglass.ValidateResult(body.Result, body.Result.Query); err != nil {
		problem(w, 422, "invalid typed diagnostic result")
		return
	}
	raw, err := json.Marshal(body.Result)
	if err != nil || len(raw) > 4<<20 {
		problem(w, 413, "diagnostic result exceeds 4 MiB")
		return
	}
	checksum := digest(string(raw))
	status := 200
	message := ""
	duplicate := false
	attemptedArtifact := false
	err = a.store.Update(func(s *State) error {
		actor, err := lookingGlassActor(s, user, session)
		if err != nil {
			status = 403
			return err
		}
		if !lookingGlassWorker(session, actor, body.Result.Query.RouterID) {
			status = 403
			return errors.New("runner permission changed")
		}
		q := lookingGlassByID(s, body.LeaseID)
		if q == nil {
			status = 404
			return errors.New("query not found")
		}
		if q.Query != body.Result.Query || q.LeaseOwner != lookingGlassLeaseOwner(session) || q.LeaseHash == "" || q.LeaseHash != digest(body.LeaseToken) {
			status = 409
			return errors.New("diagnostic lease or target changed")
		}
		if lookingGlassTerminal(q.Status) {
			if !q.Pruned && q.ResultChecksum == checksum {
				duplicate = true
				return nil
			}
			status = 409
			return errors.New("diagnostic request is already terminal")
		}
		now := time.Now().UTC()
		if q.Status != "running" || !q.LeaseUntil.After(now) || !q.ExpiresAt.After(now) {
			status = 409
			return errors.New("diagnostic lease expired")
		}
		if !lookingGlassBound(s, *q) {
			q.Status = "failed"
			q.ErrorCode = "target-changed"
			q.CompletedAt = now
			q.Revision = token(16)
			q.LeaseHash = ""
			q.LeaseOwner = ""
			status = 409
			message = "diagnostic target changed during collection"
			return nil
		}
		if body.Result.ObservedAt.IsZero() || body.Result.ObservedAt.Before(q.StartedAt.Add(-30*time.Second)) || body.Result.ObservedAt.After(now.Add(30*time.Second)) {
			status = 422
			return errors.New("diagnostic sample timestamp is outside the lease capture window")
		}
		if err := lookingglass.ValidateResult(body.Result, q.Query); err != nil {
			status = 422
			return errors.New("diagnostic result does not prove the leased target")
		}
		q.ResultStatus = body.Result.Status
		q.ResultChecksum = checksum
		q.CompletedAt = now
		q.Revision = token(16)
		q.Complete = body.Result.Complete
		q.Truncated = body.Result.Truncated
		q.RouteCount = len(body.Result.Routes)
		q.ErrorCode = body.Result.ErrorCode
		if body.Result.Status == "ok" || body.Result.Status == "truncated" {
			attemptedArtifact = true
			file, err := a.writeEncryptedArtifactLocked("looking-glass", q.ID, checksum, string(raw))
			if err != nil {
				status = 500
				return errors.New("could not persist diagnostic result")
			}
			q.File = file
			q.ObservedAt = body.Result.ObservedAt
			q.Status = "complete"
		} else if body.Result.Status == "unsupported" {
			q.Status = "unsupported"
		} else {
			q.Status = "failed"
		}
		return nil
	})
	if err != nil {
		if attemptedArtifact {
			a.store.mu.Lock()
			ids := []string{}
			for _, q := range a.store.state.LookingGlassQueries {
				if q.File.Version != 0 {
					ids = append(ids, q.ID)
				}
			}
			_ = a.cleanupEncryptedArtifactOrphansLocked("looking-glass", ids)
			a.store.mu.Unlock()
		}
		if status == 200 {
			problem(w, 500, "could not commit diagnostic result")
			return
		}
		problem(w, status, err.Error())
		return
	}
	if status != 200 {
		problem(w, status, message)
		return
	}
	respond(w, 200, Record{"ok": true, "duplicate": duplicate})
}

func validateLookingGlassState(s *State) error {
	if len(s.LookingGlassQueries) > 2000 {
		return errors.New("diagnostic history exceeds supported limit")
	}
	seen := map[string]bool{}
	for _, q := range s.LookingGlassQueries {
		if !identifier.MatchString(q.ID) || seen[q.ID] || !credentialRevision.MatchString(q.Revision) || !identifier.MatchString(q.RequesterID) {
			return errors.New("invalid diagnostic request identity")
		}
		seen[q.ID] = true
		canonical, err := lookingglass.NormalizeQuery(q.Query)
		if err != nil || canonical != q.Query {
			return errors.New("invalid persisted diagnostic query")
		}
		if q.Status != "queued" && q.Status != "running" && !lookingGlassTerminal(q.Status) {
			return errors.New("invalid diagnostic lifecycle status")
		}
		if q.CreatedAt.IsZero() || q.ExpiresAt.Before(q.CreatedAt) || q.Attempts < 0 || q.Attempts > 3 || q.RouteCount < 0 || q.RouteCount > q.Query.MaxRows {
			return errors.New("invalid diagnostic limits or timestamps")
		}
		if q.Query.PeerID != "" && !identifier.MatchString(q.MemberID) || q.MemberVisible && q.Query.PeerID == "" {
			return errors.New("invalid diagnostic member binding")
		}
		if q.Status == "running" && (!externalDigest(q.LeaseHash) || !externalDigest(q.LeaseOwner) || q.StartedAt.IsZero() || q.LeaseUntil.IsZero() || q.LeaseUntil.After(q.ExpiresAt)) {
			return errors.New("invalid diagnostic runner lease")
		}
		if q.ResultChecksum != "" && !externalDigest(q.ResultChecksum) {
			return errors.New("invalid diagnostic result checksum")
		}
		if q.File.Version != 0 && (q.File.Version != 1 || q.File.Size < 1 || q.File.Size > encryptedArtifactCipherLimit || !externalDigest(q.File.Checksum) || !externalDigest(q.ResultChecksum) || q.Status != "complete") {
			return errors.New("invalid diagnostic artifact metadata")
		}
		if q.Status == "complete" && (q.File.Version != 1 || q.ObservedAt.IsZero() || (q.ResultStatus != "ok" && q.ResultStatus != "truncated")) {
			return errors.New("complete diagnostic result is missing its artifact")
		}
		if q.Pruned && (!lookingGlassTerminal(q.Status) || q.PrunedAt.IsZero()) {
			return errors.New("invalid diagnostic pruning marker")
		}
	}
	return nil
}
func ValidateLookingGlassArtifacts(store *Store) error {
	return (&App{store: store}).initLookingGlass()
}
func (a *App) initLookingGlass() error {
	if err := a.initEncryptedArtifactDir("looking-glass"); err != nil {
		return err
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if a.store.closed {
		return errors.New("store is closed")
	}
	if err := validateLookingGlassState(&a.store.state); err != nil {
		return err
	}
	ids := []string{}
	for _, q := range a.store.state.LookingGlassQueries {
		if q.File.Version == 0 {
			continue
		}
		ids = append(ids, q.ID)
		if q.Pruned {
			if _, err := a.removeEncryptedArtifactLocked("looking-glass", q.ID, q.ResultChecksum, q.File); err != nil {
				return err
			}
		} else if _, err := a.readLookingGlassLocked(q); err != nil {
			return err
		}
	}
	return a.cleanupEncryptedArtifactOrphansLocked("looking-glass", ids)
}
func (a *App) readLookingGlassLocked(q LookingGlassQuery) (lookingglass.Result, error) {
	var result lookingglass.Result
	if q.Pruned || q.File.Version == 0 {
		return result, errors.New("diagnostic result is unavailable")
	}
	content, err := a.readEncryptedArtifactLocked("looking-glass", q.ID, q.ResultChecksum, q.File)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, errors.New("invalid stored diagnostic result")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return result, errors.New("stored diagnostic result contains trailing data")
	}
	if err := lookingglass.ValidateResult(result, q.Query); err != nil {
		return result, errors.New("stored diagnostic target/shape mismatch")
	}
	if !result.ObservedAt.Equal(q.ObservedAt) || result.Status != q.ResultStatus || len(result.Routes) != q.RouteCount || result.Complete != q.Complete || result.Truncated != q.Truncated {
		return result, errors.New("diagnostic metadata does not match its snapshot")
	}
	return result, nil
}
func (a *App) lookingGlassResult(w http.ResponseWriter, r *http.Request, s *State, q LookingGlassQuery, user Record) {
	if q.Pruned {
		respond(w, 410, Record{"query": lookingGlassPublic(q, user, time.Now().UTC()), "error": "diagnostic snapshot was explicitly pruned"})
		return
	}
	if q.Status != "complete" {
		problem(w, 409, "diagnostic query has no completed route snapshot")
		return
	}
	a.store.mu.RLock()
	result, err := a.readLookingGlassLocked(q)
	a.store.mu.RUnlock()
	if err != nil {
		problem(w, 500, "diagnostic result failed integrity checks")
		return
	}
	now := time.Now().UTC()
	age := lookingGlassAge(q, now).Seconds()
	state := "fresh"
	if age > lookingGlassFreshFor.Seconds() || !lookingGlassBound(s, q) {
		state = "stale"
	}
	respond(w, 200, Record{"query": lookingGlassPublic(q, user, now), "result": result, "freshness": Record{"status": state, "observedAt": q.ObservedAt, "ageSeconds": age, "staleAfterSeconds": 30}})
}

func (a *App) retainLookingGlass(w http.ResponseWriter, r *http.Request, user Record, session Session, initial LookingGlassQuery, remove bool) {
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "browser administrator required for diagnostic retention")
		return
	}
	var body struct {
		ExpectedRevision string `json:"expectedRevision"`
		Acknowledge      bool   `json:"acknowledge"`
		Reason           string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if !body.Acknowledge || len(body.Reason) > 1000 || strings.ContainsRune(body.Reason, 0) || (remove && body.Reason == "") {
		problem(w, 400, "acknowledgement and a deletion reason are required")
		return
	}
	var result LookingGlassQuery
	err := a.store.Update(func(s *State) error {
		actor, err := lookingGlassActor(s, user, session)
		if err != nil {
			return err
		}
		if str(actor, "role") != "admin" {
			return errors.New("administrator permission changed")
		}
		expireLookingGlass(s, time.Now().UTC())
		for i := range s.LookingGlassQueries {
			q := &s.LookingGlassQueries[i]
			if q.ID != initial.ID {
				continue
			}
			if q.Revision != body.ExpectedRevision || !lookingGlassTerminal(q.Status) {
				return errors.New("query changed or is still pending")
			}
			details := lookingGlassPublic(*q, actor, time.Now().UTC())
			details["reason"] = body.Reason
			if remove {
				if !q.Pruned {
					return errors.New("prune the snapshot before deleting its metadata")
				}
				if q.File.Version != 0 {
					if _, err := a.removeEncryptedArtifactLocked("looking-glass", q.ID, q.ResultChecksum, q.File); err != nil {
						return err
					}
				}
				auditDetails(s, str(actor, "id"), "delete-query-metadata", "looking-glass", q.ID, details)
				result = *q
				s.LookingGlassQueries = append(s.LookingGlassQueries[:i], s.LookingGlassQueries[i+1:]...)
				return nil
			}
			if !q.Pruned {
				if q.File.Version != 0 {
					if _, err := a.readLookingGlassLocked(*q); err != nil {
						return err
					}
				}
				q.Pruned = true
				q.PrunedAt = time.Now().UTC()
				q.Revision = token(16)
				auditDetails(s, str(actor, "id"), "prune-query", "looking-glass", q.ID, details)
			}
			result = *q
			return nil
		}
		return errors.New("query not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if !remove && result.File.Version != 0 {
		a.store.mu.Lock()
		_, err = a.removeEncryptedArtifactLocked("looking-glass", result.ID, result.ResultChecksum, result.File)
		a.store.mu.Unlock()
		if err != nil {
			problem(w, 500, "pruning recorded; file removal will resume after recovery")
			return
		}
	}
	if remove {
		respond(w, 200, Record{"ok": true, "auditPreserved": true})
	} else {
		respond(w, 200, lookingGlassPublic(result, user, time.Now().UTC()))
	}
}
