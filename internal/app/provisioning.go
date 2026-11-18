package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/provision"
)

type ProvisionRevision struct {
	RFC2317          *RFC2317Revision   `json:"rfc2317,omitempty"`
	ID               string             `json:"id"`
	ProfileID        string             `json:"profileId"`
	ProfileName      string             `json:"profileName"`
	ProfileRevision  string             `json:"profileRevision"`
	Options          provision.Options  `json:"options"`
	Kind             string             `json:"kind"`
	Filename         string             `json:"filename"`
	Zone             string             `json:"zone,omitempty"`
	Status           string             `json:"status"`
	Checksum         string             `json:"checksum"`
	RenderChecksum   string             `json:"renderChecksum"`
	CreatedAt        string             `json:"createdAt"`
	ApprovedBy       string             `json:"approvedBy,omitempty"`
	Validation       string             `json:"validation,omitempty"`
	ValidationLevel  string             `json:"validationLevel"`
	Complete         bool               `json:"complete"`
	TargetCount      int                `json:"targetCount"`
	Notices          []provision.Notice `json:"notices"`
	ArtifactVersion  int                `json:"artifactVersion"`
	ArtifactSize     int64              `json:"artifactSize"`
	ArtifactChecksum string             `json:"artifactChecksum"`
	Pruned           bool               `json:"pruned,omitempty"`
	PrunedAt         time.Time          `json:"prunedAt,omitempty"`
	PrunedBy         string             `json:"prunedBy,omitempty"`
}

func provisionLevel(kind string) string {
	if kind == "dns-arpa" || kind == "as112-dns" || kind == provision.RFC2317MergeKind {
		return "native"
	}
	return "structural"
}
func provisionNotice(kind string) string {
	if kind == provision.RFC2317MergeKind {
		return "完整父区与子区须一起通过 named-checkzone 并批准。安装前仍须在目标权威服务核对实际父区快照与子区 SOA。"
	}
	if kind == "as112-dns" {
		return "AS112 PowerDNS权威配置与全部区文件需原生检查；本机地址、UDP/TCP健康与持续失效撤回BGP必须另行验证，下载不等于服务或宣告已生效。"
	}
	if kind == "switch-core" {
		return "类型化交换机核心配置计划，仅完成结构校验；需要目标厂商适配与目标验证，下载不等于应用。"
	}
	if kind == "dns-arpa" {
		return "DNS 区文件必须通过 named-checkzone；下载不等于已在权威服务器加载。"
	}
	return "这是受限配置片段，仅完成结构检查；必须结合目标服务的完整配置验证模板、probe 和检查命令后再应用。"
}
func provisionArtifactDigest(artifact provision.Artifact) string {
	b, _ := json.Marshal(artifact)
	return digest(string(b))
}
func (d ProvisionRevision) fileMetadata() encryptedArtifactFile {
	return encryptedArtifactFile{Version: d.ArtifactVersion, Size: d.ArtifactSize, Checksum: d.ArtifactChecksum}
}
func (d ProvisionRevision) artifact(content string) provision.Artifact {
	return provision.Artifact{Kind: d.Kind, Content: content, Filename: d.Filename, Zone: d.Zone, ValidationLevel: d.ValidationLevel, Complete: d.Complete, Notices: d.Notices, TargetCount: d.TargetCount}
}
func (a *App) provisionReviewToken(d ProvisionRevision) string {
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("provisioning-review-v1\x00" + d.ID + "\x00" + d.Checksum + "\x00" + d.RenderChecksum))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *App) provisionMetadata(d ProvisionRevision) Record {
	view := Record{"id": d.ID, "profileId": d.ProfileID, "profileName": d.ProfileName, "profileRevision": d.ProfileRevision, "kind": d.Kind, "filename": d.Filename, "zone": d.Zone, "status": d.Status, "checksum": a.provisionReviewToken(d), "checksumKind": "hmac-review-token", "createdAt": d.CreatedAt, "approvedBy": d.ApprovedBy, "validation": d.Validation, "validationLevel": d.ValidationLevel, "validationNotice": provisionNotice(d.Kind), "complete": d.Complete, "targetCount": d.TargetCount, "notices": d.Notices, "pruned": d.Pruned, "prunedAt": optionalTime(d.PrunedAt), "artifactUnavailable": d.Pruned}
	if d.Kind == "dns-arpa" {
		view["dnsSerial"] = d.Options.DNS.Serial
	}
	if d.Kind == provision.RFC2317MergeKind && d.RFC2317 != nil {
		view["rfc2317"] = d.RFC2317.metadata()
	}
	if d.Kind == "switch-core" {
		view["switchId"] = d.Options.SwitchID
	}
	if d.Kind == "as112-dns" {
		view["routerId"] = d.Options.RouterID
		view["healthExecuted"] = false
	}
	return view
}

