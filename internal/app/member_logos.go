package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/memberlogo"
	"github.com/moedove/ixp-manager/internal/migrate"
)

// Each logical owner has its own immutable blob. A removed logo keeps a random
// revision tombstone, preventing delete/recreate ABA and cross-entity reuse.
type MemberLogo struct {
	MemberID        string              `json:"memberId"`
	MemberCreatedAt string              `json:"memberCreatedAt"`
	Revision        string              `json:"revision"`
	UpdatedAt       string              `json:"updatedAt"`
	BlobID          string              `json:"blobId,omitempty"`
	Checksum        string              `json:"checksum,omitempty"`
	Size            int64               `json:"size,omitempty"`
	Width           int                 `json:"width,omitempty"`
	Height          int                 `json:"height,omitempty"`
	Published       bool                `json:"published"`
	LegacySource    *migrate.LogoSource `json:"legacySource,omitempty"`
}

func logoUsage(s *State) int64 {
	var n int64
	for _, v := range s.MemberLogos {
		n += v.Size
	}
	return n
}
func blobUsage(s *State) int64 { return documentUsage(s) + loaUsage(s) + logoUsage(s) }

func validateMemberLogos(s *State) error {
	seen := map[string]bool{}
	for _, d := range s.Documents {
		seen[d.BlobID] = true
		for _, v := range d.Revisions {
			seen[v.BlobID] = true
		}
	}
	for _, l := range s.LOAs {
		seen[l.PDFBlobID], seen[l.HTMLBlobID] = true, true
	}
	for id, l := range s.MemberLogos {
		if id != l.MemberID || !identifier.MatchString(id) || !identifier.MatchString(l.Revision) || l.MemberCreatedAt == "" {
			return errors.New("invalid member logo identity")
		}
		if _, err := time.Parse(time.RFC3339Nano, l.UpdatedAt); err != nil {
			return errors.New("invalid member logo timestamp")
		}
		if l.BlobID == "" {
			if l.Published || l.Size != 0 || l.Checksum != "" || l.Width != 0 || l.Height != 0 || l.LegacySource != nil {
				return errors.New("invalid removed member logo")
			}
			continue
		}
		member := find(s, "members", id)
		if member == nil || str(member, "createdAt") != l.MemberCreatedAt || !blobName.MatchString(l.BlobID) || seen[l.BlobID] || !digestPattern.MatchString(l.Checksum) || l.Size < 1 || l.Size > memberlogo.MaxOutputBytes || l.Width < 1 || l.Height < 1 || l.Width > memberlogo.MaxDimension || l.Height > memberlogo.MaxDimension || int64(l.Width)*int64(l.Height) > memberlogo.MaxPixels {
			return errors.New("invalid member logo content or owner")
		}
		if l.LegacySource != nil {
			if e := migrate.ValidateLogoSource(*l.LegacySource); e != nil || l.LegacySource.MemberID != id {
				return errors.New("invalid member logo legacy provenance")
			}
		}
		seen[l.BlobID] = true
	}
	return nil
}

func (a *App) logoRevision(s *State, member Record) string {
	return a.coreHMAC("member-logo", Record{"memberId": str(member, "id"), "memberCreatedAt": str(member, "createdAt"), "logo": s.MemberLogos[str(member, "id")]})
}
func logoCanEdit(user Record, id string) bool {
	role := str(user, "role")
	return role == "admin" || role == "operator" || (role == "member" && str(user, "memberId") == id && memberAdministrator(user, id))
}
func logoCanRead(user Record, id string) bool {
	return contains([]string{"admin", "operator", "viewer"}, str(user, "role")) || (str(user, "role") == "member" && str(user, "memberId") == id)
}

// Store -> session lock order also checks the selected workspace and CSRF
// context. A slow upload from the previous workspace must not commit.
func (a *App) logoActor(s *State, user Record, session Session) (Record, error) {
	if session.API {
		return nil, errors.New("member logos require a browser session")
	}
	actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, current := range a.sessions {
		if current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation && current.CSRF == session.CSRF && current.MemberID == session.MemberID && time.Now().Before(current.Expires) {
			return actor, nil
		}
	}
	return nil, errors.New("session or selected member changed; reload this workspace")
}

func (a *App) logoView(s *State, member, user Record) Record {
	id := str(member, "id")
	l := s.MemberLogos[id]
	configured := l.BlobID != "" && l.MemberCreatedAt == str(member, "createdAt")
	public := configured && l.Published && str(member, "status") == "active"
	result := Record{"memberId": id, "memberName": member["name"], "configured": configured, "revision": a.logoRevision(s, member), "canEdit": logoCanEdit(user, id), "published": configured && l.Published, "publiclyAvailable": public}
	if configured {
		result["width"], result["height"], result["size"], result["updatedAt"] = l.Width, l.Height, l.Size, l.UpdatedAt
		result["previewUrl"] = "/api/members/" + id + "/logo/content?revision=" + a.logoRevision(s, member)
		if public {
			result["publicUrl"] = "/api/public/members/" + id + "/logo"
		}
		if l.LegacySource != nil {
			result["imported"] = true
			if str(user, "role") != "member" {
				result["legacySource"] = l.LegacySource
			}
		}
	}
	return result
}

