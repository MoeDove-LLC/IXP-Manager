package migrate

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const DocstoreBundleKind = "ixp-manager-docstore"
const DocstoreManifestLimit int64 = 32 << 20
const DocstoreFileLimit int64 = 25 << 20
const DocstoreBundleLimit int64 = 1 << 30
const DocstoreCompressedLimit int64 = DocstoreBundleLimit + (8 << 20)
const DocstoreEntryLimit = 8193
const DocstoreRecordLimit = 100000

// A DocstoreBundle contains immutable source material, not current accounts,
// authorization tokens, successful-download events, or historical file bytes.
type DocstoreBundle struct {
	Version  int            `json:"version"`
	Kind     string         `json:"kind"`
	Source   string         `json:"source"`
	Warnings []string       `json:"warnings"`
	Docstore DocstoreImport `json:"docstore"`
}
type DocstoreImport struct {
	Version        int                       `json:"version"`
	SourceTimezone string                    `json:"sourceTimezone"`
	Directories    []DocstoreDirectorySource `json:"directories"`
	Files          []DocstoreFileSource      `json:"files"`
	Logs           []DocstoreLogSource       `json:"logs"`
}
type DocstoreDirectorySource struct {
	ID          string            `json:"id"`
	LegacyID    int64             `json:"legacyId"`
	Kind        string            `json:"kind"`
	ParentID    string            `json:"parentId,omitempty"`
	MemberID    string            `json:"memberId,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Facts       map[string]string `json:"facts"`
}
type DocstoreFileSource struct {
	ID            string            `json:"id"`
	LegacyID      int64             `json:"legacyId"`
	Kind          string            `json:"kind"`
	DirectoryID   string            `json:"directoryId,omitempty"`
	MemberID      string            `json:"memberId,omitempty"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	MinPrivileges int               `json:"minPrivileges"`
	StorageKey    string            `json:"storageKey"`
	Size          int64             `json:"size"`
	Checksum      string            `json:"checksum,omitempty"`
	Entry         string            `json:"entry"`
	Facts         map[string]string `json:"facts"`
}

// The legacy table combines authenticated views and download attempts, records
// before file I/O, and contains no outcome. DownloadedBy is only an old user ID.
type DocstoreLogSource struct {
	ID           string            `json:"id"`
	LegacyID     int64             `json:"legacyId"`
	FileID       string            `json:"fileId"`
	DownloadedBy *int64            `json:"downloadedBy,omitempty"`
	CreatedAt    string            `json:"createdAt"`
	UpdatedAt    string            `json:"updatedAt"`
	Facts        map[string]string `json:"facts"`
}

func DocstoreID(kind, entity string, id int64) string {
	return fmt.Sprintf("legacy-docstore-%s-%s-%d", kind, entity, id)
}
func DocstoreFileEntry(kind string, id int64) string { return fmt.Sprintf("files/%s-%d", kind, id) }

// StorageKey is the old disk-relative path, not the display name or a digest.
func ValidateDocstoreStorageKey(value string) error {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.Contains(value, "\\") {
		return errors.New("legacy document path must be a canonical relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." || component == "." || component == "" {
			return errors.New("unsafe legacy document path component")
		}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("legacy document path contains control characters")
		}
	}
	return nil
}

