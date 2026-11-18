package app

import (
	"fmt"
	"net/http"
	"unicode/utf8"
)

// prepareResourceImport changes only the caller's copy of the state. Both the
// read-only preview and the store transaction run the same import checks.
func (a *App) prepareResourceImport(s *State, actor Record, body resourceImport) (int, error) {
	n, err := a.appendImportedResources(s, actor, body)
	if err != nil {
		return 0, err
	}
	if body.Patching != nil {
		if _, err := importLegacyPatching(s, actor, body.Patching, nil, patchHash(body)); err != nil {
			return 0, err
		}
	}
	if err := validateImportedState(s); err != nil {
		return 0, err
	}
	return n, nil
}

func (a *App) importData(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	var body resourceImport
	if !decode(w, r, &body) {
		return
	}
	if body.Version != 1 || body.Resources == nil {
		problem(w, 400, "invalid import version or resources")
		return
	}
	if body.Patching != nil && len(body.Patching.Files) > 0 {
		problem(w, 400, "patch attachments require the administrator bundle import endpoint")
		return
	}
	if r.URL.Path == "/api/import/preview" {
		a.previewResourceImport(w, r, user, session, body)
		return
	}
	var n int
	status := http.StatusBadRequest
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin")
		if err != nil {
			status = http.StatusUnauthorized
			return err
		}
		n, err = a.prepareResourceImport(s, actor, body)
		if err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			status = http.StatusUnauthorized
			return err
		}
		audit(s, str(actor, "id"), "import", "resources", fmt.Sprint(n))
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"imported": n, "warnings": body.Warnings})
}

func (a *App) previewResourceImport(w http.ResponseWriter, r *http.Request, user Record, session Session, body resourceImport) {
	snapshot := a.store.Snapshot()
	actor, err := a.atlasActor(&snapshot, user, session, "admin")
	if err != nil {
		problem(w, 401, "import authority changed; sign in again")
		return
	}
	n, err := a.prepareResourceImport(&snapshot, actor, body)
	if r.Context().Err() != nil {
		return
	}
	if _, authErr := a.atlasActorCurrent(user, session, "admin"); authErr != nil {
		problem(w, 401, "import authority changed; sign in again")
		return
	}
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	counts := make(map[string]int, len(body.Resources))
	for kind, rows := range body.Resources {
		counts[kind] = len(rows)
	}
	limit := min(len(body.Warnings), 50)
	warnings := make([]string, 0, limit)
	for _, warning := range body.Warnings[:limit] {
		if len(warning) > 512 {
			end := 512
			for !utf8.RuneStart(warning[end]) {
				end--
			}
			warning = warning[:end] + "…"
		}
		warnings = append(warnings, warning)
	}
	respond(w, 200, Record{
		"checked": n, "resourceCounts": counts,
		"coreBundles": len(body.CoreBundles), "sflowReceivers": len(body.SFlowReceivers),
		"patching": body.Patching != nil, "warnings": warnings, "warningCount": len(body.Warnings),
	})
}
