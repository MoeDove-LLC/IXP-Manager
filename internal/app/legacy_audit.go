package app

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const maxLegacyAuditRows = 50000
const maxLegacyAuditBatches = 50
const legacyAuditPageSize = 50

type legacyAuditBatch struct {
	Archive    migrate.LegacyAuditArchive `json:"archive"`
	ImportedAt string                     `json:"importedAt"`
	ImportedBy string                     `json:"importedBy"`
}

type legacyAuditBatchView struct {
	Checksum       string `json:"checksum"`
	Source         string `json:"source"`
	SourceTimezone string `json:"sourceTimezone"`
	Count          int    `json:"count"`
	ImportedAt     string `json:"importedAt"`
}

func validateLegacyAuditBatches(batches []legacyAuditBatch) error {
	if len(batches) > maxLegacyAuditBatches {
		return errors.New("legacy audit history exceeds the 50-batch store limit")
	}
	checksums := make(map[string]bool, len(batches))
	ids := make(map[string]bool)
	for _, batch := range batches {
		archive := batch.Archive
		if err := migrate.ValidateLegacyAuditArchive(archive); err != nil {
			return err
		}
		if checksums[archive.Checksum] {
			return errors.New("legacy audit archive has already been imported")
		}
		if _, err := time.Parse(time.RFC3339Nano, batch.ImportedAt); err != nil || !identifier.MatchString(batch.ImportedBy) {
			return errors.New("invalid legacy audit import receipt")
		}
		checksums[archive.Checksum] = true
		for _, entry := range archive.Entries {
			key := archive.Source + ":" + entry.ID
			if ids[key] {
				return errors.New("legacy audit source row has already been imported")
			}
			ids[key] = true
			if len(ids) > maxLegacyAuditRows {
				return errors.New("legacy audit history exceeds the 50000-row store limit")
			}
		}
	}
	return nil
}

func (batch legacyAuditBatch) view() legacyAuditBatchView {
	return legacyAuditBatchView{
		Checksum: batch.Archive.Checksum, Source: batch.Archive.Source, SourceTimezone: batch.Archive.SourceTimezone,
		Count: len(batch.Archive.Entries), ImportedAt: batch.ImportedAt,
	}
}

func (a *App) legacyAuditRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	const path = "/api/audit/legacy"
	const importPath = "/api/import/audit-archive"
	if r.URL.Path != path && r.URL.Path != importPath && !strings.HasPrefix(r.URL.Path, path+"/") {
		return false
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "legacy audit requires an administrator browser session")
		return true
	}
	if r.URL.Path == importPath {
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return true
		}
		a.importLegacyAudit(w, r, user, session)
		return true
	}
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return true
	}
	if r.URL.Path == path {
		a.store.mu.RLock()
		_, err := a.atlasActor(&a.store.state, user, session, "admin")
		if err != nil || a.store.closed {
			a.store.mu.RUnlock()
			problem(w, 401, "legacy audit authority changed")
			return true
		}
		views := make([]legacyAuditBatchView, 0, len(a.store.state.LegacyAudit))
		for i := len(a.store.state.LegacyAudit) - 1; i >= 0; i-- {
			views = append(views, a.store.state.LegacyAudit[i].view())
		}
		a.store.mu.RUnlock()
		respond(w, 200, Record{"batches": views})
		return true
	}
	a.readLegacyAuditPage(w, r, user, session, strings.TrimPrefix(r.URL.Path, path+"/"))
	return true
}

func (a *App) readLegacyAuditPage(w http.ResponseWriter, r *http.Request, user Record, session Session, checksum string) {
	if !digestPattern.MatchString(checksum) {
		problem(w, 404, "legacy audit archive not found")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > migrate.MaxLegacyAuditArchiveRows {
			problem(w, 400, "legacy audit page offset is outside the supported range")
			return
		}
		offset = n
	}
	a.store.mu.RLock()
	_, err := a.atlasActor(&a.store.state, user, session, "admin")
	if err != nil || a.store.closed {
		a.store.mu.RUnlock()
		problem(w, 401, "legacy audit authority changed")
		return
	}
	for _, batch := range a.store.state.LegacyAudit {
		if batch.Archive.Checksum != checksum {
			continue
		}
		view := batch.view()
		start := min(offset, view.Count)
		end := min(start+legacyAuditPageSize, view.Count)
		entries := append([]migrate.LegacyAuditEntry{}, batch.Archive.Entries[start:end]...)
		var next *int
		if end < view.Count {
			next = &end
		}
		a.store.mu.RUnlock()
		respond(w, 200, Record{"batch": view, "entries": entries, "nextOffset": next})
		return
	}
	a.store.mu.RUnlock()
	problem(w, 404, "legacy audit archive not found")
}

func (a *App) importLegacyAudit(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	var archive migrate.LegacyAuditArchive
	if !decode(w, r, &archive) {
		return
	}
	if err := migrate.ValidateLegacyAuditArchive(archive); err != nil {
		problem(w, 400, err.Error())
		return
	}
	batch := legacyAuditBatch{Archive: archive, ImportedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	status := http.StatusConflict
	err := a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin")
		if err != nil {
			status = http.StatusUnauthorized
			return err
		}
		batch.ImportedBy = str(actor, "id")
		s.LegacyAudit = append(s.LegacyAudit, batch)
		if err := validateLegacyAuditBatches(s.LegacyAudit); err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		if _, err := a.atlasActor(s, user, session, "admin"); err != nil {
			status = http.StatusUnauthorized
			return err
		}
		audit(s, batch.ImportedBy, "import-legacy-audit", "legacy-audit", archive.Checksum)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 201, Record{"batch": batch.view()})
}
