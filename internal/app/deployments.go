package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

func (a *App) deployments(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	role := str(user, "role")
	if role == "member" {
		problem(w, 403, "staff access required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/deployments"), "/")
	if len(parts) == 3 && parts[2] == "routeros-handoff" {
		if r.Method != http.MethodGet {
			problem(w, 405, "method not allowed")
			return
		}
		a.routerOSHandoff(w, r, user, session, parts[1])
		return
	}
	state := a.store.Snapshot()
	if len(parts) == 1 && parts[0] == "" {
		if r.Method == "GET" {
			items := []Record{}
			for i := len(state.Deployments) - 1; i >= 0; i-- {
				items = append(items, a.deploymentMetadata(state.Deployments[i]))
			}
			respond(w, 200, Record{"items": items})
			return
		}
		if r.Method != "POST" || (role != "admin" && role != "operator") {
			problem(w, 403, "staff write access required")
			return
		}
		var body struct {
			RouterID string `json:"routerId"`
		}
		if !decode(w, r, &body) {
			return
		}
		var d Deployment
		status := 400
		err := a.store.Update(func(s *State) error {
			if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil {
				status = 401
				return errors.New("deployment authority changed")
			}
			if len(s.Deployments) >= deploymentCountLimit {
				return errors.New("deployment history quota reached; prune and archive older revisions")
			}
			config, err := a.renderRouter(s, body.RouterID)
			if err != nil {
				return err
			}
			d = Deployment{ID: token(16), RouterID: body.RouterID, Platform: routerPlatform(find(s, "routers", body.RouterID)), Status: "draft", Checksum: routing.Checksum(config), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			d.RouterRole = routerRole(find(s, "routers", body.RouterID))
			if d.RouterRole == routing.RoleAS112 {
				r := find(s, "routers", body.RouterID)
				d.AS112Service = str(r, "as112Service")
				d.AS112IPv4, _ = r["as112IPv4"].(bool)
				d.AS112IPv6, _ = r["as112IPv6"].(bool)
				d.AS112NextHop4, d.AS112NextHop6 = str(r, "as112NextHop4"), str(r, "as112NextHop6")
			}
			d.CollectorPolicy = routerCollectorPolicy(find(s, "routers", body.RouterID))
			if d.RouterRole != routing.RoleAS112 && routing.RFC1997ControlSupported(d.Platform) {
				value := d.Platform == "openbgpd"
				d.RFC1997Mode = "native"
				if explicit, ok := find(s, "routers", body.RouterID)["rfc1997Passthrough"].(bool); ok {
					value = explicit
					d.RFC1997Mode = "explicit"
				}
				d.RFC1997Passthrough = &value
			}
			d.CredentialRevisions, d.CredentialPresence = deploymentCredentialSnapshot(s, body.RouterID)
			if err = a.writeDeploymentArtifact(&d, config); err != nil {
				return err
			}
			s.Deployments = append(s.Deployments, d)
			audit(s, str(user, "id"), "preview", "deployments", d.ID)
			return nil
		})
		if err != nil {
			if status == 401 {
				problem(w, status, "deployment authority changed; sign in again")
			} else {
				problem(w, status, "could not create deployment; check current routing inputs and artifact storage")
			}
			return
		}
		respond(w, 201, a.deploymentMetadata(d))
		return
	}
	if len(parts) < 2 || len(parts) > 3 {
		problem(w, 404, "deployment not found")
		return
	}
	var d Deployment
	found := false
	for _, candidate := range state.Deployments {
		if candidate.ID == parts[1] {
			d = candidate
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "deployment not found")
		return
	}
	if len(parts) == 3 && parts[2] == "readiness" && r.Method == "GET" {
		a.deploymentReadinessView(w, r, user, session, d.ID)
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		view, err := a.deploymentDetail(d)
		if err != nil {
			problem(w, 500, "deployment preview failed integrity or redaction checks")
			return
		}
		if d.Pruned {
			respond(w, 410, view)
		} else {
			respond(w, 200, view)
		}
		return
	}
	if len(parts) == 3 && parts[2] == "diff" && r.Method == "GET" {
		if d.Pruned {
			respond(w, 410, a.deploymentMetadata(d))
			return
		}
		diff, err := a.deploymentDiff(&state, d)
		if err != nil {
			problem(w, 500, "safe deployment diff unavailable")
			return
		}
		respond(w, 200, diff)
		return
	}
	if len(parts) == 3 && parts[2] == "config" && r.Method == "GET" {
		if role != "admin" && role != "operator" {
			problem(w, 403, "deployment download requires an administrator or operator")
			return
		}
		if session.API && (!contains(session.Scopes, "deployments:pull") || !contains(session.RouterIDs, d.RouterID)) {
			problem(w, 403, "router-bound deployments:pull token required")
			return
		}
		if d.Pruned {
			problem(w, 410, "deployment artifact has been explicitly pruned")
			return
		}
		if d.Status != "approved" {
			problem(w, 409, "approve the exact reviewed revision before downloading")
			return
		}
		current, config, status, err := a.currentDeploymentDownload(r, user, session, d)
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		d = current
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.%s"`, d.Platform, d.ID, extension(d.Platform)))
		w.Header().Set("X-Config-SHA256", d.Checksum)
		w.Header().Set("X-Config-Platform", d.Platform)
		w.Header().Set("X-Config-Router-ID", d.RouterID)
		w.Header().Set("X-Config-Revision", d.ID)
		_, _ = w.Write([]byte(config))
		return
	}
	if len(parts) == 2 && r.Method == "DELETE" {
		a.deploymentRetention(w, r, user, session, d, true)
		return
	}
	if r.Method != "POST" || len(parts) != 3 {
		problem(w, 405, "method not allowed")
		return
	}
	action := parts[2]
	if action == "prune" {
		a.deploymentRetention(w, r, user, session, d, false)
		return
	}
	if action != "validate" && action != "approve" {
		problem(w, 404, "unknown deployment action")
		return
	}
	if role != "admin" && role != "operator" {
		problem(w, 403, "staff write access required")
		return
	}
	if action == "approve" && (role != "admin" || session.API) {
		problem(w, 403, "browser administrator review required")
		return
	}
	if d.Pruned {
		problem(w, 410, "artifact unavailable")
		return
	}
	if action == "approve" {
		var body struct {
			Checksum string `json:"checksum"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Checksum != a.deploymentReviewToken(d) {
			problem(w, 409, "review identifier no longer matches this revision")
			return
		}
	}
	if action == "validate" {
		if d.Status != "draft" {
			problem(w, 409, "only drafts can be validated")
			return
		}
		config, err := a.readDeploymentArtifact(d)
		if err != nil {
			problem(w, 500, "artifact integrity check failed")
			return
		}
		current, err := a.renderRouter(&state, d.RouterID)
		if err != nil || routing.Checksum(current) != d.Checksum {
			problem(w, 409, "routing data, authentication or generation rules changed; regenerate before validating")
			return
		}
		binary, checkerErr := a.routerValidatorBinaryForConfig(d.Platform, config)
		if checkerErr != nil {
			problem(w, 422, "reviewed artifact does not bind a supported platform address family")
			return
		}
		if err = routing.ValidateFor(r.Context(), d.Platform, binary, config); err != nil {
			problem(w, 422, "platform preflight failed; verify the configured validator and platform version")
			return
		}
	}
	status := 409
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin", "operator")
		if err != nil {
			status = 401
			return errors.New("deployment authority changed; sign in again")
		}
		for i := range s.Deployments {
			current := &s.Deployments[i]
			if current.ID != d.ID {
				continue
			}
			if current.Pruned || current.Checksum != d.Checksum {
				return errors.New("revision changed")
			}
			if action == "approve" && str(actor, "role") != "admin" {
				return errors.New("administrator required")
			}
			rendered, err := a.renderRouter(s, current.RouterID)
			if err != nil || routing.Checksum(rendered) != current.Checksum {
				return errors.New("routing data, authentication or generation rules changed; generate a new revision")
			}
			if _, err = a.readDeploymentArtifactLocked(*current); err != nil {
				return errors.New("artifact integrity check failed")
			}
			if action == "validate" {
				if current.Status != "draft" {
					return errors.New("revision status changed")
				}
				current.Status = "validated"
				current.Validation = current.Platform + " native parse passed"
				if routing.ValidationLevel(current.Platform) == "structural" {
					current.Validation = "XML structure checked only; target NETCONF validate and confirmed-commit are mandatory before activation"
				}
			} else {
				if current.Status != "validated" {
					return errors.New("preflight validation required")
				}
				current.Status = "approved"
				current.ApprovedBy = str(user, "id")
			}
			audit(s, str(user, "id"), action, "deployments", current.ID)
			d = *current
			return nil
		}
		return errors.New("revision not found")
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, a.deploymentMetadata(d))
}
func (a *App) deploymentRetention(w http.ResponseWriter, r *http.Request, user Record, session Session, d Deployment, remove bool) {
	if str(user, "role") != "admin" || session.API {
		problem(w, 403, "browser administrator required for deployment retention")
		return
	}
	var body struct {
		Checksum    string `json:"checksum"`
		Acknowledge bool   `json:"acknowledge"`
		Reason      string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if !body.Acknowledge || body.Checksum != a.deploymentReviewToken(d) || len(body.Reason) > 1000 || strings.ContainsRune(body.Reason, 0) || (remove && body.Reason == "") {
		problem(w, 400, "current review identifier, acknowledgement and deletion reason are required")
		return
	}
	status := 409
	err := a.store.Update(func(s *State) error {
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			status = 401
			return errors.New("administrator permission changed")
		}
		for i := range s.Deployments {
			current := &s.Deployments[i]
			if current.ID != d.ID {
				continue
			}
			if current.Checksum != d.Checksum || !a.deploymentPrunable(s, *current) {
				return errors.New("cannot prune the current approved deployment")
			}
			details := a.deploymentMetadata(*current)
			details["reason"] = body.Reason
			if remove {
				if !current.Pruned {
					return errors.New("prune artifact before deleting metadata")
				}
				if _, err := a.removePrunedDeployment(*current); err != nil {
					return err
				}
				auditDetails(s, str(user, "id"), "delete-deployment-metadata", "deployments", current.ID, details)
				d = *current
				s.Deployments = append(s.Deployments[:i], s.Deployments[i+1:]...)
				return nil
			}
			if !current.Pruned {
				if _, err := a.readDeploymentArtifactLocked(*current); err != nil {
					return err
				}
				current.Pruned = true
				current.PrunedAt = time.Now().UTC()
				current.PrunedBy = str(user, "id")
				auditDetails(s, str(user, "id"), "prune-deployment", "deployments", current.ID, details)
			}
			d = *current
			return nil
		}
		return errors.New("revision not found")
	})
	if err != nil {
		if status == 401 {
			problem(w, status, "deployment authority changed; sign in again")
		} else {
			problem(w, status, "deployment retention refused or artifact integrity check failed")
		}
		return
	}
	if !remove {
		a.store.mu.Lock()
		_, err = a.removePrunedDeployment(d)
		a.store.mu.Unlock()
		if err != nil {
			problem(w, 500, "pruning recorded; removal will resume after storage recovery")
			return
		}
		respond(w, 200, a.deploymentMetadata(d))
		return
	}
	respond(w, 200, Record{"ok": true, "auditPreserved": true})
}
func routerPlatform(r Record) string {
	p := str(r, "platform")
	if p == "" {
		return "bird2"
	}
	return p
}
func extension(platform string) string {
	for _, c := range routing.Capabilities() {
		if c.Platform == platform {
			return c.Extension
		}
	}
	return "conf"
}
func (a *App) platforms(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, Record{"items": a.platformCapabilities()})
}
