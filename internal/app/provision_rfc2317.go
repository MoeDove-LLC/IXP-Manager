package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

const rfc2317ProfileID = "rfc2317"
const rfc2317ReleaseFilename = "rfc2317-complete-zones.json"

// Pair admission binds both approval heads, including the distinction between a
// missing head and serial zero. The candidate and input digests stay private.
type RFC2317Revision struct {
	ParentZone           string  `json:"parentZone"`
	ChildZone            string  `json:"childZone"`
	PreviousParentSerial uint32  `json:"previousParentSerial"`
	ParentSerial         uint32  `json:"parentSerial"`
	PreviousChildSerial  *uint32 `json:"previousChildSerial,omitempty"`
	ChildSerial          uint32  `json:"childSerial"`
	NewChildZone         bool    `json:"newChildZone"`
	AddedRecords         int     `json:"addedRecords"`
	ParentSnapshotSHA256 string  `json:"parentSnapshotSHA256"`
	ParentSHA256         string  `json:"parentSHA256"`
	ChildSHA256          string  `json:"childSHA256"`
	ParentApproval       *uint32 `json:"parentApproval,omitempty"`
	ChildApproval        *uint32 `json:"childApproval,omitempty"`
}

func rfc2317Revision(bundle provision.RFC2317MergedBundle) RFC2317Revision {
	input := bundle.Review.Input
	return RFC2317Revision{
		ParentZone: input.Parent.Zone, ChildZone: input.ChildZone, NewChildZone: input.NewChildZone,
		PreviousParentSerial: bundle.PreviousParentSerial, ParentSerial: bundle.ParentSerial,
		PreviousChildSerial: input.PreviousChildSerial, ChildSerial: bundle.ChildSerial, AddedRecords: bundle.AddedRecords,
		ParentSnapshotSHA256: bundle.ParentSnapshotSHA256, ParentSHA256: bundle.Files[1].SHA256, ChildSHA256: bundle.Files[0].SHA256,
	}
}

func (p RFC2317Revision) metadata() Record {
	result := Record{"parentZone": p.ParentZone, "childZone": p.ChildZone, "previousParentSerial": p.PreviousParentSerial,
		"parentSerial": p.ParentSerial, "childSerial": p.ChildSerial, "newChildZone": p.NewChildZone, "addedRecords": p.AddedRecords}
	if p.PreviousChildSerial != nil {
		result["previousChildSerial"] = *p.PreviousChildSerial
	}
	return result
}

func (p RFC2317Revision) checksum() string {
	raw, _ := json.Marshal(p)
	return digest(string(raw))
}

func (p RFC2317Revision) valid() bool {
	childLabel, attached := strings.CutSuffix(p.ChildZone, "."+p.ParentZone)
	parentOrder, err := provision.CompareSerial(p.ParentSerial, p.PreviousParentSerial)
	if !attached || strings.Contains(childLabel, ".") || !provisionSerialZone(p.ParentZone) || !provisionSerialZone(p.ChildZone) ||
		!strings.HasSuffix(p.ParentZone, ".in-addr.arpa.") || err != nil || parentOrder <= 0 || p.ParentSerial == 0 || p.ChildSerial == 0 ||
		p.NewChildZone != (p.PreviousChildSerial == nil) || p.AddedRecords < 0 || p.AddedRecords > 384 ||
		!externalDigest(p.ParentSnapshotSHA256) || !externalDigest(p.ParentSHA256) || !externalDigest(p.ChildSHA256) {
		return false
	}
	if p.PreviousChildSerial != nil {
		order, err := provision.CompareSerial(p.ChildSerial, *p.PreviousChildSerial)
		return err == nil && order > 0
	}
	return true
}

func rfc2317PairContent(d ProvisionRevision, content string) (provision.RFC2317MergedBundle, error) {
	bundle, err := provision.DecodeRFC2317Merged([]byte(content))
	if err != nil {
		return bundle, err
	}
	if d.RFC2317 == nil {
		return bundle, errors.New("RFC 2317 release metadata missing")
	}
	metadata := rfc2317Revision(bundle)
	metadata.ParentApproval, metadata.ChildApproval = d.RFC2317.ParentApproval, d.RFC2317.ChildApproval
	if metadata.checksum() != d.RenderChecksum {
		return bundle, errors.New("RFC 2317 bundle does not match revision metadata")
	}
	return bundle, nil
}

func currentApprovalHead(heads map[string]uint32, zone string) *uint32 {
	if serial, exists := heads[zone]; exists {
		return &serial
	}
	return nil
}

func approvalHeadMatches(heads map[string]uint32, zone string, expected *uint32) bool {
	serial, exists := heads[zone]
	return (!exists && expected == nil) || (exists && expected != nil && serial == *expected)
}

