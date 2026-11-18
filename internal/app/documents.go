package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxDocumentSize int64 = 25 << 20
const maxTextSize int64 = 1 << 20
const maxDocumentVersions = 20

var errDocumentTooLarge = errors.New("file exceeds 25 MiB limit")

var blobName = regexp.MustCompile(`^blob-[A-Za-z0-9_-]{24}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Directory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parentId"`
	MemberID    string `json:"memberId"`
	Visibility  string `json:"visibility"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}
type DocumentVersion struct {
	Version   int    `json:"version"`
	BlobID    string `json:"blobId"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	Checksum  string `json:"checksum"`
	CreatedAt string `json:"createdAt"`
}
type Document struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	DirectoryID string            `json:"directoryId"`
	MemberID    string            `json:"memberId"`
	Visibility  string            `json:"visibility"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
	CreatedBy   string            `json:"createdBy"`
	Version     int               `json:"version"`
	BlobID      string            `json:"blobId"`
	Size        int64             `json:"size"`
	MediaType   string            `json:"mediaType"`
	Checksum    string            `json:"checksum"`
	Revisions   []DocumentVersion `json:"revisions,omitempty"`
}
type documentInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	DirectoryID string `json:"directoryId"`
	MemberID    string `json:"memberId"`
	Visibility  string `json:"visibility"`
}
type directoryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ParentID    string `json:"parentId"`
	MemberID    string `json:"memberId"`
	Visibility  string `json:"visibility"`
}

func documentView(d Document) Record {
	return Record{"id": d.ID, "name": d.Name, "description": d.Description, "directoryId": d.DirectoryID, "memberId": d.MemberID, "visibility": d.Visibility, "createdAt": d.CreatedAt, "updatedAt": d.UpdatedAt, "createdBy": d.CreatedBy, "size": d.Size, "mediaType": d.MediaType, "checksum": d.Checksum, "version": d.Version}
}
func directoryByID(s *State, id string) *Directory {
	for i := range s.Directories {
		if s.Directories[i].ID == id {
			return &s.Directories[i]
		}
	}
	return nil
}
func documentByID(s *State, id string) *Document {
	for i := range s.Documents {
		if s.Documents[i].ID == id {
			return &s.Documents[i]
		}
	}
	return nil
}
func validDocumentName(name string) bool {
	if name == "." || name == ".." || strings.TrimSpace(name) == "" || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func documentScope(s *State, visibility, member string) error {
	switch visibility {
	case "staff":
	case "member":
		if member == "" {
			return fmt.Errorf("member-visible documents require a member")
		}
	case "authenticated", "public":
		if member != "" {
			return fmt.Errorf("shared documents cannot be bound to one member")
		}
	default:
		return fmt.Errorf("invalid document visibility")
	}
	if member != "" && find(s, "members", member) == nil {
		return fmt.Errorf("document references missing member")
	}
	return nil
}
func documentVisible(visibility, member string, user Record) bool {
	if visibility == "public" {
		return true
	}
	role := str(user, "role")
	if role == "" {
		return false
	}
	if role != "member" {
		return true
	}
	return visibility == "authenticated" || (visibility == "member" && member == str(user, "memberId"))
}
func validateDocuments(s *State) error {
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, d := range s.Directories {
		if !identifier.MatchString(d.ID) || ids[d.ID] || !validDocumentName(d.Name) || len(d.Description) > 10000 {
			return fmt.Errorf("invalid or duplicate directory metadata")
		}
		ids[d.ID] = true
		if err := documentScope(s, d.Visibility, d.MemberID); err != nil {
			return err
		}
		key := d.ParentID + "\x00" + d.Visibility + "\x00" + d.MemberID + "\x00" + strings.ToLower(d.Name)
		if names[key] {
			return fmt.Errorf("directory name already exists in this folder")
		}
		names[key] = true
		seen := map[string]bool{d.ID: true}
		parent := d.ParentID
		depth := 0
		for parent != "" {
			p := directoryByID(s, parent)
			if p == nil {
				return fmt.Errorf("parent directory not found")
			}
			if seen[parent] || depth >= 32 {
				return fmt.Errorf("directory cycle or depth limit exceeded")
			}
			if p.MemberID != d.MemberID || p.Visibility != d.Visibility {
				return fmt.Errorf("directory permissions must match its parent")
			}
			seen[parent] = true
			parent = p.ParentID
			depth++
		}
	}
	ids = map[string]bool{}
	names = map[string]bool{}
	blobs := map[string]bool{}
	for _, d := range s.Documents {
		if !identifier.MatchString(d.ID) || ids[d.ID] || !validDocumentName(d.Name) || len(d.Description) > 10000 || d.Version < 1 {
			return fmt.Errorf("invalid or duplicate document metadata")
		}
		ids[d.ID] = true
		if err := documentScope(s, d.Visibility, d.MemberID); err != nil {
			return err
		}
		if d.DirectoryID != "" {
			p := directoryByID(s, d.DirectoryID)
			if p == nil {
				return fmt.Errorf("document directory not found")
			}
			if p.MemberID != d.MemberID || p.Visibility != d.Visibility {
				return fmt.Errorf("document permissions must match its directory")
			}
		}
		key := d.DirectoryID + "\x00" + d.Visibility + "\x00" + d.MemberID + "\x00" + strings.ToLower(d.Name)
		if names[key] {
			return fmt.Errorf("document name already exists in this folder")
		}
		names[key] = true
		versions := append([]DocumentVersion{{Version: d.Version, BlobID: d.BlobID, Size: d.Size, MediaType: d.MediaType, Checksum: d.Checksum}}, d.Revisions...)
		seen := map[int]bool{}
		if len(d.Revisions) > maxDocumentVersions {
			return fmt.Errorf("document history exceeds retention limit")
		}
		for _, v := range versions {
			if !blobName.MatchString(v.BlobID) || !digestPattern.MatchString(v.Checksum) || v.Size < 0 || v.Size > maxDocumentSize || seen[v.Version] || v.Version < 1 || v.Version > d.Version || blobs[v.BlobID] {
				return fmt.Errorf("invalid document version")
			}
			seen[v.Version] = true
			blobs[v.BlobID] = true
		}
	}
	if err := validateLegacyPatching(s); err != nil {
		return err
	}
	return validateLegacyDocstore(s)
}
func documentUsage(s *State) int64 {
	var size int64
	for _, d := range s.Documents {
		size += d.Size
		for _, v := range d.Revisions {
			size += v.Size
		}
	}
	return size
}
func (a *App) blobDir() string { return filepath.Join(filepath.Dir(a.store.path), "blobs") }
func (a *App) initDocuments() error {
	dir := a.blobDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("document blob directory must be a real directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	s := a.store.Snapshot()
	refs := map[string]int64{}
	for _, d := range s.Documents {
		refs[d.BlobID] = d.Size
		for _, v := range d.Revisions {
			refs[v.BlobID] = v.Size
		}
	}
	for _, letter := range s.LOAs {
		refs[letter.PDFBlobID] = letter.PDFSize
		refs[letter.HTMLBlobID] = letter.HTMLSize
	}
	for _, logo := range s.MemberLogos {
		if logo.BlobID != "" {
			refs[logo.BlobID] = logo.Size
		}
	}
	for id, size := range refs {
		f, err := a.openBlob(id)
		if err != nil {
			return fmt.Errorf("document content missing or unsafe: %s", id)
		}
		st, err := f.Stat()
		f.Close()
		if err != nil || st.Size() != size {
			return fmt.Errorf("document content size mismatch: %s", id)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := refs[entry.Name()]; ok {
			continue
		}
		if blobName.MatchString(entry.Name()) || strings.HasPrefix(entry.Name(), ".upload-") {
			if entry.Type().IsRegular() {
				if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (a *App) openBlob(id string) (*os.File, error) {
	if !blobName.MatchString(id) {
		return nil, fmt.Errorf("invalid blob identifier")
	}
	root, err := os.OpenRoot(a.blobDir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(id, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("blob is not a regular file")
	}
	return f, nil
}
func (a *App) discardBlob(id string) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return
	}
	if blobName.MatchString(id) {
		root, err := os.OpenRoot(a.blobDir())
		if err == nil {
			defer root.Close()
			_ = root.Remove(id)
		}
	}
}
func (a *App) writeBlob(reader io.Reader) (DocumentVersion, error) {
	f, err := os.CreateTemp(a.blobDir(), ".upload-")
	if err != nil {
		return DocumentVersion{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(reader, maxDocumentSize+1))
	if err != nil {
		return DocumentVersion{}, err
	}
	if size > maxDocumentSize {
		return DocumentVersion{}, errDocumentTooLarge
	}
	if _, err = f.Seek(0, 0); err != nil {
		return DocumentVersion{}, err
	}
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return DocumentVersion{}, err
	}
	mediaType := http.DetectContentType(buf[:n])
	if err = f.Sync(); err != nil {
		return DocumentVersion{}, err
	}
	if err = f.Close(); err != nil {
		return DocumentVersion{}, err
	}
	id := "blob-" + token(18)
	// Hard-link publishes a fully fsynced immutable inode without overwriting an
	// existing blob. All logical names and paths remain in validated metadata.
	if err = os.Link(f.Name(), filepath.Join(a.blobDir(), id)); err != nil {
		return DocumentVersion{}, err
	}
	dir, err := os.Open(a.blobDir())
	if err != nil {
		a.discardBlob(id)
		return DocumentVersion{}, err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		a.discardBlob(id)
		return DocumentVersion{}, err
	}
	return DocumentVersion{BlobID: id, Size: size, MediaType: mediaType, Checksum: hex.EncodeToString(h.Sum(nil)), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}
func (a *App) readUpload(w http.ResponseWriter, r *http.Request) (DocumentVersion, map[string]string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDocumentSize+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return DocumentVersion{}, nil, fmt.Errorf("multipart/form-data is required")
	}
	fields := map[string]string{}
	var version DocumentVersion
	fail := func(err error) (DocumentVersion, map[string]string, error) {
		a.discardBlob(version.BlobID)
		return DocumentVersion{}, nil, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(err)
		}
		name := part.FormName()
		if name == "file" {
			if version.BlobID != "" {
				part.Close()
				return fail(fmt.Errorf("only one file may be uploaded"))
			}
			fields["originalName"] = part.FileName()
			version, err = a.writeBlob(part)
			part.Close()
			if err != nil {
				return fail(err)
			}
			continue
		}
		if !slices.Contains([]string{"name", "description", "directoryId", "memberId", "visibility", "checksum"}, name) {
			part.Close()
			return fail(fmt.Errorf("unknown upload field"))
		}
		if _, ok := fields[name]; ok {
			part.Close()
			return fail(fmt.Errorf("duplicate upload field"))
		}
		b, err := io.ReadAll(io.LimitReader(part, 10001))
		part.Close()
		if err != nil {
			return fail(err)
		}
		if len(b) > 10000 {
			return fail(fmt.Errorf("upload metadata too large"))
		}
		fields[name] = string(b)
	}
	if version.BlobID == "" {
		return fail(fmt.Errorf("file field is required"))
	}
	return version, fields, nil
}
func inheritDocumentScope(s *State, input *documentInput) error {
	if input.DirectoryID != "" {
		d := directoryByID(s, input.DirectoryID)
		if d == nil {
			return fmt.Errorf("directory not found")
		}
		if input.Visibility == "" {
			input.Visibility = d.Visibility
		}
		if input.MemberID == "" {
			input.MemberID = d.MemberID
		}
	}
	if input.Visibility == "" {
		input.Visibility = "staff"
	}
	return nil
}
func (a *App) listDocuments(w http.ResponseWriter, r *http.Request, user Record) {
	s := a.store.Snapshot()
	dirs := []Record{}
	items := []Record{}
	for _, d := range s.Directories {
		if directoryReadable(&s, d, user) {
			dirs = append(dirs, legacyDirectoryView(&s, d))
		}
	}
	for _, d := range s.Documents {
		if documentReadable(&s, d, user) {
			items = append(items, legacyDocumentView(&s, d))
		}
	}
	result := Record{"directories": dirs, "items": items}
	if str(user, "role") == "admin" || str(user, "role") == "operator" {
		result["usedBytes"] = blobUsage(&s)
		result["quotaBytes"] = a.documentQuota
	}
	respond(w, 200, result)
}
func (a *App) publicDocuments(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		problem(w, 405, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/public/documents")
	if path == "" {
		a.listDocuments(w, r, nil)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[2] != "download" {
		problem(w, 404, "document not found")
		return
	}
	a.downloadDocument(w, r, nil, parts[1])
}

type documentSessionKey struct{}

func (a *App) documents(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	r = r.WithContext(context.WithValue(r.Context(), documentSessionKey{}, session))
	if r.URL.Path == "/api/documents/legacy" {
		a.legacyDocstoreArchive(w, r, user)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/directories") {
		a.directories(w, r, user)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/documents")
	staff := str(user, "role") == "admin" || str(user, "role") == "operator"
	if r.Method != "GET" && r.Method != "HEAD" && !staff {
		problem(w, 403, "document editing requires staff access")
		return
	}
	if path == "" {
		switch r.Method {
		case "GET":
			a.listDocuments(w, r, user)
		case "POST":
			a.uploadDocument(w, r, user, "")
		default:
			problem(w, 405, "method not allowed")
		}
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || len(parts) > 3 || !identifier.MatchString(parts[1]) {
		problem(w, 404, "document not found")
		return
	}
	id := parts[1]
	if r.Method != "GET" && r.Method != "HEAD" {
		snapshot := a.store.Snapshot()
		if legacyPatchAttachment(&snapshot, id) != nil || legacyDocstoreFile(&snapshot, id) != nil {
			problem(w, 409, "legacy attachment snapshots are read-only; upload a separate new document for changes")
			return
		}
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "download":
			if r.Method == "GET" || r.Method == "HEAD" {
				a.downloadDocument(w, r, user, id)
				return
			}
		case "content":
			if r.Method == "POST" {
				a.uploadDocument(w, r, user, id)
				return
			}
		case "text":
			a.documentText(w, r, user, id)
			return
		}
		problem(w, 405, "method not allowed")
		return
	}
	s := a.store.Snapshot()
	d := documentByID(&s, id)
	if d == nil || !documentReadable(&s, *d, user) {
		problem(w, 404, "document not found")
		return
	}
	if r.Method == "GET" {
		respond(w, 200, legacyDocumentView(&s, *d))
		return
	}
	if r.Method == "DELETE" {
		var blobs []string
		err := a.store.Update(func(s *State) error {
			if err := requireMutableDocument(s, id); err != nil {
				return err
			}
			d := documentByID(s, id)
			if d == nil {
				return fmt.Errorf("document not found")
			}
			blobs = append(blobs, d.BlobID)
			for _, v := range d.Revisions {
				blobs = append(blobs, v.BlobID)
			}
			s.Documents = slices.DeleteFunc(s.Documents, func(d Document) bool { return d.ID == id })
			if err := validatePatching(s); err != nil {
				return err
			}
			audit(s, str(user, "id"), "delete", "documents", id)
			return nil
		})
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		for _, blob := range blobs {
			a.discardBlob(blob)
		}
		respond(w, 200, Record{"ok": true})
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "method not allowed")
		return
	}
	var input documentInput
	if !decode(w, r, &input) {
		return
	}
	var result Document
	err := a.store.Update(func(s *State) error {
		if err := requireMutableDocument(s, id); err != nil {
			return err
		}
		d := documentByID(s, id)
		if d == nil {
			return fmt.Errorf("document not found")
		}
		if err := inheritDocumentScope(s, &input); err != nil {
			return err
		}
		d.Name = input.Name
		d.Description = input.Description
		d.DirectoryID = input.DirectoryID
		d.MemberID = input.MemberID
		d.Visibility = input.Visibility
		d.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateDocuments(s); err != nil {
			return err
		}
		if err := validatePatching(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), "update", "documents", id)
		result = *d
		return nil
	})
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	respond(w, 200, documentView(result))
}
func (a *App) uploadDocument(w http.ResponseWriter, r *http.Request, user Record, id string) {
	version, fields, err := a.readUpload(w, r)
	if err != nil {
		status := 400
		var maxError *http.MaxBytesError
		if errors.Is(err, errDocumentTooLarge) || errors.As(err, &maxError) {
			status = 413
		}
		problem(w, status, err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			a.discardBlob(version.BlobID)
		}
	}()
	var result Document
	var stale []string
	err = a.store.Update(func(s *State) error {
		if id != "" {
			if err := requireMutableDocument(s, id); err != nil {
				return err
			}
			d := documentByID(s, id)
			if d == nil {
				return fmt.Errorf("document not found")
			}
			if fields["checksum"] == "" || fields["checksum"] != d.Checksum {
				return fmt.Errorf("document changed; reload and retry with its current checksum")
			}
			stale = replaceDocument(d, version)
			result = *d
		} else {
			input := documentInput{Name: fields["name"], Description: fields["description"], DirectoryID: fields["directoryId"], MemberID: fields["memberId"], Visibility: fields["visibility"]}
			if input.Name == "" {
				input.Name = fields["originalName"]
			}
			if err := inheritDocumentScope(s, &input); err != nil {
				return err
			}
			result = Document{ID: token(16), Name: input.Name, Description: input.Description, DirectoryID: input.DirectoryID, MemberID: input.MemberID, Visibility: input.Visibility, CreatedAt: version.CreatedAt, UpdatedAt: version.CreatedAt, CreatedBy: str(user, "id"), Version: 1, BlobID: version.BlobID, Size: version.Size, MediaType: version.MediaType, Checksum: version.Checksum}
			s.Documents = append(s.Documents, result)
		}
		if err := validateDocuments(s); err != nil {
			return err
		}
		if blobUsage(s) > a.documentQuota {
			return fmt.Errorf("document quota exceeded")
		}
		audit(s, str(user, "id"), "upload", "documents", result.ID)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	committed = true
	for _, blob := range stale {
		a.discardBlob(blob)
	}
	status := 201
	if id != "" {
		status = 200
	}
	respond(w, status, documentView(result))
}
func replaceDocument(d *Document, v DocumentVersion) []string {
	d.Revisions = append(d.Revisions, DocumentVersion{Version: d.Version, BlobID: d.BlobID, Size: d.Size, MediaType: d.MediaType, Checksum: d.Checksum, CreatedAt: d.UpdatedAt})
	stale := []string{}
	for len(d.Revisions) > maxDocumentVersions {
		stale = append(stale, d.Revisions[0].BlobID)
		d.Revisions = d.Revisions[1:]
	}
	d.Version++
	d.BlobID = v.BlobID
	d.Size = v.Size
	d.MediaType = v.MediaType
	d.Checksum = v.Checksum
	d.UpdatedAt = v.CreatedAt
	return stale
}
func (a *App) downloadDocument(w http.ResponseWriter, r *http.Request, user Record, id string) {
	s := a.store.Snapshot()
	d := documentByID(&s, id)
	if d == nil || !documentReadable(&s, *d, user) {
		problem(w, 404, "document not found")
		return
	}
	f, err := a.openBlob(d.BlobID)
	if err != nil {
		problem(w, 500, "document content unavailable")
		return
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil || size != d.Size || hex.EncodeToString(h.Sum(nil)) != d.Checksum {
		problem(w, 500, "document integrity check failed")
		return
	}
	if _, err = f.Seek(0, 0); err != nil {
		problem(w, 500, "document content unavailable")
		return
	}
	if err = a.commitDocumentRead(r, user, *d, "download", r.Method != "HEAD"); err != nil {
		problem(w, 404, "document access or current allocation changed")
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Name}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("ETag", `"`+d.Checksum+`"`)
	updated, _ := time.Parse(time.RFC3339Nano, d.UpdatedAt)
	http.ServeContent(w, r, d.Name, updated, f)
}
func (a *App) documentText(w http.ResponseWriter, r *http.Request, user Record, id string) {
	s := a.store.Snapshot()
	d := documentByID(&s, id)
	if d == nil || !documentReadable(&s, *d, user) {
		problem(w, 404, "document not found")
		return
	}
	extension := strings.ToLower(filepath.Ext(d.Name))
	if extension != ".txt" && extension != ".md" {
		problem(w, 415, "text editing supports .txt and .md only")
		return
	}
	if r.Method == "GET" {
		if d.Size > maxTextSize {
			problem(w, 413, "text preview is limited to 1 MiB")
			return
		}
		f, err := a.openBlob(d.BlobID)
		if err != nil {
			problem(w, 500, "document content unavailable")
			return
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxTextSize+1))
		sum := sha256.Sum256(b)
		if err != nil || int64(len(b)) != d.Size || hex.EncodeToString(sum[:]) != d.Checksum || !utf8.Valid(b) {
			problem(w, 422, "document is not valid UTF-8 text or failed integrity check")
			return
		}
		if err = a.commitDocumentRead(r, user, *d, "view", true); err != nil {
			problem(w, 404, "document access or current allocation changed")
			return
		}
		respond(w, 200, Record{"content": string(b), "checksum": d.Checksum})
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "method not allowed")
		return
	}
	var body struct {
		Content  string `json:"content"`
		Checksum string `json:"checksum"`
	}
	if !decode(w, r, &body) {
		return
	}
	if int64(len(body.Content)) > maxTextSize || !utf8.ValidString(body.Content) {
		problem(w, 400, "text must be UTF-8 and at most 1 MiB")
		return
	}
	v, err := a.writeBlob(strings.NewReader(body.Content))
	if err != nil {
		problem(w, 500, "could not save document")
		return
	}
	committed := false
	defer func() {
		if !committed {
			a.discardBlob(v.BlobID)
		}
	}()
	var result Document
	var stale []string
	err = a.store.Update(func(s *State) error {
		if err := requireMutableDocument(s, id); err != nil {
			return err
		}
		d := documentByID(s, id)
		if d == nil || d.Checksum != body.Checksum {
			return fmt.Errorf("document changed; reload before saving")
		}
		stale = replaceDocument(d, v)
		if blobUsage(s) > a.documentQuota {
			return fmt.Errorf("document quota exceeded")
		}
		result = *d
		audit(s, str(user, "id"), "edit", "documents", id)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	committed = true
	for _, blob := range stale {
		a.discardBlob(blob)
	}
	respond(w, 200, documentView(result))
}
func (a *App) directories(w http.ResponseWriter, r *http.Request, user Record) {
	if str(user, "role") != "admin" && str(user, "role") != "operator" {
		problem(w, 403, "directory editing requires staff access")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/directories")
	id := ""
	if path != "" {
		if strings.Count(path, "/") != 1 || !identifier.MatchString(path[1:]) {
			problem(w, 404, "directory not found")
			return
		}
		id = path[1:]
	}
	if (r.Method == "POST" && id != "") || ((r.Method == "PUT" || r.Method == "DELETE") && id == "") {
		problem(w, 400, "invalid directory URL")
		return
	}
	if r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
		problem(w, 405, "method not allowed")
		return
	}
	var body directoryInput
	if r.Method != "DELETE" && !decode(w, r, &body) {
		return
	}
	var result Directory
	err := a.store.Update(func(s *State) error {
		old := directoryByID(s, id)
		if legacyPatchDirectory(s, id) != nil || legacyDocstoreDirectory(s, id) != nil {
			return errors.New("legacy attachment directories are read-only")
		}
		if r.Method != "POST" && old == nil {
			return fmt.Errorf("directory not found")
		}
		if r.Method == "DELETE" {
			for _, d := range s.Directories {
				if d.ParentID == id {
					return fmt.Errorf("directory is not empty")
				}
			}
			for _, d := range s.Documents {
				if d.DirectoryID == id {
					return fmt.Errorf("directory is not empty")
				}
			}
			s.Directories = slices.DeleteFunc(s.Directories, func(d Directory) bool { return d.ID == id })
			audit(s, str(user, "id"), "delete", "directories", id)
			return nil
		}
		if body.ParentID != "" {
			parent := directoryByID(s, body.ParentID)
			if parent == nil {
				return fmt.Errorf("parent directory not found")
			}
			if body.Visibility == "" {
				body.Visibility = parent.Visibility
			}
			if body.MemberID == "" {
				body.MemberID = parent.MemberID
			}
		}
		if body.Visibility == "" {
			body.Visibility = "staff"
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result = Directory{ID: id, Name: body.Name, Description: body.Description, ParentID: body.ParentID, MemberID: body.MemberID, Visibility: body.Visibility, CreatedAt: now, UpdatedAt: now}
		if old == nil {
			result.ID = token(16)
			s.Directories = append(s.Directories, result)
		} else {
			result.CreatedAt = old.CreatedAt
			*old = result
			subtree := map[string]bool{id: true}
			for pass := 0; pass <= len(s.Directories); pass++ {
				changed := false
				for _, d := range s.Directories {
					if subtree[d.ParentID] && !subtree[d.ID] {
						subtree[d.ID] = true
						changed = true
					}
				}
				if !changed {
					break
				}
			}
			for i := range s.Directories {
				d := &s.Directories[i]
				if subtree[d.ID] {
					d.Visibility = body.Visibility
					d.MemberID = body.MemberID
					d.UpdatedAt = now
				}
			}
			for i := range s.Documents {
				d := &s.Documents[i]
				if subtree[d.DirectoryID] {
					d.Visibility = body.Visibility
					d.MemberID = body.MemberID
					d.UpdatedAt = now
				}
			}
		}
		if err := validateDocuments(s); err != nil {
			return err
		}
		if err := validatePatching(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), strings.ToLower(r.Method), "directories", result.ID)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if r.Method == "DELETE" {
		respond(w, 200, Record{"ok": true})
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	respond(w, status, result)
}