func logoRoute(path string) (id string, content, public, matched bool) {
	if path == "/api/member/logo" || path == "/api/member/logo/content" {
		return "", strings.HasSuffix(path, "/content"), false, true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	if len(parts) == 4 && parts[0] == "public" && parts[1] == "members" && parts[3] == "logo" {
		return parts[2], true, true, true
	}
	if (len(parts) == 3 || len(parts) == 4) && parts[0] == "members" && parts[2] == "logo" && (len(parts) == 3 || parts[3] == "content") {
		return parts[1], len(parts) == 4, false, true
	}
	return "", false, false, false
}

func (a *App) memberLogoRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	id, content, public, matched := logoRoute(r.URL.Path)
	if !matched {
		return false
	}
	if public && r.Method != "GET" && r.Method != "HEAD" {
		problem(w, 405, "method not allowed")
		return true
	}
	s := a.store.Snapshot()
	actor := user
	if !public {
		var err error
		actor, err = a.logoActor(&s, user, session)
		if err != nil {
			problem(w, 403, err.Error())
			return true
		}
		if id == "" {
			id = str(actor, "memberId")
		}
	}
	member := find(&s, "members", id)
	if member == nil || (!public && !logoCanRead(actor, id)) {
		problem(w, 404, "member logo not found")
		return true
	}
	if content {
		if r.Method != "GET" && r.Method != "HEAD" {
			problem(w, 405, "method not allowed")
			return true
		}
		a.serveMemberLogo(w, r, &s, member, actor, session, public)
		return true
	}
	if r.Method == "GET" {
		respond(w, 200, a.logoView(&s, member, actor))
		return true
	}
	if !contains([]string{"POST", "PUT", "DELETE"}, r.Method) {
		problem(w, 405, "method not allowed")
		return true
	}
	if !logoCanEdit(actor, id) {
		problem(w, 403, "member administrator or staff write permission required")
		return true
	}
	if r.Method == "POST" {
		if !a.beginLogoWork(w) {
			return true
		}
		defer a.endLogoWork()
	}
	var expected string
	var published *bool
	var image memberlogo.Image
	if r.Method == "POST" {
		var err error
		expected, published, image, err = parseLogoUpload(w, r)
		if err != nil {
			problem(w, 400, err.Error())
			return true
		}
	} else {
		var body struct {
			ExpectedRevision string `json:"expectedRevision"`
			Published        *bool  `json:"published"`
		}
		if !decode(w, r, &body) {
			return true
		}
		expected, published = body.ExpectedRevision, body.Published
		if r.Method == "PUT" && published == nil {
			problem(w, 400, "published must be an explicit boolean")
			return true
		}
		if r.Method == "DELETE" && published != nil {
			problem(w, 400, "delete accepts only expectedRevision")
			return true
		}
	}
	if expected == "" {
		problem(w, 428, "review the current logo before changing it")
		return true
	}
	if expected != a.logoRevision(&s, member) {
		problem(w, 409, "logo or member identity changed; reload and review before retrying")
		return true
	}
	var staged DocumentVersion
	if r.Method == "POST" {
		if blobUsage(&s)-s.MemberLogos[id].Size+int64(len(image.PNG)) > a.documentQuota {
			problem(w, 413, "shared file quota exceeded")
			return true
		}
		var err error
		staged, err = a.writeBlob(bytes.NewReader(image.PNG))
		if err != nil {
			problem(w, 500, "could not store logo")
			return true
		}
	}
	committed := false
	defer func() {
		if !committed && staged.BlobID != "" {
			a.discardBlob(staged.BlobID)
		}
	}()
	var result Record
	var oldBlob string
	status := 409
	err := a.store.Update(func(current *State) error {
		live, e := a.logoActor(current, user, session)
		if e != nil {
			status = 403
			return e
		}
		m := find(current, "members", id)
		if m == nil || !logoCanRead(live, id) {
			status = 404
			return errors.New("member logo not found")
		}
		if !logoCanEdit(live, id) {
			status = 403
			return errors.New("logo write permission changed")
		}
		if a.logoRevision(current, m) != expected {
			return errors.New("logo or member identity changed; reload and review before retrying")
		}
		old := current.MemberLogos[id]
		l := old
		if r.Method == "PUT" && old.BlobID == "" {
			return errors.New("upload a logo before publishing")
		}
		if r.Method == "DELETE" {
			l = MemberLogo{}
		} else if r.Method == "POST" {
			l = MemberLogo{BlobID: staged.BlobID, Checksum: staged.Checksum, Size: staged.Size, Width: image.Width, Height: image.Height, Published: old.Published}
		}
		if published != nil && r.Method != "DELETE" {
			l.Published = *published
		}
		l.MemberID, l.MemberCreatedAt, l.Revision, l.UpdatedAt = id, str(m, "createdAt"), token(24), time.Now().UTC().Format(time.RFC3339Nano)
		if current.MemberLogos == nil {
			current.MemberLogos = map[string]MemberLogo{}
		}
		current.MemberLogos[id] = l
		if blobUsage(current) > a.documentQuota {
			status = 413
			return errors.New("shared file quota exceeded")
		}
		if e = validateMemberLogos(current); e != nil {
			return e
		}
		if old.BlobID != l.BlobID {
			oldBlob = old.BlobID
		}
		audit(current, str(live, "id"), "member-logo-"+strings.ToLower(r.Method), "members", id)
		result = a.logoView(current, m, live)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return true
	}
	committed = true
	if oldBlob != "" {
		a.discardBlob(oldBlob)
	}
	respond(w, 200, result)
	return true
}

func parseLogoUpload(w http.ResponseWriter, r *http.Request) (expected string, published *bool, img memberlogo.Image, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, memberlogo.MaxInputBytes+(64<<10))
	mr, e := r.MultipartReader()
	if e != nil {
		err = errors.New("multipart file and expectedRevision are required")
		return
	}
	seen := map[string]bool{}
	for {
		p, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			err = e
			return
		}
		name := p.FormName()
		if seen[name] || !contains([]string{"file", "expectedRevision", "published"}, name) {
			p.Close()
			err = errors.New("unexpected or duplicate logo upload field")
			return
		}
		seen[name] = true
		if name == "file" {
			img, e = memberlogo.Normalize(p)
		} else {
			var value []byte
			value, e = io.ReadAll(io.LimitReader(p, 1025))
			if len(value) > 1024 {
				e = errors.New("logo upload field too large")
			}
			if name == "expectedRevision" {
				expected = string(value)
			} else if e == nil {
				if string(value) != "true" && string(value) != "false" {
					e = errors.New("published must be true or false")
				} else {
					v := string(value) == "true"
					published = &v
				}
			}
		}
		p.Close()
		if e != nil {
			err = e
			return
		}
	}
	if !seen["file"] {
		err = errors.New("one PNG or JPEG file is required")
	}
	return
}

