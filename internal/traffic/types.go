// Package traffic defines exact counter-derived statistics. It does not ingest
// sampled sFlow estimates or infer VLAN/address-family splits from port counters.
package traffic

import "time"

const (
	Octets          = "octets"
	Unicast         = "unicast"
	Multicast       = "multicast"
	Broadcast       = "broadcast"
	Errors          = "errors"
	Discards        = "discards"
	FiveMinutes     = "5m"
	Hour            = "1h"
	Day             = "1d"
	MaxQueryBuckets = 5000
)

// CounterPair directions may be omitted independently. Empty is unavailable,
// whereas "0" is an observed counter value. Bits defaults to64 when omitted.
type CounterPair struct {
	In      string `json:"in,omitempty"`
	Out     string `json:"out,omitempty"`
	Bits    int    `json:"bits,omitempty"`
	InBits  int    `json:"inBits,omitempty"`
	OutBits int    `json:"outBits,omitempty"`
}

// Width returns the effective width of one independently observed direction.
func (p CounterPair) Width(in bool) int {
	v := p.OutBits
	if in {
		v = p.InBits
	}
	if v == 0 {
		v = p.Bits
	}
	if v == 0 {
		v = 64
	}
	return v
}

// Dimensions are frozen by the control plane at ingestion, never looked up from
// today's inventory when reading history. Port/member IDs live on PortSample.
type Dimensions struct {
	MemberRevision   string    `json:"memberRevision,omitempty"`
	ConnectionIDs    []string  `json:"connectionIds,omitempty"`
	SwitchID         string    `json:"switchId,omitempty"`
	RackID           string    `json:"rackId,omitempty"`
	LocationID       string    `json:"locationId,omitempty"`
	InfrastructureID string    `json:"infrastructureId,omitempty"`
	CoreBundleID     string    `json:"coreBundleId,omitempty"`
	CoreLinkID       string    `json:"coreLinkId,omitempty"`
	CoreSide         string    `json:"coreSide,omitempty"`
	OwnershipSince   time.Time `json:"ownershipSince,omitempty"`
}

type Key struct {
	PortID            string     `json:"portId"`
	MemberID          string     `json:"memberId,omitempty"`
	SourceID          string     `json:"sourceId"`
	OwnershipRevision string     `json:"ownershipRevision"`
	CounterEpoch      string     `json:"counterEpoch"`
	Dimensions        Dimensions `json:"dimensions"`
	Metric            string     `json:"metric"`
}

type Query struct {
	Scope             string    `json:"scope"` // port/member/switch/rack/location/infrastructure/core-bundle
	ID                string    `json:"id"`
	MemberID          string    `json:"memberId,omitempty"` // optional ACL restriction, applied before aggregation
	MemberRevision    string    `json:"memberRevision,omitempty"`
	OwnershipRevision string    `json:"ownershipRevision,omitempty"`
	CoreSide          string    `json:"coreSide,omitempty"`
	ExpectedPortIDs   []string  `json:"expectedPortIds,omitempty"` // trusted inventory projection, not inferred from observed subset
	Metric            string    `json:"metric"`
	Resolution        string    `json:"resolution"`
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
}

type Direction struct {
	Total              *string    `json:"total"`           // exact decimal units over observed valid intervals; nil means no coverage
	LowerBoundTotal    *string    `json:"lowerBoundTotal"` // conditional modulo-counter lower bound, including any exact observed portion; never add to Total
	AmbiguousIntervals uint64     `json:"ambiguousIntervals"`
	AverageRate        *float64   `json:"averageRate"`
	MaxRate            *float64   `json:"maxRate"`
	MaxAt              *time.Time `json:"maxAt,omitempty"`
	CoveredMillis      uint64     `json:"coveredMillis"`
	RateCoveredMillis  uint64     `json:"rateCoveredMillis"`
	ExpectedMillis     uint64     `json:"expectedMillis"`
	Coverage           float64    `json:"coverage"`
	ValidIntervals     uint64     `json:"validIntervals"`
	ResetIntervals     uint64     `json:"resetIntervals"`
	WrapIntervals      uint64     `json:"wrapIntervals"`
	GapIntervals       uint64     `json:"gapIntervals"`
}

type Bucket struct {
	Start                 time.Time `json:"start"`
	End                   time.Time `json:"end"`
	In                    Direction `json:"in"`
	Out                   Direction `json:"out"`
	ContributingPorts     int       `json:"contributingPorts"`
	ExpectedPorts         int       `json:"expectedPorts"`
	UniformAllocation     bool      `json:"uniformAllocation"`
	PeakMethod            string    `json:"peakMethod"` // observed-interval or aligned-bucket-average; never instantaneous
	PeakResolutionSeconds int       `json:"peakResolutionSeconds"`
	Partial               bool      `json:"partial"`
}

type Result struct {
	SourceKind       string     `json:"sourceKind"` // counter
	Scope            string     `json:"scope"`
	ID               string     `json:"id"`
	CoreSide         string     `json:"coreSide,omitempty"`
	Metric           string     `json:"metric"`
	Resolution       string     `json:"resolution"`
	TotalUnit        string     `json:"totalUnit"` // bytes/packets/events
	RateUnit         string     `json:"rateUnit"`  // bits/s, packets/s or events/s
	From             time.Time  `json:"from"`
	To               time.Time  `json:"to"`
	Buckets          []Bucket   `json:"buckets"`
	Summary          Bucket     `json:"summary"`
	RetainedFrom     *time.Time `json:"retainedFrom"`
	LatestAt         *time.Time `json:"latestAt"`
	ArchiveStartedAt *time.Time `json:"archiveStartedAt"`
	RetentionLimited bool       `json:"retentionLimited"`
	Notices          []string   `json:"notices"`
}

// Target directory entries retain sampled dimensions; labels from today's
// inventory must not be substituted for deleted/reassigned historical objects.
type TargetQuery struct {
	Scope          string `json:"scope,omitempty"`
	MemberID       string `json:"memberId,omitempty"`
	MemberRevision string `json:"memberRevision,omitempty"`
}
type Target struct {
	Scope          string    `json:"scope"`
	ID             string    `json:"id"`
	MemberID       string    `json:"memberId,omitempty"`
	MemberRevision string    `json:"memberRevision,omitempty"`
	CoreSide       string    `json:"coreSide,omitempty"`
	FirstSeen      time.Time `json:"firstSeen"`
	LastSeen       time.Time `json:"lastSeen"`
	PortIDs        []string  `json:"portIds"`
	SourceIDs      []string  `json:"sourceIds"`
	StaffOnly      bool      `json:"staffOnly"`
}
type Catalog struct {
	Targets   []Target `json:"targets"`
	Truncated bool     `json:"truncated"`
}

// Legacy report rows are a separate future import source. Their windows/units
// and attribution evidence must be supplied, never converted into counter deltas.
type LegacyReport struct {
	SourceKind    string     `json:"sourceKind"` // legacy-mrtg-window/legacy-rrd-rate/legacy-p2p-daily
	Unit          string     `json:"unit"`
	WindowStart   *time.Time `json:"windowStart,omitempty"`
	WindowEnd     *time.Time `json:"windowEnd,omitempty"`
	OriginalDay   string     `json:"originalDay,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	MemberID      string     `json:"memberId,omitempty"`
	OwnerEvidence string     `json:"ownerEvidence,omitempty"`
	StaffOnly     bool       `json:"staffOnly"`
	InTotal       *string    `json:"inTotal,omitempty"`
	OutTotal      *string    `json:"outTotal,omitempty"`
}