func (p RFC2317Revision) approvalHeadsMatch(s *State) bool {
	return approvalHeadMatches(s.ProvisionApprovedSerials, p.ParentZone, p.ParentApproval) &&
		approvalHeadMatches(s.ProvisionApprovedSerials, p.ChildZone, p.ChildApproval)
}

func (p *RFC2317Revision) bindApprovalHeads(s *State) error {
	p.ParentApproval = currentApprovalHead(s.ProvisionApprovedSerials, p.ParentZone)
	p.ChildApproval = currentApprovalHead(s.ProvisionApprovedSerials, p.ChildZone)
	if p.ParentApproval != nil {
		order, err := provision.CompareSerial(p.PreviousParentSerial, *p.ParentApproval)
		if err != nil || order < 0 {
			return errors.New("parent snapshot predates or cannot be ordered against the approved parent")
		}
		if order == 0 {
			if sha := provisionApprovedZoneDigest(s, p.ParentZone, *p.ParentApproval); sha != "" && sha != p.ParentSnapshotSHA256 {
				return errors.New("parent snapshot bytes differ from the approved parent at the same SOA serial")
			}
		}
	}
	if p.ChildApproval != nil {
		if p.NewChildZone || p.PreviousChildSerial == nil {
			return errors.New("the child already has an approved release; review its current SOA")
		}
		order, err := provision.CompareSerial(*p.PreviousChildSerial, *p.ChildApproval)
		if err != nil || order < 0 {
			return errors.New("reviewed child SOA predates or cannot be ordered against the approved child")
		}
	}
	return nil
}

func provisionApprovedZoneDigest(s *State, zone string, serial uint32) string {
	for _, revision := range s.ProvisionRevisions {
		if revision.Status != "approved" {
			continue
		}
		if revision.Kind == "dns-arpa" && revision.Zone == zone && revision.Options.DNS.Serial == serial {
			return revision.Checksum
		}
		if p := revision.RFC2317; p != nil {
			if p.ParentZone == zone && p.ParentSerial == serial {
				return p.ParentSHA256
			}
			if p.ChildZone == zone && p.ChildSerial == serial {
				return p.ChildSHA256
			}
		}
	}
	return ""
}

func (a *App) importRFC2317Revision(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != http.MethodPost {
		problem(w, 405, "method not allowed")
		return
	}
	if session.API || !contains([]string{"admin", "operator"}, str(user, "role")) {
		problem(w, 403, "staff browser access required")
		return
	}
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		problem(w, 415, "Content-Type must be application/json")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, provision.MaxRFC2317MergeBytes))
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			problem(w, 413, "RFC 2317 merged bundle exceeds the upload limit")
		} else {
			problem(w, 400, "could not read RFC 2317 merged bundle")
		}
		return
	}
	bundle, err := provision.DecodeRFC2317Merged(raw)
	if err != nil {
		problem(w, 422, "invalid canonical RFC 2317 merged bundle; generate a complete parent and child pair with ixp-provision")
		return
	}
	metadata := rfc2317Revision(bundle)
	d := ProvisionRevision{ID: token(16), ProfileID: rfc2317ProfileID, ProfileName: "RFC 2317", ProfileRevision: token(16),
		Kind: provision.RFC2317MergeKind, Filename: rfc2317ReleaseFilename, Status: "draft", Checksum: digest(string(raw)),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ValidationLevel: "native", Complete: true, TargetCount: 2, RFC2317: &metadata}
	err = a.store.Update(func(s *State) error {
		actor, err := a.atlasActor(s, user, session, "admin", "operator")
		if err != nil || r.Context().Err() != nil {
			return errAtlasAuthentication
		}
		if len(s.ProvisionRevisions) >= 2000 {
			return errors.New("provisioning history quota reached; prune and remove older records")
		}
		if err := metadata.bindApprovalHeads(s); err != nil {
			return err
		}
		if err := reserveRFC2317Serials(s, d); err != nil {
			return err
		}
		d.RenderChecksum = metadata.checksum()
		file, err := a.writeEncryptedArtifactLocked("provisioning", d.ID, d.Checksum, string(raw))
		if err != nil {
			return errors.New("could not store the RFC 2317 artifacts")
		}
		d.ArtifactVersion, d.ArtifactSize, d.ArtifactChecksum = file.Version, file.Size, file.Checksum
		if err := validateProvisionRevision(d); err != nil {
			return err
		}
		s.ProvisionRevisions = append(s.ProvisionRevisions, d)
		auditDetails(s, str(actor, "id"), "import-rfc2317", "provisioning", d.ID, metadata.metadata())
		return nil
	})
	if errors.Is(err, errAtlasAuthentication) {
		problem(w, 401, "provisioning authority changed; sign in again")
		return
	}
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 201, a.provisionMetadata(d))
}