func (a *App) serveMemberLogo(w http.ResponseWriter, r *http.Request, s *State, member, user Record, session Session, public bool) {
	id := str(member, "id")
	opened := s.MemberLogos[id]
	allowed := func(current *State) bool {
		m := find(current, "members", id)
		l := current.MemberLogos[id]
		if m == nil || l.BlobID == "" || l.BlobID != opened.BlobID || l.Checksum != opened.Checksum || l.Revision != opened.Revision || l.MemberCreatedAt != str(m, "createdAt") {
			return false
		}
		if public {
			return l.Published && str(m, "status") == "active"
		}
		actor, e := a.logoActor(current, user, session)
		return e == nil && logoCanRead(actor, id)
	}
	if !allowed(s) {
		problem(w, 404, "member logo not found")
		return
	}
	f, err := a.openBlob(opened.BlobID)
	if err != nil {
		problem(w, 500, "logo content unavailable")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, memberlogo.MaxOutputBytes+1))
	sum := sha256.Sum256(data)
	if err != nil || int64(len(data)) != opened.Size || hex.EncodeToString(sum[:]) != opened.Checksum {
		problem(w, 500, "logo content integrity check failed")
		return
	}
	current := a.store.Snapshot()
	if !allowed(&current) {
		problem(w, 404, "member logo not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", "inline; filename=member-logo.png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
}

func logoDeleteGuard(s *State, kind, id string) error {
	if kind == "members" && s.MemberLogos[id].BlobID != "" {
		return fmt.Errorf("member has a logo; remove it explicitly before deleting the member")
	}
	return nil
}

func (a *App) beginLogoWork(w http.ResponseWriter) bool {
	select {
	case a.logoWork <- struct{}{}:
		return true
	default:
		problem(w, 429, "image processing is busy; retry shortly")
		return false
	}
}
func (a *App) endLogoWork() { <-a.logoWork }

// Backup and startup independently validate passive image bytes and metadata.
// This never modifies an artifact or performs network access.
func ValidateMemberLogoArtifacts(store *Store) error {
	s := store.Snapshot()
	if err := validateMemberLogos(&s); err != nil {
		return err
	}
	a := &App{store: store}
	for _, l := range s.MemberLogos {
		if l.BlobID == "" {
			continue
		}
		f, err := a.openBlob(l.BlobID)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, memberlogo.MaxOutputBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != l.Size || hex.EncodeToString(sum[:]) != l.Checksum {
			return errors.New("member logo content integrity mismatch")
		}
		img, err := memberlogo.Normalize(bytes.NewReader(data))
		if err != nil || img.Width != l.Width || img.Height != l.Height || !bytes.Equal(img.PNG, data) {
			return errors.New("member logo is not a matching normalized passive PNG")
		}
	}
	return nil
}