func docstoreKind(kind string) bool { return kind == "shared" || kind == "customer" }
func docstoreFacts(facts map[string]string, allowed string) error {
	if err := patchFacts(facts); err != nil {
		return err
	}
	keys := " " + allowed + " "
	for k := range facts {
		if !strings.Contains(keys, " "+k+" ") {
			return fmt.Errorf("unsupported legacy docstore fact %q", k)
		}
	}
	return nil
}
func docstoreRef(facts map[string]string, key, kind, entity, actual string) bool {
	v := facts[key]
	if v == "" || v == "0" {
		return actual == ""
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return err == nil && n > 0 && actual == DocstoreID(kind, entity, n)
}
func docstoreMember(kind, actual string, facts map[string]string) bool {
	if kind == "shared" {
		return actual == "" && facts["cust_id"] == ""
	}
	if actual == "" {
		return true
	} // unresolved source customer remains staff review
	n, err := strconv.ParseInt(facts["cust_id"], 10, 64)
	return err == nil && patchSourceID(n) && actual == PatchID("members", n)
}
func docstoreText(name, description string, facts map[string]string) bool {
	return name == facts["name"] && description == facts["description"] && len(name) <= 4096 && len(description) <= 1<<20 && utf8.ValidString(name+description)
}

// Unknown or missing privilege values must never receive the public zero value.
func DocstorePrivilege(raw string) int {
	n, e := strconv.Atoi(raw)
	if e != nil || n < 0 || n > 3 {
		return -1
	}
	return n
}

func ValidateDocstore(p *DocstoreImport, contentRequired bool) error {
	if p == nil || p.Version != 1 || len(p.Directories)+len(p.Files)+len(p.Logs) > DocstoreRecordLimit || len(p.Files)+1 > DocstoreEntryLimit {
		return errors.New("invalid bounded docstore import")
	}
	if err := ValidatePatchTimezone(p.SourceTimezone); err != nil {
		return err
	}
	seen := map[string]bool{}
	check := func(id, expected string, legacyID int64, facts map[string]string, allowed string) error {
		if legacyID <= 0 || id != expected || seen[id] || patchFactID(facts, "id") != legacyID {
			return errors.New("invalid or duplicate legacy docstore identity")
		}
		seen[id] = true
		return docstoreFacts(facts, allowed)
	}
	dirs := map[string]DocstoreDirectorySource{}
	for _, d := range p.Directories {
		if !docstoreKind(d.Kind) {
			return errors.New("invalid legacy document directory kind")
		}
		if err := check(d.ID, DocstoreID(d.Kind, "directory", d.LegacyID), d.LegacyID, d.Facts, "id cust_id parent_dir_id name description created_at updated_at"); err != nil {
			return err
		}
		if !docstoreText(d.Name, d.Description, d.Facts) || !docstoreMember(d.Kind, d.MemberID, d.Facts) || !docstoreRef(d.Facts, "parent_dir_id", d.Kind, "directory", d.ParentID) {
			return errors.New("legacy directory metadata contradicts source row")
		}
		dirs[d.ID] = d
	}
	// Iterative color traversal is linear and does not recurse on hostile depth.
	colors := map[string]byte{}
	for id := range dirs {
		trail := []string{}
		for id != "" && colors[id] == 0 {
			d := dirs[id]
			colors[id] = 1
			trail = append(trail, id)
			if d.ParentID != "" {
				parent, ok := dirs[d.ParentID]
				if !ok || parent.Kind != d.Kind || (d.Kind == "customer" && parent.Facts["cust_id"] != d.Facts["cust_id"]) {
					return errors.New("legacy directory has missing or cross-customer parent")
				}
			}
			id = d.ParentID
		}
		if id != "" && colors[id] == 1 {
			return errors.New("legacy directory tree contains a cycle")
		}
		for _, id := range trail {
			colors[id] = 2
		}
	}
	var total int64
	for _, f := range p.Files {
		if !docstoreKind(f.Kind) {
			return errors.New("invalid legacy document kind")
		}
		if err := check(f.ID, DocstoreID(f.Kind, "file", f.LegacyID), f.LegacyID, f.Facts, "id cust_id docstore_directory_id docstore_customer_directory_id name description disk path sha256 min_privs file_last_updated created_by created_at updated_at"); err != nil {
			return err
		}
		key, other := "docstore_directory_id", "docstore_customer_directory_id"
		if f.Kind == "customer" {
			key, other = other, key
		}
		if !docstoreText(f.Name, f.Description, f.Facts) || !docstoreMember(f.Kind, f.MemberID, f.Facts) || !docstoreRef(f.Facts, key, f.Kind, "directory", f.DirectoryID) || f.Facts[other] != "" || f.MinPrivileges != DocstorePrivilege(f.Facts["min_privs"]) || f.StorageKey != f.Facts["path"] {
			return errors.New("legacy file metadata or privilege contradicts source row")
		}
		if f.DirectoryID != "" {
			d, ok := dirs[f.DirectoryID]
			if !ok || d.Kind != f.Kind || (f.Kind == "customer" && d.Facts["cust_id"] != f.Facts["cust_id"]) {
				return errors.New("legacy file has missing or cross-customer directory")
			}
		}
		if f.Facts["disk"] == "" || len(f.Facts["disk"]) > 128 {
			return errors.New("explicit legacy document disk is required")
		}
		if err := ValidateDocstoreStorageKey(f.StorageKey); err != nil {
			return err
		}
		if f.Entry != DocstoreFileEntry(f.Kind, f.LegacyID) || f.Size < 0 || f.Size > DocstoreFileLimit || f.Size > DocstoreBundleLimit-total {
			return errors.New("invalid bounded legacy file content")
		}
		total += f.Size
		if (contentRequired || f.Checksum != "") && !patchHex.MatchString(f.Checksum) {
			return errors.New("legacy document content checksum is required")
		}
		original := strings.ToLower(f.Facts["sha256"])
		if original != "" && (!patchHex.MatchString(original) || (f.Checksum != "" && original != f.Checksum)) {
			return errors.New("legacy document source checksum mismatch")
		}
	}
	for _, l := range p.Logs {
		if err := check(l.ID, DocstoreID("shared", "log", l.LegacyID), l.LegacyID, l.Facts, "id docstore_file_id downloaded_by created_at updated_at"); err != nil {
			return err
		}
		if !docstoreRef(l.Facts, "docstore_file_id", "shared", "file", l.FileID) || l.CreatedAt != l.Facts["created_at"] || l.UpdatedAt != l.Facts["updated_at"] {
			return errors.New("legacy access record contradicts source row")
		}
		actor := l.Facts["downloaded_by"]
		if actor == "" {
			if l.DownloadedBy != nil {
				return errors.New("legacy access actor must remain unknown")
			}
		} else {
			n, e := strconv.ParseInt(actor, 10, 64)
			if e != nil || !patchSourceID(n) || l.DownloadedBy == nil || *l.DownloadedBy != n {
				return errors.New("invalid legacy access actor")
			}
		}
	}
	return nil
}

func validateDocstoreBundle(b *DocstoreBundle, content bool) error {
	if b == nil || b.Version != 1 || b.Kind != DocstoreBundleKind || b.Source != "ixp-manager-legacy" || len(b.Warnings) > DocstoreRecordLimit {
		return errors.New("invalid docstore bundle manifest")
	}
	for _, warning := range b.Warnings {
		if len(warning) > 4096 || !utf8.ValidString(warning) {
			return errors.New("invalid docstore warning")
		}
	}
	return ValidateDocstore(&b.Docstore, content)
}
