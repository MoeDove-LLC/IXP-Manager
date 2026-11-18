package app

import (
	"errors"
	"net/http"
	"os/exec"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

type deploymentReadinessAction struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons"`
}
type deploymentReadiness struct {
	ID                     string                               `json:"id"`
	RouterID               string                               `json:"routerId"`
	Platform               string                               `json:"platform"`
	CurrentPlatform        string                               `json:"currentPlatform"`
	Status                 string                               `json:"status"`
	ReviewIdentifier       string                               `json:"reviewIdentifier"`
	CheckedAt              string                               `json:"checkedAt"`
	Inputs                 string                               `json:"inputs"`
	Artifact               string                               `json:"artifact"`
	ValidationLevel        string                               `json:"validationLevel"`
	NativeCheckerAvailable bool                                 `json:"nativeCheckerAvailable"`
	AddressFamily          int                                  `json:"addressFamily,omitempty"`
	ActivationMode         string                               `json:"activationMode"`
	TargetChecked          bool                                 `json:"targetChecked"`
	Actions                map[string]deploymentReadinessAction `json:"actions"`
}

func deploymentByID(s *State, id string) *Deployment {
	for i := range s.Deployments {
		if s.Deployments[i].ID == id {
			return &s.Deployments[i]
		}
	}
	return nil
}

func deploymentDownloadScope(s *State, actor Record, session Session, d Deployment) bool {
	if str(actor, "role") != "admin" && str(actor, "role") != "operator" {
		return false
	}
	if !session.API {
		return true
	}
	if !contains(session.Scopes, "deployments:pull") || !contains(session.RouterIDs, d.RouterID) {
		return false
	}
	for _, current := range s.Security[str(actor, "id")].Tokens {
		if current.ID == session.TokenID && contains(current.Scopes, "deployments:pull") && contains(current.RouterIDs, d.RouterID) {
			return true
		}
	}
	return false
}

func deploymentAction(reasons ...string) deploymentReadinessAction {
	if reasons == nil {
		reasons = []string{}
	}
	return deploymentReadinessAction{Allowed: len(reasons) == 0, Reasons: reasons}
}

// Called with the store lock held. No native validator or device is contacted,
// no state is changed, and no plaintext/configuration digest is returned.
func (a *App) deploymentReadinessLocked(s *State, user Record, session Session, d Deployment) deploymentReadiness {
	result := deploymentReadiness{ID: d.ID, RouterID: d.RouterID, Platform: d.Platform, Status: d.Status, ReviewIdentifier: a.deploymentReviewToken(d), CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Inputs: "unavailable", Artifact: "unavailable", ValidationLevel: routing.ValidationLevel(d.Platform), ActivationMode: "unavailable", Actions: map[string]deploymentReadinessAction{}}
	var capability routing.Capability
	for _, candidate := range routing.Capabilities() {
		if candidate.Platform == d.Platform {
			capability = candidate
			break
		}
	}
	switch d.Platform {
	case "bird1":
		result.ActivationMode = "bird1-confirmed-configure"
	case "bird2", "bird3":
		result.ActivationMode = "bird-confirmed-configure"
	case "frr":
		result.ActivationMode = "frr-local-watchdog"
	case "openbgpd":
		result.ActivationMode = "openbgpd-local-watchdog"
	case "quagga":
		result.ActivationMode = "quagga-local-watchdog"
	case "junos":
		result.ActivationMode = "netconf-confirmed-commit"
	}
	if result.ValidationLevel == "native" && d.Platform != "bird1" {
		_, err := exec.LookPath(a.routerValidatorBinary(d.Platform))
		result.NativeCheckerAvailable = err == nil
	}
	base := []string{}
	currentRouter := find(s, "routers", d.RouterID)
	canRender := false
	if currentRouter == nil {
		base = append(base, "router-missing")
	} else {
		result.CurrentPlatform = routerPlatform(currentRouter)
		current, err := a.renderRouter(s, d.RouterID)
		if err != nil {
			base = append(base, "inputs-invalid")
		} else {
			canRender = true
			if result.CurrentPlatform != d.Platform {
				result.Inputs = "changed"
				base = append(base, "platform-changed")
			} else if routing.Checksum(current) != d.Checksum {
				result.Inputs = "changed"
				base = append(base, "inputs-changed")
			} else {
				result.Inputs = "current"
			}
		}
	}
	if d.Pruned {
		result.Artifact = "pruned"
		base = append(base, "artifact-pruned")
	} else if config, err := a.readDeploymentArtifactLocked(d); err != nil {
		base = append(base, "artifact-unavailable")
	} else {
		result.Artifact = "verified"
		if d.Platform == "bird1" {
			family, e := routing.BIRD1Family(config)
			if e != nil {
				result.Artifact = "unavailable"
				base = append(base, "artifact-unavailable")
			} else {
				result.AddressFamily = family
				binary, e := a.bird1ValidatorBinary(family)
				if e == nil {
					_, e = exec.LookPath(binary)
				}
				result.NativeCheckerAvailable = e == nil
			}
		}
	}
	if d.Platform == "routeros7" {
		handoffReasons := append([]string{}, base...)
		if str(user, "role") != "admin" && str(user, "role") != "operator" || session.API {
			handoffReasons = append(handoffReasons, "staff-browser-required")
		}
		if d.Status != "draft" {
			handoffReasons = append(handoffReasons, "draft-required")
		}
		result.Actions["routerosHandoff"] = deploymentAction(handoffReasons...)
	}
	if !capability.Validate {
		base = append(base, "preview-only")
	}
	write := str(user, "role") == "admin" || str(user, "role") == "operator"
	write = write && (!session.API || contains(session.Scopes, "deployments:write"))
	validateReasons := append([]string{}, base...)
	if !write {
		validateReasons = append(validateReasons, "staff-write-required")
	}
	if d.Status != "draft" {
		validateReasons = append(validateReasons, "validation-already-recorded")
	}
	if capability.Validate && result.ValidationLevel == "native" && !result.NativeCheckerAvailable {
		validateReasons = append(validateReasons, "checker-missing")
	}
	approveReasons := append([]string{}, base...)
	if str(user, "role") != "admin" || session.API {
		approveReasons = append(approveReasons, "browser-admin-required")
	}
	if d.Status != "validated" {
		if d.Status == "approved" {
			approveReasons = append(approveReasons, "approval-already-recorded")
		} else {
			approveReasons = append(approveReasons, "validation-required")
		}
	}
	downloadReasons := append([]string{}, base...)
	if !deploymentDownloadScope(s, user, session, d) {
		downloadReasons = append(downloadReasons, "download-permission-required")
	}
	if d.Status != "approved" {
		downloadReasons = append(downloadReasons, "approval-required")
	}
	generateReasons := []string{}
	if !write {
		generateReasons = append(generateReasons, "staff-write-required")
	}
	if !canRender {
		generateReasons = append(generateReasons, "inputs-invalid")
	}
	result.Actions["validate"], result.Actions["approve"] = deploymentAction(validateReasons...), deploymentAction(approveReasons...)
	result.Actions["download"], result.Actions["generate"] = deploymentAction(downloadReasons...), deploymentAction(generateReasons...)
	return result
}

