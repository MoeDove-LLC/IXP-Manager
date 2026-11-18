package traffic

import "time"

const (
	LegacyTableJSON           = "ixp-table-json"
	LegacyRRDJSON             = "rrd-xport-json"
	LegacyRRDXML              = "rrd-xport-xml"
	LegacyMaxInputBytes       = 16 << 20
	LegacyMaxBatchBytes       = 64 << 20
	LegacyMaxRows             = 20000
	LegacyMaxBatches          = 2000
	LegacyQuotaBytes    int64 = 512 << 20
)

// LegacyInput contains an explicit source declaration and an offline UTF-8
// export. Content is never fetched as a URL or executed as a template/command.
type LegacyInput struct {
	Format  string                 `json:"format"`
	Source  LegacySource           `json:"source"`
	Content string                 `json:"content"`
	Columns []LegacyColumn         `json:"columns,omitempty"`
	Windows []LegacyDeclaredWindow `json:"windows,omitempty"`
	Owners  []LegacyOwnerEvidence  `json:"owners,omitempty"`
}
type LegacySource struct {
	Name       string `json:"name"`
	Timezone   string `json:"timezone"`             // required IANA timezone, including explicit UTC
	Backend    string `json:"backend"`              // mrtg, rrd, sflow-rrd
	Table      string `json:"table,omitempty"`      // traffic_daily, traffic_daily_phys_ints, p2p_daily_stats
	Unit       string `json:"unit,omitempty"`       // required for RRD export: bits, bytes, packets, events
	EntityKind string `json:"entityKind,omitempty"` // required for RRD export: customer, physical-interface, dataset
	EntityID   string `json:"entityId,omitempty"`   // legacy identifier, never current inventory attribution
}
type LegacyColumn struct {
	Index     int    `json:"index"`     // explicit zero-based export column
	Legend    string `json:"legend"`    // must equal the export legend
	Direction string `json:"direction"` // in, out
	Statistic string `json:"statistic"` // average, maximum, minimum, last
}
type LegacyDeclaredWindow struct {
	RecordID string    `json:"recordId"`
	Period   string    `json:"period"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Evidence string    `json:"evidence"`
}

// Evidence is an explicit historical assertion, not a present-day resource map.
// A row remains staff-only unless a complete known window is covered by it.
type LegacyOwnerEvidence struct {
	EntityKind      string    `json:"entityKind"`
	EntityID        string    `json:"entityId"`
	MemberID        string    `json:"memberId"`
	MemberRevision  string    `json:"memberRevision"`
	ValidFrom       time.Time `json:"validFrom"`
	ValidTo         time.Time `json:"validTo"`
	ReferenceSHA256 string    `json:"referenceSha256"`
	Description     string    `json:"description"`
}
type LegacyWindow struct {
	Kind            string     `json:"kind"`   // rolling, calendar-day, rate-bucket
	Period          string     `json:"period"` // day/week/month/year, calendar-day, or step
	NominalSeconds  int64      `json:"nominalSeconds,omitempty"`
	Start           *time.Time `json:"start"`
	End             *time.Time `json:"end"`
	BoundaryQuality string     `json:"boundaryQuality"` // unknown, calendar-label, export, declared
	Evidence        string     `json:"evidence,omitempty"`
}
type LegacyMeasure struct {
	Average       *string           `json:"average"`
	Maximum       *string           `json:"maximum"`
	Minimum       *string           `json:"minimum"`
	Last          *string           `json:"last"`
	Total         *string           `json:"total"`
	MaxAt         *time.Time        `json:"maxAt"`
	OriginalMaxAt string            `json:"originalMaxAt,omitempty"`
	Unknown       map[string]string `json:"unknown,omitempty"` // missing/null/nan/ambiguous-local-time
}
type LegacyRow struct {
	ID             string               `json:"id"`
	RecordID       string               `json:"recordId"`
	SourceKind     string               `json:"sourceKind"` // legacy-mrtg-window, legacy-rrd-window, legacy-p2p-daily, legacy-rrd-rate
	EntityKind     string               `json:"entityKind"`
	EntityID       string               `json:"entityId"`
	PeerEntityID   string               `json:"peerEntityId,omitempty"`
	OriginalDay    string               `json:"originalDay,omitempty"`
	Category       string               `json:"category"`
	Family         int                  `json:"family,omitempty"`
	TotalUnit      string               `json:"totalUnit"`
	RateUnit       string               `json:"rateUnit"`
	Window         LegacyWindow         `json:"window"`
	In             LegacyMeasure        `json:"in"`
	Out            LegacyMeasure        `json:"out"`
	MaxSemantics   string               `json:"maxSemantics"`
	StaffOnly      bool                 `json:"staffOnly"`
	MemberID       string               `json:"memberId,omitempty"`
	MemberRevision string               `json:"memberRevision,omitempty"`
	OwnerEvidence  *LegacyOwnerEvidence `json:"ownerEvidence,omitempty"`
	Notices        []string             `json:"notices"`
}
type LegacyBatch struct {
	Version       int            `json:"version"`
	ID            string         `json:"id"`
	Checksum      string         `json:"checksum"` // SHA256 of canonical source/input-checksum/rows/notices, excluding ID/checksum
	InputChecksum string         `json:"inputChecksum"`
	Format        string         `json:"format"`
	Source        LegacySource   `json:"source"`
	Columns       []LegacyColumn `json:"columns,omitempty"`
	Rows          []LegacyRow    `json:"rows"`
	Notices       []string       `json:"notices"`
}
type LegacyMetadata struct {
	ID              string       `json:"id"`
	Checksum        string       `json:"checksum"`
	InputChecksum   string       `json:"inputChecksum"`
	Format          string       `json:"format"`
	Source          LegacySource `json:"source"`
	RowCount        int          `json:"rowCount"`
	StaffOnlyRows   int          `json:"staffOnlyRows"`
	OriginalDayFrom string       `json:"originalDayFrom,omitempty"`
	OriginalDayTo   string       `json:"originalDayTo,omitempty"`
	ImportedAt      time.Time    `json:"importedAt"`
	Size            int64        `json:"size"`
}
type LegacyQuery struct {
	BatchID         string     `json:"batchId"` // required: rows are never implicitly combined across imports
	SourceKind      string     `json:"sourceKind,omitempty"`
	EntityKind      string     `json:"entityKind,omitempty"`
	EntityID        string     `json:"entityId,omitempty"`
	OriginalDayFrom string     `json:"originalDayFrom,omitempty"`
	OriginalDayTo   string     `json:"originalDayTo,omitempty"`
	WindowFrom      *time.Time `json:"windowFrom,omitempty"`
	WindowTo        *time.Time `json:"windowTo,omitempty"`
	Period          string     `json:"period,omitempty"`
	Category        string     `json:"category,omitempty"`
	Family          int        `json:"family,omitempty"`
	MemberID        string     `json:"memberId,omitempty"`
	MemberRevision  string     `json:"memberRevision,omitempty"`
	Offset          int        `json:"offset"`
	Limit           int        `json:"limit"`
}
type LegacyResult struct {
	SourceKind    string      `json:"sourceKind"` // legacy-report
	BatchID       string      `json:"batchId"`
	Rows          []LegacyRow `json:"rows"`
	TotalRows     int         `json:"totalRows"`
	Offset        int         `json:"offset"`
	Limit         int         `json:"limit"`
	Truncated     bool        `json:"truncated"`
	NoAggregation bool        `json:"noAggregation"`
	Notices       []string    `json:"notices"`
}
type LegacyCatalog struct {
	Batches    []LegacyMetadata `json:"batches"`
	Bytes      int64            `json:"bytes"`
	MaxBytes   int64            `json:"maxBytes"`
	MaxBatches int              `json:"maxBatches"`
}

// Planned entry points implemented alongside these contract types:
// ParseLegacy(LegacyInput) (LegacyBatch,error)
// OpenLegacy(dir string) (*LegacyStore,error)
// (*LegacyStore).Put(batch LegacyBatch, expectedChecksum string) (LegacyMetadata,error)
// (*LegacyStore).List() (LegacyCatalog,error)
// (*LegacyStore).Get(id string) (LegacyBatch,error)
// (*LegacyStore).Query(LegacyQuery) (LegacyResult,error)
// (*LegacyStore).Delete(id, expectedChecksum string) error
// (*LegacyStore).Close() error
// LegacyListFiles(dir string) ([]string,error); LegacyValidateFiles(dir string) error
