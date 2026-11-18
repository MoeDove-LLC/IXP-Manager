package migrate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const PatchBundleKind = "ixp-manager-patching"
const PatchManifestLimit int64 = 32 << 20
const PatchFileLimit int64 = 25 << 20
const PatchBundleLimit int64 = 1 << 30
const PatchEntryLimit = 8193
const PatchRecordLimit = 100000

// Facts are original scalar database values, not executable events, current
// grants, or values to be resolved by matching a customer/switch display name.
type PatchPanelSource struct {
	ID       string            `json:"id"`
	LegacyID int64             `json:"legacyId"`
	RackID   string            `json:"rackId,omitempty"`
	Name     string            `json:"name"`
	Prefix   string            `json:"prefix"`
	Facts    map[string]string `json:"facts"`
}
type PatchPortSource struct {
	ID             string            `json:"id"`
	LegacyID       int64             `json:"legacyId"`
	PanelID        string            `json:"panelId,omitempty"`
	MemberID       string            `json:"memberId,omitempty"`
	SwitchPortID   string            `json:"switchPortId,omitempty"`
	DuplexMasterID string            `json:"duplexMasterId,omitempty"`
	Number         int               `json:"number"`
	State          int               `json:"state"`
	Facts          map[string]string `json:"facts"`
}
type PatchHistorySource struct {
	ID             string            `json:"id"`
	LegacyID       int64             `json:"legacyId"`
	PortID         string            `json:"portId,omitempty"`
	DuplexMasterID string            `json:"duplexMasterId,omitempty"`
	Number         int               `json:"number"`
	State          int               `json:"state"`
	Facts          map[string]string `json:"facts"`
}
type PatchFileSource struct {
	ID         string            `json:"id"`
	LegacyID   int64             `json:"legacyId"`
	Kind       string            `json:"kind"` // current or history
	ParentID   string            `json:"parentId,omitempty"`
	Name       string            `json:"name"`
	Private    bool              `json:"private"`
	StorageKey string            `json:"storageKey"`
	Size       int64             `json:"size"`
	Checksum   string            `json:"checksum,omitempty"`
	Entry      string            `json:"entry"`
	Facts      map[string]string `json:"facts"`
}
type PatchingImport struct {
	Version        int                  `json:"version"`
	SourceTimezone string               `json:"sourceTimezone"`
	Panels         []PatchPanelSource   `json:"panels"`
	Ports          []PatchPortSource    `json:"ports"`
	Histories      []PatchHistorySource `json:"histories"`
	Files          []PatchFileSource    `json:"files"`
}

var patchHex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var patchFactKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var patchZone = regexp.MustCompile(`^[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)*$`)

func ValidatePatchTimezone(value string) error {
	if value == "" || value == "unknown" {
		return nil
	}
	if value == "Local" || len(value) > 100 || !patchZone.MatchString(value) {
		return errors.New("source timezone must be an explicit IANA name or unknown")
	}
	if _, err := time.LoadLocation(value); err != nil {
		return errors.New("invalid source timezone")
	}
	return nil
}
func patchFactID(facts map[string]string, key string) int64 {
	n, e := strconv.ParseInt(facts[key], 10, 64)
	if e != nil || n < 1 {
		return 0
	}
	return n
}
func patchMappedRef(facts map[string]string, key, kind, value string, unresolved bool) bool {
	n := patchFactID(facts, key)
	if n == 0 {
		return value == ""
	}
	return value == PatchID(kind, n) || (unresolved && value == "")
}
func patchMappedNumber(facts map[string]string, key string, value int) bool {
	n, e := strconv.ParseInt(facts[key], 10, 64)
	if e != nil {
		return value == 0
	}
	return n == int64(value)
}