func (a *App) deploymentReadinessView(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	a.store.mu.RLock()
	actor, err := a.atlasActor(&a.store.state, user, session, "admin", "operator", "viewer")
	if err != nil || a.store.closed {
		a.store.mu.RUnlock()
		problem(w, 401, "deployment authority changed; sign in again")
		return
	}
	d := deploymentByID(&a.store.state, id)
	if d == nil {
		a.store.mu.RUnlock()
		problem(w, 404, "deployment not found")
		return
	}
	result := a.deploymentReadinessLocked(&a.store.state, actor, session, *d)
	// Logout can revoke a browser session without needing the store lock.
	_, err = a.atlasActor(&a.store.state, user, session, "admin", "operator", "viewer")
	a.store.mu.RUnlock()
	if err != nil || r.Context().Err() != nil {
		problem(w, 401, "deployment authority changed; sign in again")
		return
	}
	respond(w, 200, result)
}

// The actual download retains its own final decision. Readiness never grants
// access to these bytes, and the mutable current actor/target are checked again.
func (a *App) currentDeploymentDownload(r *http.Request, user Record, session Session, opened Deployment) (Deployment, string, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	actor, err := a.atlasActor(s, user, session, "admin", "operator")
	if err != nil || a.store.closed || r.Context().Err() != nil {
		return Deployment{}, "", 401, errors.New("deployment authority changed; sign in again")
	}
	d := deploymentByID(s, opened.ID)
	if d == nil || d.Checksum != opened.Checksum || d.Status != "approved" || d.Pruned {
		return Deployment{}, "", 409, errors.New("deployment revision changed; refresh before downloading")
	}
	if !deploymentDownloadScope(s, actor, session, *d) {
		return Deployment{}, "", 403, errors.New("current router-bound download authority is required")
	}
	rendered, err := a.renderRouter(s, d.RouterID)
	if err != nil || routing.Checksum(rendered) != d.Checksum {
		return Deployment{}, "", 409, errors.New("routing data, authentication or generation rules changed; generate and review a new revision")
	}
	config, err := a.readDeploymentArtifactLocked(*d)
	if err != nil {
		return Deployment{}, "", 500, errors.New("deployment artifact integrity check failed")
	}
	if _, err = a.atlasActor(s, user, session, "admin", "operator"); err != nil || r.Context().Err() != nil {
		return Deployment{}, "", 401, errors.New("deployment authority changed; sign in again")
	}
	return *d, config, 200, nil
}
