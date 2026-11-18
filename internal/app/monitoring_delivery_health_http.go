package app

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (a *App) monitoringDeliveryHealth(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "admin" {
		problem(w, http.StatusForbidden, "browser administrator required")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid monitoring health parameters")
		return
	}
	for key, values := range query {
		if (key != "after" && key != "limit") || len(values) != 1 {
			problem(w, http.StatusBadRequest, "expected a single after cursor and limit")
			return
		}
	}
	after := query.Get("after")
	if query.Has("after") && !identifier.MatchString(after) {
		problem(w, http.StatusBadRequest, "invalid monitoring health cursor")
		return
	}
	limit := 20
	if query.Has("limit") {
		raw := query.Get("limit")
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 25 || raw != strconv.Itoa(limit) {
			problem(w, http.StatusBadRequest, "monitoring health limit must be between 1 and 25")
			return
		}
	}
	if _, err := a.atlasActorCurrent(user, session, "admin"); err != nil {
		problem(w, http.StatusForbidden, "administrator authority changed")
		return
	}
	snapshot := a.store.Snapshot()
	report := a.monitoringDeliveryReport(&snapshot, after, limit, time.Now().UTC())
	if _, err := a.atlasActorCurrent(user, session, "admin"); err != nil {
		problem(w, http.StatusForbidden, "administrator authority changed")
		return
	}
	respond(w, http.StatusOK, report)
}
