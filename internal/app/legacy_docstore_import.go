package app

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func importLegacyDocstore(s *State, actor Record, p *migrate.DocstoreImport, versions map[string]DocumentVersion, receipt string) (LegacyDocstoreBatch, error) {
	var result LegacyDocstoreBatch
	if err := migrate.ValidateDocstore(p, true); err != nil {
		return result, err
	}
	if s.LegacyDocstore == nil {
		s.LegacyDocstore = &LegacyDocstoreState{Batches: []LegacyDocstoreBatch{}, Directories: []LegacyDocstoreDirectory{}, Files: []LegacyDocstoreFile{}, Logs: []LegacyDocstoreLog{}}
	}
	l := s.LegacyDocstore
	for _, b := range l.Batches {
		if b.ID == receipt {
			return result, errors.New("this document bundle was already imported")
		}
	}
	existing := map[string]bool{}
	for _, d := range l.Directories {
		existing[d.Source.ID] = true
	}
	for _, f := range l.Files {
		existing[f.Source.ID] = true
	}
	for _, log := range l.Logs {
		existing[log.Source.ID] = true
	}
	for _, d := range p.Directories {
		if existing[d.ID] {
			return result, errors.New("legacy source directory already exists")
		}
	}
	for _, f := range p.Files {
		if existing[f.ID] || documentByID(s, f.ID) != nil || directoryByID(s, "dir-"+f.ID) != nil {
			return result, errors.New("legacy source file already exists")
		}
	}
	for _, log := range p.Logs {
		if existing[log.ID] {
			return result, errors.New("legacy access record already exists")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result = LegacyDocstoreBatch{ID: receipt, ImportedAt: now, ImportedBy: str(actor, "id"), SourceTimezone: p.SourceTimezone, Files: len(p.Files), Directories: len(p.Directories), Logs: len(p.Logs)}
	for _, d := range p.Directories {
		l.Directories = append(l.Directories, LegacyDocstoreDirectory{Source: d, BatchID: receipt})
	}
	for _, source := range p.Files {
		v, ok := versions[source.ID]
		if !ok || v.Checksum != source.Checksum || v.Size != source.Size {
			return result, errors.New("validated document content is missing")
		}
		f := LegacyDocstoreFile{Source: source, BatchID: receipt, DocumentID: source.ID, DirectoryID: "dir-" + source.ID}
		if source.MinPrivileges < 0 || source.MinPrivileges > 3 {
			f.Review = "旧权限值无法识别，仅工作人员可见"
		}
		if source.Kind == "customer" {
			member := find(s, "members", source.MemberID)
			if member == nil || str(member, "createdAt") == "" {
				f.Review = "无法确认原成员实体，仅工作人员可见；稍后创建同编号成员不会自动授权"
			} else {
				f.MemberCreatedAt = str(member, "createdAt")
			}
		}
		if f.Review != "" {
			result.ReviewFiles++
		}
		name := source.Name
		if !validDocumentName(name) {
			name = "旧文档-" + source.ID
		}
		folder := Directory{ID: f.DirectoryID, Name: "旧文档 " + source.ID, Visibility: "staff", CreatedAt: now, UpdatedAt: now}
		d := Document{ID: f.DocumentID, Name: name, Description: "旧文档只读快照；按原文件权限和成员实体授权。", DirectoryID: folder.ID, Visibility: "staff", CreatedAt: now, UpdatedAt: now, CreatedBy: str(actor, "id"), Version: 1, BlobID: v.BlobID, Size: v.Size, MediaType: v.MediaType, Checksum: v.Checksum}
		s.Directories = append(s.Directories, folder)
		s.Documents = append(s.Documents, d)
		l.Files = append(l.Files, f)
	}
	for _, log := range p.Logs {
		l.Logs = append(l.Logs, LegacyDocstoreLog{Source: log, BatchID: receipt})
	}
	l.Batches = append(l.Batches, result)
	return result, validateLegacyDocstore(s)
}

func (a *App) importDocstoreBundle(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, migrate.DocstoreCompressedLimit)
	multipart, err := r.MultipartReader()
	if err != nil {
		problem(w, 400, "multipart bundle field required")
		return
	}
	part, err := multipart.NextPart()
	if err != nil || part.FormName() != "bundle" || part.FileName() == "" {
		problem(w, 400, "one document bundle file required")
		return
	}
	versions := map[string]DocumentVersion{}
	snapshot := a.store.Snapshot()
	remaining := a.documentQuota - blobUsage(&snapshot)
	var staged int64
	committed := false
	defer func() {
		if !committed {
			for _, v := range versions {
				a.discardBlob(v.BlobID)
			}
		}
	}()
	bundle, err := migrate.ReadDocstoreBundle(part, func(file migrate.DocstoreFileSource, reader io.Reader) error {
		if file.Size > remaining-staged {
			return errors.New("document quota cannot fit this bundle")
		}
		staged += file.Size
		if _, duplicate := versions[file.ID]; duplicate {
			return errors.New("duplicate document content")
		}
		v, e := a.writeBlob(reader)
		if e != nil {
			return e
		}
		versions[file.ID] = v
		if v.Size != file.Size || v.Checksum != file.Checksum {
			return errors.New("document content integrity mismatch")
		}
		return nil
	})
	part.Close()
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if next, e := multipart.NextPart(); e != io.EOF {
		if next != nil {
			next.Close()
		}
		problem(w, 400, "exactly one document bundle field is allowed")
		return
	}
	receipt := patchHash(bundle)
	var result LegacyDocstoreBatch
	status := 409
	err = a.store.Update(func(s *State) error {
		code, e := resourceWriter(s, user, session, "users")
		if e != nil {
			status = code
			return e
		}
		actor, e := a.atlasActor(s, user, session, "admin")
		if e != nil {
			status = 403
			return errors.New("import browser session or authority changed")
		}
		result, e = importLegacyDocstore(s, actor, &bundle.Docstore, versions, receipt)
		if e != nil {
			return e
		}
		if blobUsage(s) > a.documentQuota {
			return errors.New("document quota exceeded")
		}
		if e = validateDocuments(s); e != nil {
			return e
		}
		audit(s, str(actor, "id"), "legacy-document-import", "documents", receipt)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	committed = true
	respond(w, 200, Record{"docstore": result, "warnings": bundle.Warnings, "readOnly": true, "accessRecordsAreSuccessfulDownloads": false})
}