func validateProvisionRevision(d ProvisionRevision) error {
	if !identifier.MatchString(d.ID) || !identifier.MatchString(d.ProfileID) || !credentialRevision.MatchString(d.ProfileRevision) || !externalDigest(d.Checksum) || !externalDigest(d.RenderChecksum) {
		return errors.New("invalid provisioning revision identity")
	}
	paired := d.Kind == provision.RFC2317MergeKind
	if d.Kind != "dns-arpa" && d.Kind != "nagios" && d.Kind != "smokeping" && d.Kind != "switch-core" && d.Kind != "as112-dns" && !paired {
		return errors.New("invalid provisioning revision kind")
	}
	if d.Status != "draft" && d.Status != "validated" && d.Status != "approved" {
		return errors.New("invalid provisioning revision status")
	}
	if d.ValidationLevel != provisionLevel(d.Kind) || d.Complete != (d.Kind == "dns-arpa" || d.Kind == "as112-dns" || paired) {
		return errors.New("invalid provisioning capability metadata")
	}
	if paired {
		if d.RFC2317 == nil || !d.RFC2317.valid() || d.RFC2317.checksum() != d.RenderChecksum || d.Options.Kind != "" ||
			d.ProfileID != rfc2317ProfileID || d.Filename != rfc2317ReleaseFilename || d.TargetCount != 2 {
			return errors.New("invalid paired RFC 2317 revision metadata")
		}
	} else {
		if d.Options.Kind != d.Kind || d.RFC2317 != nil {
			return errors.New("invalid provisioning capability metadata")
		}
		if _, err := provision.NormalizeOptions(d.Options); err != nil {
			return errors.New("invalid provisioning option snapshot")
		}
	}
	if d.Kind == "dns-arpa" {
		zone, err := provision.ReverseZone(d.Options.DNS.Prefix)
		if err != nil || zone != d.Zone {
			return errors.New("DNS revision zone does not match its prefix")
		}
	} else if d.Zone != "" {
		return errors.New("non-DNS revision cannot declare a zone")
	}
	if d.Filename == "" || len(d.Filename) > 253 || filepath.Base(d.Filename) != d.Filename || strings.ContainsAny(d.Filename, "\r\n\x00\"\\") || d.Filename == "." || d.Filename == ".." {
		return errors.New("invalid provisioning filename")
	}
	if _, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err != nil {
		return errors.New("invalid provisioning timestamp")
	}
	if d.TargetCount < 0 || d.TargetCount > 100000 || len(d.Notices) > 101 || len(d.Validation) > 2048 {
		return errors.New("provisioning metadata limit exceeded")
	}
	if d.ArtifactVersion != 1 || d.ArtifactSize < 1 || d.ArtifactSize > encryptedArtifactCipherLimit || !externalDigest(d.ArtifactChecksum) {
		return errors.New("invalid provisioning encrypted metadata")
	}
	if d.Pruned && (d.PrunedAt.IsZero() || !identifier.MatchString(d.PrunedBy)) {
		return errors.New("invalid provisioning retention metadata")
	}
	return nil
}

func (a *App) readProvisionRevisionLocked(d ProvisionRevision) (string, error) {
	if err := validateProvisionRevision(d); err != nil {
		return "", err
	}
	if d.Pruned {
		return "", errors.New("provisioning artifact has been pruned")
	}
	return a.readEncryptedArtifactLocked("provisioning", d.ID, d.Checksum, d.fileMetadata())
}
func (a *App) readProvisionRevision(d ProvisionRevision) (string, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	return a.readProvisionRevisionLocked(d)
}

