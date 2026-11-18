package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/migrate"
)

type resourceImport struct {
	Version        int                     `json:"version"`
	Resources      map[string][]Record     `json:"resources"`
	CoreBundles    []core.Bundle           `json:"coreBundles"`
	SFlowReceivers []SFlowReceiverImport   `json:"sflowReceivers"`
	Patching       *migrate.PatchingImport `json:"patching,omitempty"`
	Warnings       []string                `json:"warnings"`
	Source         string                  `json:"source"`
}

func (a *App) appendImportedResources(s *State, user Record, body resourceImport) (int, error) {
	n := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for kind, items := range body.Resources {
		if kind == "users" {
			return 0, errors.New("user accounts cannot be imported")
		}
		if _, ok := Schemas[kind]; !ok {
			return 0, fmt.Errorf("unknown resource %s", kind)
		}
		for _, original := range items {
			item := Record{}
			for k, v := range original {
				item[k] = v
			}
			id := str(item, "id")
			if !identifier.MatchString(id) || find(s, kind, id) != nil {
				return 0, fmt.Errorf("invalid or duplicate id %s/%s", kind, id)
			}
			if _, ok := item["passwordHash"]; ok {
				return 0, errors.New("password hashes cannot be imported")
			}
			if kind == "patch-ports" {
				if err := guardPatchResourceMutation(s, kind, "POST", nil, item); err != nil {
					return 0, err
				}
			}
			item["createdAt"], item["updatedAt"] = now, now
			setOwnershipTime(kind, nil, item, now)
			s.Resources[kind] = append(s.Resources[kind], item)
			if kind == "patch-ports" {
				if err := recordPatchResourceChange(s, user, "POST", nil, item); err != nil {
					return 0, err
				}
			}
			n++
		}
	}
	if err := importCoreBundles(s, body.CoreBundles); err != nil {
		return 0, err
	}
	if err := a.importSFlowReceivers(s, body.SFlowReceivers); err != nil {
		return 0, err
	}
	return n + len(body.CoreBundles) + len(body.SFlowReceivers), nil
}
func validateImportedState(s *State) error {
	normalizeLegacyCorePorts(s)
	if err := validateAll(s); err != nil {
		return err
	}
	if err := validateDocuments(s); err != nil {
		return err
	}
	if err := normalizeLegacyPatching(s); err != nil {
		return err
	}
	if err := validateDevices(s); err != nil {
		return err
	}
	if err := validatePeering(s); err != nil {
		return err
	}
	return validatePatching(s)
}

func (a *App) importPatchBundle(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if session.API || str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, migrate.PatchCompressedLimit)
	multipart, err := r.MultipartReader()
	if err != nil {
		problem(w, 400, "multipart bundle field required")
		return
	}
	part, err := multipart.NextPart()
	if err != nil || part.FormName() != "bundle" || part.FileName() == "" {
		problem(w, 400, "one patch bundle file required")
		return
	}
	versions := map[string]DocumentVersion{}
	snapshot := a.store.Snapshot()
	remaining := a.documentQuota - blobUsage(&snapshot)
	var stagedBytes int64
	committed := false
	defer func() {
		if !committed {
			for _, v := range versions {
				a.discardBlob(v.BlobID)
			}
		}
	}()
	bundle, err := migrate.ReadPatchBundle(part, func(file migrate.PatchFileSource, reader io.Reader) error {
		if file.Size > remaining-stagedBytes {
			return errors.New("document quota cannot fit this patch bundle")
		}
		stagedBytes += file.Size
		version, e := a.writeBlob(reader)
		if e != nil {
			return e
		}
		versions[file.ID] = version
		if version.Size != file.Size || version.Checksum != file.Checksum {
			return errors.New("attachment content integrity mismatch")
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
		problem(w, 400, "exactly one patch bundle field is allowed")
		return
	}
	body := resourceImport{Version: bundle.Version, Resources: map[string][]Record{}, CoreBundles: bundle.CoreBundles, Warnings: bundle.Warnings, Source: bundle.Source}
	for kind, items := range bundle.Resources {
		for _, item := range items {
			body.Resources[kind] = append(body.Resources[kind], Record(item))
		}
	}
	for _, item := range bundle.SFlowReceivers {
		body.SFlowReceivers = append(body.SFlowReceivers, SFlowReceiverImport{Name: item.Name, Spec: item.Spec})
	}
	receipt := patchHash(bundle)
	var result LegacyPatchBatch
	n := 0
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
		n, e = a.appendImportedResources(s, actor, body)
		if e != nil {
			return e
		}
		result, e = importLegacyPatching(s, actor, &bundle.Patching, versions, receipt)
		if e != nil {
			return e
		}
		if blobUsage(s) > a.documentQuota {
			return errors.New("document quota exceeded")
		}
		if e = validateImportedState(s); e != nil {
			return e
		}
		audit(s, str(actor, "id"), "legacy-patch-import", "patching", receipt)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	committed = true
	respond(w, 200, Record{"imported": n, "patching": result, "warnings": bundle.Warnings, "legacySnapshots": true, "loaAuthorizationImported": false})
}
