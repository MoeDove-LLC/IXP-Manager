package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"
)

const LegacyAuditArchiveKind = "legacy-audit-metadata"
const MaxLegacyAuditArchiveRows = 10000

type LegacyAuditEntry struct {
	ID        string `json:"id"`
	UserID    string `json:"userId,omitempty"`
	Model     string `json:"model"`
	ModelID   string `json:"modelId,omitempty"`
	Action    string `json:"action"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type LegacyAuditArchive struct {
	Version        int                `json:"version"`
	Kind           string             `json:"kind"`
	Source         string             `json:"source"`
	SourceTimezone string             `json:"sourceTimezone"`
	Entries        []LegacyAuditEntry `json:"entries"`
	Checksum       string             `json:"checksum"`
}

var legacyAuditOrigin = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Observable records class basenames in the original log table. Keep that
// finite vocabulary so a custom field named model cannot carry arbitrary text.
func LegacyAuditModel(model string) string {
	switch model {
	case "AppPassword", "Cabinet", "CompanyBillingDetail", "CompanyRegisteredDetail", "ConsoleServer", "ConsoleServerConnection", "Contact", "CoreBundle", "CoreLink", "Customer", "CustomerEquipment", "CustomerNote", "CustomerToCustomerTag", "CustomerToUser", "DocstoreCustomerFile", "DocstoreFile", "Infrastructure", "IrrdbConfig", "Layer2Address", "Location", "Logo", "NetworkInfo", "P2pDailyStats", "PatchPanel", "PatchPanelPort", "PatchPanelPortFile", "PhysicalInterface", "RouteServerFilter", "RouteServerFilterProd", "Router", "SflowReceiver", "Switcher", "User", "Vendor", "VirtualInterface", "Vlan", "VlanInterface":
		return model
	default:
		return "Unknown"
	}
}

func LegacyAuditAction(action string) string {
	switch action {
	case "CREATED", "UPDATED", "DELETED":
		return action
	default:
		return "UNKNOWN"
	}
}

func legacyAuditNumber(value string, optional bool) (uint64, bool) {
	if value == "" && optional {
		return 0, true
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return n, err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}

func legacyAuditTime(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 36 {
		return false
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func legacyAuditChecksum(archive LegacyAuditArchive) string {
	archive.Checksum = ""
	data, _ := json.Marshal(archive)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ValidateLegacyAuditArchive(archive LegacyAuditArchive) error {
	if archive.Version != 1 || archive.Kind != LegacyAuditArchiveKind || !legacyAuditOrigin.MatchString(archive.Source) || len(archive.Entries) == 0 || len(archive.Entries) > MaxLegacyAuditArchiveRows {
		return errors.New("invalid legacy audit archive header or row count")
	}
	if err := ValidatePatchTimezone(archive.SourceTimezone); archive.SourceTimezone == "" || err != nil {
		return errors.New("invalid legacy audit source timezone")
	}
	var previous uint64
	for i, row := range archive.Entries {
		id, ok := legacyAuditNumber(row.ID, false)
		_, userOK := legacyAuditNumber(row.UserID, true)
		_, modelOK := legacyAuditNumber(row.ModelID, true)
		if !ok || !userOK || !modelOK || i > 0 && id >= previous || row.Model != LegacyAuditModel(row.Model) || row.Action != LegacyAuditAction(row.Action) || !legacyAuditTime(row.CreatedAt) || !legacyAuditTime(row.UpdatedAt) {
			return errors.New("legacy audit metadata contains an invalid or duplicate row")
		}
		previous = id
	}
	if archive.Checksum != legacyAuditChecksum(archive) {
		return errors.New("legacy audit archive checksum does not match")
	}
	return nil
}

func EncodeLegacyAuditArchive(archive LegacyAuditArchive) ([]byte, error) {
	archive.Checksum = legacyAuditChecksum(archive)
	if err := ValidateLegacyAuditArchive(archive); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(archive, "", "  ")
	return append(raw, '\n'), err
}
