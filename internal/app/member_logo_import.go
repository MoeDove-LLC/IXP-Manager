package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (a *App) logoBundleReview(s *State, b *migrate.LogoBundle) (Record, error) {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	checksum := hex.EncodeToString(sum[:])
	items := []Record{}
	bindings := []Record{}
	var size int64
	for _, item := range b.Items {
		m := find(s, "members", item.Source.MemberID)
		if m == nil {
			return nil, errors.New("import the corresponding member inventory before its logos")
		}
		if _, exists := s.MemberLogos[item.Source.MemberID]; exists {
			return nil, errors.New("member already has logo history; use the explicit logo editor instead of overwriting it with a legacy import")
		}
		size += int64(len(item.PNG))
		bindings = append(bindings, Record{"memberId": str(m, "id"), "etag": a.resourceETag("members", m), "logoRevision": a.logoRevision(s, m)})
		items = append(items, Record{"memberId": str(m, "id"), "memberName": m["name"], "source": item.Source, "width": item.Width, "height": item.Height, "size": len(item.PNG), "published": false})
	}
	return Record{"checksum": checksum, "reviewToken": a.coreHMAC("logo-import", Record{"checksum": checksum, "members": bindings}), "items": items, "bytes": size, "published": false}, nil
}

func readLogoImport(w http.ResponseWriter, r *http.Request) (*migrate.LogoBundle, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, migrate.LogoBundleLimit+(64<<10))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", errors.New("multipart logo bundle required")
	}
	seen := map[string]bool{}
	var bundle *migrate.LogoBundle
	review := ""
	for {
		p, e := mr.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, "", e
		}
		name := p.FormName()
		if seen[name] || !contains([]string{"bundle", "reviewToken"}, name) {
			p.Close()
			return nil, "", errors.New("unexpected or duplicate logo import field")
		}
		seen[name] = true
		if name == "bundle" {
			bundle, e = migrate.ReadLogoBundle(p)
		} else {
			var raw []byte
			raw, e = io.ReadAll(io.LimitReader(p, 1025))
			if len(raw) > 1024 {
				e = errors.New("review token too long")
			}
			review = string(raw)
		}
		p.Close()
		if e != nil {
			return nil, "", e
		}
	}
	if bundle == nil {
		return nil, "", errors.New("one logo bundle is required")
	}
	return bundle, review, nil
}
func (a *App) importMemberLogos(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	if !a.beginLogoWork(w) {
		return
	}
	defer a.endLogoWork()
	b, expected, err := readLogoImport(w, r)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s := a.store.Snapshot()
	actor, err := a.logoActor(&s, user, session)
	if err != nil || str(actor, "role") != "admin" {
		problem(w, 403, "import permission changed")
		return
	}
	review, err := a.logoBundleReview(&s, b)
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if strings.HasSuffix(r.URL.Path, "/preview") {
		respond(w, 200, review)
		return
	}
	if expected == "" {
		problem(w, 428, "preview the bundle and explicitly confirm the target members first")
		return
	}
	if expected != str(review, "reviewToken") {
		problem(w, 409, "bundle or member inventory changed; preview again")
		return
	}
	var staged []DocumentVersion
	committed := false
	defer func() {
		if !committed {
			for _, v := range staged {
				a.discardBlob(v.BlobID)
			}
		}
	}()
	var stagedSize int64
	for _, item := range b.Items {
		stagedSize += int64(len(item.PNG))
		if blobUsage(&s)+stagedSize > a.documentQuota {
			problem(w, 413, "shared file quota exceeded")
			return
		}
		v, e := a.writeBlob(bytes.NewReader(item.PNG))
		if e != nil {
			problem(w, 500, "could not store imported logo")
			return
		}
		staged = append(staged, v)
	}
	status := 409
	err = a.store.Update(func(current *State) error {
		live, e := a.logoActor(current, user, session)
		if e != nil || str(live, "role") != "admin" {
			status = 403
			return errors.New("import authority changed")
		}
		nowReview, e := a.logoBundleReview(current, b)
		if e != nil {
			return e
		}
		if str(nowReview, "reviewToken") != expected {
			return errors.New("bundle or member inventory changed; preview again")
		}
		if current.MemberLogos == nil {
			current.MemberLogos = map[string]MemberLogo{}
		}
		for i, item := range b.Items {
			m := find(current, "members", item.Source.MemberID)
			v := staged[i]
			current.MemberLogos[item.Source.MemberID] = MemberLogo{MemberID: item.Source.MemberID, MemberCreatedAt: str(m, "createdAt"), Revision: token(24), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), BlobID: v.BlobID, Checksum: v.Checksum, Size: v.Size, Width: item.Width, Height: item.Height, Published: false, LegacySource: &item.Source}
			audit(current, str(live, "id"), "legacy-logo-import", "members", item.Source.MemberID)
		}
		if blobUsage(current) > a.documentQuota {
			status = 413
			return errors.New("shared file quota exceeded")
		}
		return validateMemberLogos(current)
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	committed = true
	respond(w, 200, Record{"imported": len(staged), "published": false, "checksum": review["checksum"]})
}