// ValidateProvisioningArtifacts is used by the offline backup workflow while it
// owns the Store process lock. It never renders data, runs a tool or contacts a service.
func ValidateProvisioningArtifacts(store *Store) error {
	return (&App{store: store}).initProvisioningArtifacts()
}
func (a *App) initProvisioningArtifacts() error {
	if err := a.initEncryptedArtifactDir("provisioning"); err != nil {
		return err
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if a.store.closed {
		return errors.New("data store is closed")
	}
	s := &a.store.state
	if len(s.ProvisionRevisions) > 2000 {
		return errors.New("provisioning history quota exceeded")
	}
	if err := validateProvisionSerials(s); err != nil {
		return err
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, d := range s.ProvisionRevisions {
		if seen[d.ID] {
			return errors.New("duplicate provisioning revision")
		}
		seen[d.ID] = true
		ids = append(ids, d.ID)
		if err := validateProvisionRevision(d); err != nil {
			return err
		}

		if d.Pruned {
			if _, err := a.removeEncryptedArtifactLocked("provisioning", d.ID, d.Checksum, d.fileMetadata()); err != nil {
				return err
			}
		} else {
			content, err := a.readProvisionRevisionLocked(d)
			if err != nil {
				return err
			}
			if d.Kind == provision.RFC2317MergeKind {
				if _, err := rfc2317PairContent(d, content); err != nil {
					return errors.New("stored RFC 2317 pair does not match its revision")
				}
			}
		}
	}

	return a.cleanupEncryptedArtifactOrphansLocked("provisioning", ids)
}

func (a *App) provisionCurrent(s *State, d ProvisionRevision) error {
	if d.Kind == provision.RFC2317MergeKind {
		if d.RFC2317 == nil || (d.Status != "approved" && !d.RFC2317.approvalHeadsMatch(s)) {
			return errors.New("parent or child approval has changed; merge against the current pair")
		}
		return nil
	}
	profile := find(s, provisionProfileKind, d.ProfileID)
	if profile == nil || str(profile, "revision") != d.ProfileRevision {
		return errors.New("configuration profile changed; generate and review a new revision")
	}
	artifact, err := a.renderProvisioning(s, d.ProfileID, d.Options.DNS.Serial)
	if err != nil || digest(artifact.Content) != d.Checksum || provisionArtifactDigest(artifact) != d.RenderChecksum {
		return errors.New("inventory or generation rules changed; generate and review a new revision")
	}
	return nil
}

func provisionTokenAllows(session Session, scope, profileID string) bool {
	return !session.API || (contains(session.Scopes, scope) && contains(session.ProfileIDs, profileID))
}

func (a *App) provisionCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	binary := a.namedCheckzone
	if binary == "" {
		binary = "named-checkzone"
	}
	_, err := exec.LookPath(binary)
	items := []Record{}
	for _, kind := range []string{"dns-arpa", "nagios", "smokeping", "switch-core", "as112-dns", provision.RFC2317MergeKind} {
		label := map[string]string{"dns-arpa": "DNS 反向区文件", "nagios": "Nagios 监测片段", "smokeping": "Smokeping 目标片段", "switch-core": "交换机核心网络计划", "as112-dns": "AS112 权威DNS服务包", provision.RFC2317MergeKind: "RFC 2317 父子区发布"}[kind]
		available := (kind == "dns-arpa" || kind == provision.RFC2317MergeKind) && err == nil
		if kind == "as112-dns" {
			pdns := a.pdnsServer
			if pdns == "" {
				pdns = "pdns_server"
			}
			_, pdnsErr := exec.LookPath(pdns)
			available = err == nil && pdnsErr == nil
		}
		items = append(items, Record{"kind": kind, "label": label, "validationLevel": provisionLevel(kind), "nativeCheckerAvailable": available, "complete": kind == "dns-arpa" || kind == "as112-dns" || kind == provision.RFC2317MergeKind, "validationNotice": provisionNotice(kind)})
	}
	respond(w, 200, Record{"items": items})
}

func (a *App) provisioning(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	role := str(user, "role")
	if role == "member" {
		problem(w, 403, "staff access required")
		return
	}
	if r.URL.Path == "/api/provisioning/capabilities" {
		a.provisionCapabilities(w, r)
		return
	}
	if r.URL.Path == "/api/provisioning/rfc2317-import" {
		a.importRFC2317Revision(w, r, user, session)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/provisioning"), "/")
	s := a.store.Snapshot()
	if len(parts) == 1 && parts[0] == "" {
		if r.Method == "GET" {
			items := []Record{}
			for i := len(s.ProvisionRevisions) - 1; i >= 0; i-- {
				items = append(items, a.provisionMetadata(s.ProvisionRevisions[i]))
			}
			respond(w, 200, Record{"items": items})
			return
		}
		if r.Method != "POST" || (role != "admin" && role != "operator") {
			problem(w, 403, "staff write access required")
			return
		}
		var body struct {
			ProfileID string `json:"profileId"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !provisionTokenAllows(session, "provisioning:write", body.ProfileID) {
			problem(w, 403, "token is not bound to this configuration profile")
			return
		}
		var d ProvisionRevision
		err := a.store.Update(func(s *State) error {
			if _, err := a.atlasActor(s, user, session, "admin", "operator"); err != nil {
				return errors.New("provisioning authority changed; sign in again")
			}
			if len(s.ProvisionRevisions) >= 2000 {
				return errors.New("provisioning history quota reached; prune and remove older records")
			}
			profile := find(s, provisionProfileKind, body.ProfileID)
			if profile == nil {
				return errors.New("configuration profile not found")
			}
			options, err := parseProvisionOptions(profile["options"])
			if err != nil {
				return err
			}
			serial, err := allocateProvisionSerial(s, profile, options)
			if err != nil {
				return err
			}
			if options.Kind == "dns-arpa" {
				options.DNS.Serial = serial
			}
			artifact, err := a.renderProvisioning(s, body.ProfileID, serial)
			if err != nil {
				return err
			}
			notices := artifact.Notices
			if len(notices) > 100 {
				notices = append(append([]provision.Notice(nil), notices[:100]...), provision.Notice{Severity: "warning", Code: "more-notices", Message: fmt.Sprintf("另外 %d 项提示未逐条展示，请核对目标数量和生成内容。", len(artifact.Notices)-100)})
			}
			d = ProvisionRevision{ID: token(16), ProfileID: body.ProfileID, ProfileName: str(profile, "name"), ProfileRevision: str(profile, "revision"), Options: options, Kind: artifact.Kind, Filename: artifact.Filename, Zone: artifact.Zone, Status: "draft", Checksum: digest(artifact.Content), RenderChecksum: provisionArtifactDigest(artifact), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), ValidationLevel: provisionLevel(artifact.Kind), Complete: artifact.Complete, Notices: notices, TargetCount: artifact.TargetCount}
			file, err := a.writeEncryptedArtifactLocked("provisioning", d.ID, d.Checksum, artifact.Content)
			if err != nil {
				return err
			}
			d.ArtifactVersion, d.ArtifactSize, d.ArtifactChecksum = file.Version, file.Size, file.Checksum
			if err := validateProvisionRevision(d); err != nil {
				return err
			}
			s.ProvisionRevisions = append(s.ProvisionRevisions, d)
			audit(s, str(user, "id"), "preview", "provisioning", d.ID)
			return nil
		})
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		respond(w, 201, a.provisionMetadata(d))
		return
	}
	if len(parts) < 2 || len(parts) > 3 {
		problem(w, 404, "provisioning revision not found")
		return
	}
	var d ProvisionRevision
	found := false
	for _, candidate := range s.ProvisionRevisions {
		if candidate.ID == parts[1] {
			d = candidate
			found = true
			break
		}
	}
	if !found {
		problem(w, 404, "provisioning revision not found")
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		view := a.provisionMetadata(d)
		if d.Pruned {
			respond(w, 410, view)
			return
		}
		content, err := a.readProvisionRevision(d)
		if err != nil {
			problem(w, 500, "configuration artifact failed its integrity check")
			return
		}
		if d.Kind == provision.RFC2317MergeKind {
			bundle, err := rfc2317PairContent(d, content)
			if err != nil {
				problem(w, 500, "RFC 2317 artifact does not match its revision")
				return
			}
			changes, err := provision.RFC2317ParentChanges(bundle)
			if err != nil {
				problem(w, 500, "RFC 2317 parent change preview unavailable")
				return
			}
			_, child, _ := strings.Cut(bundle.Files[0].Content, "\n")
			content = "; Child zone\n" + child + "\n; Parent delegation and aliases\n" + changes
		}
		view["config"], view["redacted"] = content, true
		respond(w, 200, view)
		return
	}
	if len(parts) == 3 && parts[2] == "diff" && r.Method == "GET" {
		if d.Kind == provision.RFC2317MergeKind {
			problem(w, 405, "RFC 2317 planned record changes are shown in its paired preview")
			return
		}
		a.provisionDiff(w, r, &s, d)
		return
	}
	if len(parts) == 3 && parts[2] == "config" && r.Method == "GET" {
		if (role != "admin" && role != "operator") || !provisionTokenAllows(session, "provisioning:pull", d.ProfileID) || (d.Kind == provision.RFC2317MergeKind && session.API) {
			problem(w, 403, "profile-bound provisioning pull access required")
			return
		}
		if d.Pruned {
			problem(w, 410, "configuration artifact has been pruned")
			return
		}
		if d.Status != "approved" {
			problem(w, 409, "approve the exact reviewed revision before downloading")
			return
		}
		if !provisionSerialApproved(&s, d) {
			problem(w, 409, "DNS serial was superseded; regenerate previous content with a newer serial to roll back")
			return
		}
		current, content, status, err := a.currentProvisionDownload(r, user, session, d)
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		d = current
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, d.Filename))
		w.Header().Set("X-Config-SHA256", d.Checksum)
		w.Header().Set("X-Provisioning-Revision", d.ID)
		w.Header().Set("X-Provisioning-Profile-ID", d.ProfileID)
		w.Header().Set("X-Provisioning-Kind", d.Kind)
		if d.Kind == "switch-core" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Provisioning-Switch-ID", d.Options.SwitchID)
		}
		if d.Kind == "as112-dns" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Provisioning-Router-ID", d.Options.RouterID)
		}
		if d.Kind == provision.RFC2317MergeKind {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		_, _ = w.Write([]byte(content))
		return
	}
	if len(parts) == 2 && r.Method == "DELETE" {
		a.provisionRetention(w, r, user, session, d, true)
		return
	}
	if len(parts) != 3 || r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	action := parts[2]
	if action == "prune" {
		a.provisionRetention(w, r, user, session, d, false)
		return
	}
	if action != "validate" && action != "approve" {
		problem(w, 404, "unknown provisioning action")
		return
	}
	if (role != "admin" && role != "operator") || !provisionTokenAllows(session, "provisioning:write", d.ProfileID) || (d.Kind == provision.RFC2317MergeKind && session.API) {
		problem(w, 403, "profile-bound provisioning write access required")
		return
	}
	if action == "approve" && (role != "admin" || session.API) {
		problem(w, 403, "browser administrator review required")
		return
	}
	if d.Pruned {
		problem(w, 410, "configuration artifact unavailable")
		return
	}
	var validation provision.Validation
	if action == "approve" {
		var body struct {
			Checksum string `json:"checksum"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Checksum != a.provisionReviewToken(d) {
			problem(w, 409, "review identifier does not match this revision")
			return
		}
	}
	if action == "validate" {
		if d.Status != "draft" {
			problem(w, 409, "only drafts can be validated")
			return
		}
		if err := a.provisionCurrent(&s, d); err != nil {
			problem(w, 409, err.Error())
			return
		}
		content, err := a.readProvisionRevision(d)
		if err != nil {
			problem(w, 500, "configuration artifact failed its integrity check")
			return
		}
		if d.Kind == provision.RFC2317MergeKind {
			bundle, decodeErr := rfc2317PairContent(d, content)
			if decodeErr != nil {
				problem(w, 500, "RFC 2317 artifact does not match its revision")
				return
			}
			receipt, nativeErr := provision.ValidateRFC2317Merge(r.Context(), bundle, provision.ValidatorOptions{NamedCheckzone: a.namedCheckzone})
			err = nativeErr
			if err == nil && receipt.NativeRecordDeltaVerified && receipt.ParentSnapshotVerified && receipt.ParentCandidate.Level == "native" && receipt.Child.Level == "native" {
				validation = provision.Validation{Level: "native", Message: "named-checkzone verified both complete zones and the reviewed parent RRset delta"}
			}
		} else {
			validation, err = provision.Validate(r.Context(), d.artifact(content), provision.ValidatorOptions{NamedCheckzone: a.namedCheckzone, PDNSServer: a.pdnsServer})
		}
		if err != nil {
			problem(w, 422, "configuration validation failed; check the configured validator and profile options")
			return
		}
		if validation.Level != provisionLevel(d.Kind) {
			problem(w, 422, "validator did not provide the required validation level")
			return
		}
	}
	err := a.store.Update(func(s *State) error {
		actor, actorErr := a.atlasActor(s, user, session, "admin", "operator")
		if actorErr != nil {
			return errors.New("provisioning authority changed; sign in again")
		}
		actorRole := str(actor, "role")
		if action == "approve" && actorRole != "admin" {
			return errors.New("administrator required")
		}
		for i := range s.ProvisionRevisions {
			current := &s.ProvisionRevisions[i]
			if current.ID != d.ID {
				continue
			}
			if current.Pruned || current.Checksum != d.Checksum || current.RenderChecksum != d.RenderChecksum {
				return errors.New("revision changed")
			}
			if err := a.provisionCurrent(s, *current); err != nil {
				return err
			}
			if _, err := a.readProvisionRevisionLocked(*current); err != nil {
				return errors.New("artifact integrity check failed")
			}
			if action == "validate" {
				if current.Status != "draft" {
					return errors.New("revision status changed")
				}
				current.Status = "validated"
				current.Validation = validation.Message
			} else {
				if current.Status != "validated" {
					return errors.New("required preflight validation has not passed")
				}
				if len(provisionRevisionSerials(*current)) != 0 {
					if err := approveProvisionSerial(s, *current); err != nil {
						return err
					}
				}
				current.Status = "approved"
				current.ApprovedBy = str(user, "id")
			}
			d = *current
			audit(s, str(user, "id"), action, "provisioning", d.ID)
			return nil
		}
		return errors.New("revision not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, a.provisionMetadata(d))
}

func (a *App) provisionDiff(w http.ResponseWriter, r *http.Request, s *State, d ProvisionRevision) {
	if d.Pruned {
		respond(w, 410, a.provisionMetadata(d))
		return
	}
	var base ProvisionRevision
	for _, candidate := range s.ProvisionRevisions {
		if candidate.ProfileID == d.ProfileID && candidate.Kind == d.Kind && candidate.Status == "approved" && candidate.CreatedAt < d.CreatedAt && candidate.CreatedAt > base.CreatedAt {
			base = candidate
		}
	}
	after, err := a.readProvisionRevision(d)
	if err != nil {
		problem(w, 500, "configuration diff unavailable")
		return
	}
	before := ""
	if base.ID != "" && !base.Pruned {
		before, err = a.readProvisionRevision(base)
		if err != nil {
			problem(w, 500, "configuration diff unavailable")
			return
		}
	}
	diff := configDiff(base.ID, before, after)
	diff.Redacted = true
	diff.BaseUnavailable = base.Pruned
	respond(w, 200, diff)
}

func (a *App) provisionPrunable(s *State, d ProvisionRevision) bool {
	if d.Kind == provision.RFC2317MergeKind {
		if d.Pruned || d.Status != "approved" {
			return true
		}
		for _, version := range provisionRevisionSerials(d) {
			if head, exists := s.ProvisionApprovedSerials[version.zone]; exists && head == version.serial {
				return false
			}
		}
		return true
	}
	profile := find(s, provisionProfileKind, d.ProfileID)
	if d.Pruned || d.Status != "approved" || profile == nil {
		return true
	}
	options, err := parseProvisionOptions(profile["options"])
	if err != nil {
		return false
	}
	if options.Kind != d.Kind {
		return true
	}
	if d.Kind == "dns-arpa" {
		zone, err := provision.ReverseZone(options.DNS.Prefix)
		if err != nil {
			return false
		}
		if zone != d.Zone {
			return true
		}
		return validateProvisionSerials(s) == nil && !provisionSerialApproved(s, d)
	}
	approved := map[string]ProvisionRevision{}
	latest := ""
	var newest time.Time
	for _, other := range s.ProvisionRevisions {
		if other.ProfileID == d.ProfileID && other.Status == "approved" && !other.Pruned {
			approved[other.ID] = other
			created, _ := time.Parse(time.RFC3339Nano, other.CreatedAt)
			if created.After(newest) {
				newest = created
				latest = other.ID
			}
		}
	}
	for i := len(s.Audit) - 1; i >= 0; i-- {
		event := s.Audit[i]
		if event.Resource == "provisioning" && event.Action == "approve" {
			if _, ok := approved[event.ResourceID]; ok {
				latest = event.ResourceID
				break
			}
		}
	}
	if latest == d.ID {
		return false
	}
	if a.provisionCurrent(s, d) == nil {
		for _, other := range approved {
			if other.ID != d.ID && other.Checksum == d.Checksum && other.RenderChecksum == d.RenderChecksum && other.ProfileRevision == d.ProfileRevision {
				return true
			}
		}
		return false
	}
	return true
}
func (a *App) provisionRetention(w http.ResponseWriter, r *http.Request, user Record, session Session, d ProvisionRevision, remove bool) {
	if str(user, "role") != "admin" || session.API {
		problem(w, 403, "browser administrator required for configuration retention")
		return
	}
	var body struct {
		Checksum    string `json:"checksum"`
		Acknowledge bool   `json:"acknowledge"`
		Reason      string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if !body.Acknowledge || body.Checksum != a.provisionReviewToken(d) || len(body.Reason) > 1000 || strings.ContainsRune(body.Reason, 0) || (remove && body.Reason == "") {
		problem(w, 400, "current review identifier, acknowledgement and deletion reason are required")
		return
	}
	err := a.store.Update(func(s *State) error {
		if str(find(s, "users", str(user, "id")), "role") != "admin" {
			return errors.New("permission changed")
		}
		for i := range s.ProvisionRevisions {
			current := &s.ProvisionRevisions[i]
			if current.ID != d.ID {
				continue
			}
			if current.Checksum != d.Checksum || !a.provisionPrunable(s, *current) {
				return errors.New("current approved configuration must be retained")
			}
			details := a.provisionMetadata(*current)
			details["reason"] = body.Reason
			if remove {
				if !current.Pruned {
					return errors.New("prune the artifact before deleting metadata")
				}
				if _, err := a.removeEncryptedArtifactLocked("provisioning", current.ID, current.Checksum, current.fileMetadata()); err != nil {
					return err
				}
				auditDetails(s, str(user, "id"), "delete-provisioning-metadata", "provisioning", current.ID, details)
				d = *current
				s.ProvisionRevisions = append(s.ProvisionRevisions[:i], s.ProvisionRevisions[i+1:]...)
				return nil
			}
			if !current.Pruned {
				if _, err := a.readProvisionRevisionLocked(*current); err != nil {
					return err
				}
				current.Pruned = true
				current.PrunedAt = time.Now().UTC()
				current.PrunedBy = str(user, "id")
				auditDetails(s, str(user, "id"), "prune-provisioning", "provisioning", current.ID, details)
			}
			d = *current
			return nil
		}
		return errors.New("revision not found")
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	if !remove {
		a.store.mu.Lock()
		_, err = a.removeEncryptedArtifactLocked("provisioning", d.ID, d.Checksum, d.fileMetadata())
		a.store.mu.Unlock()
		if err != nil {
			problem(w, 500, "pruning recorded; file removal will resume after storage recovery")
			return
		}
		respond(w, 200, a.provisionMetadata(d))
	} else {
		respond(w, 200, Record{"ok": true, "auditPreserved": true})
	}
}
