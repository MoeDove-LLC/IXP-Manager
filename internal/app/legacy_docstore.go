package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

// Imported files retain their original per-file access level. The ordinary
// document metadata is deliberately staff-only; only these validated bindings
// can grant access to the immutable archive.
type LegacyDocstoreBatch struct {
	ID             string `json:"id"`
	ImportedAt     string `json:"importedAt"`
	ImportedBy     string `json:"importedBy"`
	SourceTimezone string `json:"sourceTimezone"`
	Files          int    `json:"files"`
	Directories    int    `json:"directories"`
	Logs           int    `json:"logs"`
	ReviewFiles    int    `json:"reviewFiles"`
}
type LegacyDocstoreFile struct {
	Source          migrate.DocstoreFileSource `json:"source"`
	BatchID         string                     `json:"batchId"`
	DocumentID      string                     `json:"documentId"`
	DirectoryID     string                     `json:"directoryId"`
	MemberCreatedAt string                     `json:"memberCreatedAt,omitempty"`
	Review          string                     `json:"review,omitempty"`
}
type LegacyDocstoreDirectory struct {
	Source  migrate.DocstoreDirectorySource `json:"source"`
	BatchID string                          `json:"batchId"`
}
type LegacyDocstoreLog struct {
	Source  migrate.DocstoreLogSource `json:"source"`
	BatchID string                    `json:"batchId"`
}
type LegacyDocstoreState struct {
	Batches     []LegacyDocstoreBatch     `json:"batches"`
	Directories []LegacyDocstoreDirectory `json:"directories"`
	Files       []LegacyDocstoreFile      `json:"files"`
	Logs        []LegacyDocstoreLog       `json:"logs"`
}

func legacyDocstoreFile(s *State, id string) *LegacyDocstoreFile {
	if s.LegacyDocstore != nil {
		for i := range s.LegacyDocstore.Files {
			if s.LegacyDocstore.Files[i].DocumentID == id {
				return &s.LegacyDocstore.Files[i]
			}
		}
	}
	return nil
}
func legacyDocstoreDirectory(s *State, id string) *LegacyDocstoreFile {
	if s.LegacyDocstore != nil {
		for i := range s.LegacyDocstore.Files {
			if s.LegacyDocstore.Files[i].DirectoryID == id {
				return &s.LegacyDocstore.Files[i]
			}
		}
	}
	return nil
}
func legacyDocstoreMemberCurrent(s *State, f *LegacyDocstoreFile) bool {
	member := find(s, "members", f.Source.MemberID)
	return f.MemberCreatedAt != "" && member != nil && str(member, "createdAt") == f.MemberCreatedAt
}
func legacyDocstoreVisible(s *State, f *LegacyDocstoreFile, user Record) bool {
	role := str(user, "role")
	if role == "admin" || role == "operator" || role == "viewer" {
		return true
	}
	if f == nil || f.Review != "" || f.Source.MinPrivileges < 0 || f.Source.MinPrivileges > 2 {
		return false
	}
	if f.Source.Kind == "customer" {
		if role != "member" || str(user, "memberId") != f.Source.MemberID || !legacyDocstoreMemberCurrent(s, f) {
			return false
		}
	} else if f.Source.Kind != "shared" {
		return false
	}
	if f.Source.MinPrivileges == 0 {
		return true
	}
	if role != "member" {
		return false
	}
	return f.Source.MinPrivileges == 1 || str(user, "memberRole") == "member-admin"
}
func legacyDocstoreView(s *State, d Document, out Record) {
	if f := legacyDocstoreFile(s, d.ID); f != nil {
		out["legacyDocstore"], out["readOnly"] = true, true
		out["originalName"], out["sourceUploadedAt"] = f.Source.Name, f.Source.Facts["created_at"]
		out["legacyKind"], out["minimumPrivilege"] = f.Source.Kind, f.Source.MinPrivileges
		out["visibility"] = legacyDocstoreScope(s, f)
		if f.Source.Kind == "customer" && legacyDocstoreMemberCurrent(s, f) {
			out["memberId"] = f.Source.MemberID
		}
	}
}
func legacyDocstoreScope(s *State, f *LegacyDocstoreFile) string {
	if f.Review != "" || f.Source.MinPrivileges < 0 || f.Source.MinPrivileges > 2 || f.Source.Kind == "customer" && !legacyDocstoreMemberCurrent(s, f) {
		return "staff"
	}
	if f.Source.MinPrivileges == 2 {
		if f.Source.Kind == "customer" {
			return "member-admin"
		}
		return "authenticated-admin"
	}
	if f.Source.Kind == "customer" {
		return "member"
	}
	if f.Source.MinPrivileges == 0 {
		return "public"
	}
	return "authenticated"
}

