package app

import (
	"crypto/hmac"
	"errors"
	"net/http"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routing"
)

const routerOSReviewHeader = "X-IXP-Review-Identifier"

func (a *App) routerOSHandoff(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	if session.API || (str(user, "role") != "admin" && str(user, "role") != "operator") {
		problem(w, 403, "RouterOS handoff requires an administrator or operator browser session")
		return
	}
	body, review, status, err := a.currentRouterOSHandoff(r, user, session, id)
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="routeros-handoff-`+id+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(routerOSReviewHeader, review)
	_, _ = w.Write(body)
}

func (a *App) currentRouterOSHandoff(r *http.Request, user Record, session Session, id string) ([]byte, string, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil || a.store.closed || r.Context().Err() != nil {
		return nil, "", 401, errors.New("deployment authority changed; sign in again")
	}
	d := deploymentByID(s, id)
	if d == nil || d.Platform != "routeros7" {
		return nil, "", 404, errors.New("RouterOS draft not found")
	}
	if d.Pruned {
		return nil, "", 410, errors.New("RouterOS draft artifact has been pruned")
	}
	review := a.deploymentReviewToken(*d)
	if d.Status != "draft" || !hmac.Equal([]byte(review), []byte(r.Header.Get(routerOSReviewHeader))) {
		return nil, "", 409, errors.New("refresh and review the exact RouterOS draft")
	}
	router, peers, err := a.routerRenderInputs(s, d.RouterID)
	if err != nil || router.Platform != d.Platform {
		return nil, "", 409, errors.New("current RouterOS inputs or platform changed; generate a new draft")
	}
	config, err := routing.RenderFor(router.Platform, router, peers)
	if err != nil || routing.Checksum(config) != d.Checksum {
		return nil, "", 409, errors.New("current RouterOS routing data or authentication changed; generate a new draft")
	}
	if _, err = a.readDeploymentArtifactLocked(*d); err != nil {
		return nil, "", 500, errors.New("RouterOS draft artifact integrity check failed")
	}
	handoff, err := routeros.NewPlanningHandoff(router, peers, d.ID, review)
	if err != nil {
		return nil, "", 422, errors.New("RouterOS draft inputs cannot be represented by the local planner")
	}
	body, err := handoff.Encode()
	if err != nil {
		return nil, "", 422, errors.New("RouterOS planning handoff exceeds the file limit")
	}
	if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil || r.Context().Err() != nil {
		return nil, "", 401, errors.New("deployment authority changed; sign in again")
	}
	return body, review, 200, nil
}