func PatchID(kind string, id int64) string { return fmt.Sprintf("legacy-%s-%d", kind, id) }
func PatchFileID(kind string, id int64) string {
	return fmt.Sprintf("legacy-patch-%s-file-%d", kind, id)
}
func PatchFileEntry(kind string, id int64) string { return fmt.Sprintf("files/%s-%d", kind, id) }
func PatchStoragePath(key string) (string, error) {
	if !patchHex.MatchString(key) {
		return "", errors.New("legacy attachment storage key must be64 lowercase hexadecimal characters")
	}
	return "files/ppp/" + key[:1] + "/" + key[1:2] + "/" + key, nil
}
func patchSourceID(id int64) bool { return id > 0 && id <= 2147483647 }
func patchRef(id string) bool     { return id == "" || identifier.MatchString(id) }
func patchFacts(facts map[string]string) error {
	if len(facts) > 64 {
		return errors.New("legacy patch facts exceed field limit")
	}
	for key, value := range facts {
		if !patchFactKey.MatchString(key) || key == "loa_code" || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "token") || len(value) > 1<<20 || !utf8.ValidString(value) {
			return errors.New("invalid or forbidden legacy patch fact")
		}
	}
	return nil
}
func ValidatePatching(p *PatchingImport, contentRequired bool) error {
	if p == nil || p.Version != 1 || len(p.Panels)+len(p.Ports)+len(p.Histories)+len(p.Files) > PatchRecordLimit || len(p.Files)+1 > PatchEntryLimit || len(p.SourceTimezone) > 100 {
		return errors.New("invalid bounded patch import")
	}
	if err := ValidatePatchTimezone(p.SourceTimezone); err != nil {
		return err
	}
	seen := map[string]bool{}
	check := func(id, expected string, legacyID int64, facts map[string]string) error {
		if !patchSourceID(legacyID) || id != expected || seen[id] {
			return errors.New("invalid or duplicate legacy patch identity")
		}
		seen[id] = true
		if patchFactID(facts, "id") != legacyID {
			return errors.New("legacy identity and source facts disagree")
		}
		return patchFacts(facts)
	}
	for _, v := range p.Panels {
		if err := check(v.ID, PatchID("patch-panels", v.LegacyID), v.LegacyID, v.Facts); err != nil {
			return err
		}
		if !patchRef(v.RackID) || !patchMappedRef(v.Facts, "cabinet_id", "racks", v.RackID, true) || v.Name != v.Facts["name"] || v.Prefix != v.Facts["port_prefix"] || len(v.Name) > 1024 || len(v.Prefix) > 100 || !utf8.ValidString(v.Name+v.Prefix) {
			return errors.New("invalid legacy panel metadata")
		}
	}
	for _, v := range p.Ports {
		if err := check(v.ID, PatchID("patch-ports", v.LegacyID), v.LegacyID, v.Facts); err != nil {
			return err
		}
		if !patchRef(v.PanelID) || !patchRef(v.MemberID) || !patchRef(v.SwitchPortID) || !patchRef(v.DuplexMasterID) {
			return errors.New("invalid legacy patch reference")
		}
		if !patchMappedNumber(v.Facts, "state", v.State) || !patchMappedNumber(v.Facts, "number", v.Number) {
			return errors.New("legacy port state or number contradicts source facts")
		}
		if !patchMappedRef(v.Facts, "patch_panel_id", "patch-panels", v.PanelID, false) || !patchMappedRef(v.Facts, "customer_id", "members", v.MemberID, true) || !patchMappedRef(v.Facts, "switch_port_id", "ports", v.SwitchPortID, true) || !patchMappedRef(v.Facts, "duplex_master_id", "patch-ports", v.DuplexMasterID, false) {
			return errors.New("legacy current-port references disagree with original foreign keys")
		}
	}
	for _, v := range p.Histories {
		if err := check(v.ID, PatchID("patch-history", v.LegacyID), v.LegacyID, v.Facts); err != nil {
			return err
		}
		if !patchRef(v.PortID) || !patchRef(v.DuplexMasterID) {
			return errors.New("invalid legacy snapshot reference")
		}
		if !patchMappedNumber(v.Facts, "state", v.State) || !patchMappedNumber(v.Facts, "number", v.Number) {
			return errors.New("legacy snapshot state or number contradicts source facts")
		}
		if !patchMappedRef(v.Facts, "patch_panel_port_id", "patch-ports", v.PortID, false) || !patchMappedRef(v.Facts, "duplex_master_id", "patch-history", v.DuplexMasterID, false) {
			return errors.New("legacy snapshot references disagree with original foreign keys")
		}
	}
	var size int64
	for _, v := range p.Files {
		if v.Kind != "current" && v.Kind != "history" {
			return errors.New("invalid legacy attachment kind")
		}
		if err := check(v.ID, PatchFileID(v.Kind, v.LegacyID), v.LegacyID, v.Facts); err != nil {
			return err
		}
		if !patchRef(v.ParentID) || v.Entry != PatchFileEntry(v.Kind, v.LegacyID) || !utf8.ValidString(v.Name) || len(v.Name) > 4096 || v.Size < 0 || v.Size > PatchFileLimit {
			return errors.New("invalid bounded legacy attachment")
		}
		parent, kind := "patch_panel_port_id", "patch-ports"
		if v.Kind == "history" {
			parent, kind = "patch_panel_port_history_id", "patch-history"
		}
		declaredSize, err := strconv.ParseInt(v.Facts["size"], 10, 64)
		if err != nil || declaredSize != v.Size || v.Name != v.Facts["name"] || v.StorageKey != v.Facts["storage_location"] || !patchMappedRef(v.Facts, parent, kind, v.ParentID, false) {
			return errors.New("legacy attachment metadata disagrees with its source row")
		}
		originalPrivate := v.Facts["is_private"] != "0" && v.Facts["is_private"] != "false"
		if originalPrivate != v.Private {
			return errors.New("legacy attachment privacy must not be widened")
		}
		if _, err := PatchStoragePath(v.StorageKey); err != nil {
			return err
		}
		if contentRequired && !patchHex.MatchString(v.Checksum) {
			return errors.New("attachment content checksum is required")
		}
		if v.Checksum != "" && !patchHex.MatchString(v.Checksum) {
			return errors.New("invalid attachment content checksum")
		}
		if v.Size > PatchBundleLimit-size {
			return errors.New("patch attachment total exceeds bundle limit")
		}
		size += v.Size
	}
	return nil
}