func validateLegacyDocstore(s *State) error {
	l := s.LegacyDocstore
	if l == nil {
		return nil
	}
	if len(l.Batches) > 4096 || len(l.Directories)+len(l.Files)+len(l.Logs) > migrate.DocstoreRecordLimit {
		return errors.New("legacy document archive exceeds limit")
	}
	batches := map[string]LegacyDocstoreBatch{}
	imports := map[string]*migrate.DocstoreImport{}
	for _, b := range l.Batches {
		if !digestPattern.MatchString(b.ID) || imports[b.ID] != nil || b.ImportedBy == "" {
			return errors.New("invalid legacy document import receipt")
		}
		if _, e := time.Parse(time.RFC3339Nano, b.ImportedAt); e != nil {
			return errors.New("invalid legacy document import time")
		}
		batches[b.ID] = b
		imports[b.ID] = &migrate.DocstoreImport{Version: 1, SourceTimezone: b.SourceTimezone}
	}
	ids, protected := map[string]bool{}, map[string]string{}
	for _, d := range l.Directories {
		p := imports[d.BatchID]
		if p == nil || ids[d.Source.ID] {
			return errors.New("invalid legacy source directory identity")
		}
		ids[d.Source.ID] = true
		p.Directories = append(p.Directories, d.Source)
	}
	for _, f := range l.Files {
		p := imports[f.BatchID]
		if p == nil || ids[f.Source.ID] || f.DocumentID != f.Source.ID || f.DirectoryID != "dir-"+f.Source.ID {
			return errors.New("invalid legacy document identity")
		}
		ids[f.Source.ID] = true
		p.Files = append(p.Files, f.Source)
		d, folder := documentByID(s, f.DocumentID), directoryByID(s, f.DirectoryID)
		if d == nil || folder == nil || d.DirectoryID != folder.ID || d.Visibility != "staff" || folder.Visibility != "staff" || d.MemberID != "" || folder.MemberID != "" || folder.ParentID != "" || d.Version != 1 || len(d.Revisions) != 0 || d.Checksum != f.Source.Checksum || d.Size != f.Source.Size || legacyPatchAttachment(s, d.ID) != nil || legacyPatchDirectory(s, folder.ID) != nil {
			return errors.New("legacy document snapshot or scope changed")
		}
		if f.Source.Kind == "shared" && f.MemberCreatedAt != "" || f.Source.Kind == "customer" && f.Review == "" && (f.Source.MemberID == "" || f.MemberCreatedAt == "") {
			return errors.New("invalid legacy member entity binding")
		}
		protected[folder.ID] = d.ID
	}
	for _, log := range l.Logs {
		p := imports[log.BatchID]
		if p == nil || ids[log.Source.ID] {
			return errors.New("invalid legacy access record batch")
		}
		ids[log.Source.ID] = true
		p.Logs = append(p.Logs, log.Source)
	}
	for id, p := range imports {
		b := batches[id]
		if b.Files != len(p.Files) || b.Directories != len(p.Directories) || b.Logs != len(p.Logs) {
			return errors.New("legacy archive receipt count changed")
		}
		if e := migrate.ValidateDocstore(p, true); e != nil {
			return e
		}
	}
	for _, d := range s.Documents {
		if id := protected[d.DirectoryID]; id != "" && id != d.ID {
			return errors.New("legacy document directories cannot contain additional files")
		}
	}
	for _, d := range s.Directories {
		if protected[d.ParentID] != "" {
			return errors.New("legacy document directories cannot contain children")
		}
	}
	return nil
}

func (a *App) legacyDocstoreArchive(w http.ResponseWriter, r *http.Request, user Record) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	role := str(user, "role")
	if role != "admin" && role != "operator" && role != "viewer" {
		problem(w, 403, "staff access required")
		return
	}
	s := a.store.Snapshot()
	l := s.LegacyDocstore
	if l == nil {
		l = &LegacyDocstoreState{Batches: []LegacyDocstoreBatch{}, Directories: []LegacyDocstoreDirectory{}, Files: []LegacyDocstoreFile{}, Logs: []LegacyDocstoreLog{}}
	}
	respond(w, 200, Record{"archive": l, "readOnly": true, "accessRecordsAreSuccessfulDownloads": false})
}
