package app

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

const storageCheckPath = "/api/system/storage-check"

func (a *App) runStorageCheck(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || str(user, "role") != "admin" {
		problem(w, http.StatusForbidden, "browser administrator required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !storageCheckWithoutOptions(w, r) {
		return
	}
	if _, err := a.atlasActorCurrent(user, session, "admin"); err != nil {
		problem(w, http.StatusForbidden, "administrator authority changed")
		return
	}
	if !a.storageCheckMu.TryLock() {
		problem(w, http.StatusConflict, "storage check is already in progress")
		return
	}
	defer a.storageCheckMu.Unlock()

	result := a.store.checkStorage(r.Context())
	if _, err := a.atlasActorCurrent(user, session, "admin"); err != nil {
		problem(w, http.StatusForbidden, "administrator authority changed")
		return
	}
	respond(w, http.StatusOK, result)
}

func storageCheckWithoutOptions(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		problem(w, http.StatusBadRequest, "storage check does not take options")
		return false
	}
	if r.ContentLength == 0 {
		return true
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		problem(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128))
	var options map[string]json.RawMessage
	if decoder.Decode(&options) != nil || options == nil || len(options) != 0 || decoder.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "storage check accepts only an empty JSON object")
		return false
	}
	return true
}
