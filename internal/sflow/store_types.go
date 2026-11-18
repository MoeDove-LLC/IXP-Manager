package sflow

import "time"

const (
	FiveMinuteRetention = 48 * time.Hour
	DailyRetention      = 30 * 24 * time.Hour
	MaxAggregateRows    = 100000
	MaxStoreBytes       = 128 << 20
)

type AttributedFlow struct {
	VLANID                    string `json:"vlanId,omitempty"`
	Family                    int    `json:"family"`
	SourceMemberID            string `json:"sourceMemberId,omitempty"`
	DestinationMemberID       string `json:"destinationMemberId,omitempty"`
	SourceMemberRevision      string `json:"sourceMemberRevision,omitempty"`
	DestinationMemberRevision string `json:"destinationMemberRevision,omitempty"`
	SourceConnectionID        string `json:"sourceConnectionId,omitempty"`
	DestinationConnectionID   string `json:"destinationConnectionId,omitempty"`
	EstimatedBytes            string `json:"estimatedBytes"`
	EstimatedPackets          string `json:"estimatedPackets"`
	SampleCount               uint64 `json:"sampleCount"`
	SampledTCP179Activity     bool   `json:"sampledTcp179Activity"`
	UnattributedReason        string `json:"unattributedReason,omitempty"`
}
type AttributedBatch struct {
	ID               string           `json:"id"`
	ReceivedAt       time.Time        `json:"receivedAt"`
	ExporterID       string           `json:"exporterId"`
	SwitchID         string           `json:"switchId"`
	InfrastructureID string           `json:"infrastructureId,omitempty"`
	MappingRevision  string           `json:"mappingRevision"`
	Sequence         uint32           `json:"sequence"`
	UptimeMS         uint32           `json:"uptimeMs,omitempty"`
	DatagramDigest   string           `json:"datagramDigest,omitempty"`
	Quality          Quality          `json:"quality"`
	Flows            []AttributedFlow `json:"flows"`
}
type Bucket struct {
	Start                     time.Time  `json:"start"`
	Resolution                string     `json:"resolution"`
	ExporterID                string     `json:"exporterId"`
	SwitchID                  string     `json:"switchId"`
	InfrastructureID          string     `json:"infrastructureId,omitempty"`
	VLANID                    string     `json:"vlanId,omitempty"`
	MappingRevision           string     `json:"mappingRevision"`
	Family                    int        `json:"family"`
	SourceMemberID            string     `json:"sourceMemberId,omitempty"`
	DestinationMemberID       string     `json:"destinationMemberId,omitempty"`
	SourceMemberRevision      string     `json:"sourceMemberRevision,omitempty"`
	DestinationMemberRevision string     `json:"destinationMemberRevision,omitempty"`
	SourceConnectionID        string     `json:"sourceConnectionId,omitempty"`
	DestinationConnectionID   string     `json:"destinationConnectionId,omitempty"`
	EstimatedBytes            string     `json:"estimatedBytes"`
	EstimatedPackets          string     `json:"estimatedPackets"`
	SampleCount               string     `json:"sampleCount"`
	TCP179Samples             string     `json:"tcp179Samples"`
	LastTCP179At              *time.Time `json:"lastTcp179At,omitempty"`
	UnattributedReason        string     `json:"unattributedReason,omitempty"`
	FirstReceivedAt           time.Time  `json:"firstReceivedAt"`
	LastReceivedAt            time.Time  `json:"lastReceivedAt"`
}
type Query struct {
	From           time.Time
	To             time.Time
	Resolution     string
	MemberID       string
	MemberRevision string
	ExporterID     string
	SwitchID       string
	SwitchIDs      []string
	VLANID         string
	Family         int
	Limit          int
}
type Classification struct {
	SequenceState    string `json:"sequenceState"`
	AcceptFlows      bool   `json:"acceptFlows"`
	MissingDatagrams uint32 `json:"missingDatagrams"`
}
type ExporterStatus struct {
	ExporterID           string    `json:"exporterId"`
	SwitchID             string    `json:"switchId"`
	Sequence             uint32    `json:"sequence"`
	UptimeMS             uint32    `json:"uptimeMs"`
	LastDatagramSequence uint32    `json:"lastDatagramSequence"`
	LastDatagramUptimeMS uint32    `json:"lastDatagramUptimeMs"`
	LastReceivedAt       time.Time `json:"lastReceivedAt"`
	SequenceState        string    `json:"sequenceState"`
	PendingRestart       bool      `json:"pendingRestart"`
}
type MemberIdentity struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type Result struct {
	SourceKind       string       `json:"sourceKind"`
	From             time.Time    `json:"from"`
	To               time.Time    `json:"to"`
	Items            []Bucket     `json:"items"`
	Truncated        bool         `json:"truncated"`
	Resolution       string       `json:"resolution"`
	RetentionSeconds int64        `json:"retentionSeconds"`
	OldestRetainedAt *time.Time   `json:"oldestRetainedAt"`
	NewestRetainedAt *time.Time   `json:"newestRetainedAt"`
	Quality          []QualityDay `json:"quality,omitempty"`
}
type QualityDay struct {
	Start              time.Time `json:"start"`
	ExporterID         string    `json:"exporterId"`
	SwitchID           string    `json:"switchId"`
	Datagrams          string    `json:"datagrams"`
	MissingDatagrams   string    `json:"missingDatagrams"`
	DuplicateDatagrams string    `json:"duplicateDatagrams"`
	UnorderedDatagrams string    `json:"unorderedDatagrams"`
	RestartSignals     string    `json:"restartSignals"`
	SampleSequenceGaps string    `json:"sampleSequenceGaps"`
	CounterSamples     string    `json:"counterSamples"`
}
type Usage struct {
	Rows                       int   `json:"rows"`
	Bytes                      int64 `json:"bytes"`
	MaxRows                    int   `json:"maxRows"`
	MaxBytes                   int64 `json:"maxBytes"`
	FiveMinuteRetentionSeconds int64 `json:"fiveMinuteRetentionSeconds"`
	DailyRetentionSeconds      int64 `json:"dailyRetentionSeconds"`
}
