package app

import (
	"encoding/json"
	"time"

	"github.com/moedove/ixp-manager/internal/atlas"
)

type AtlasState struct {
	InstanceID     string               `json:"instanceId,omitempty"`
	ProbeSnapshots []AtlasProbeSnapshot `json:"probeSnapshots,omitempty"`
	Runs           []AtlasRun           `json:"runs,omitempty"`
	Artifacts      []AtlasArtifact      `json:"artifacts,omitempty"`
	CreditBuckets  []AtlasCreditBucket  `json:"creditBuckets,omitempty"`
}
type AtlasCreditBucket struct {
	Hour     int64 `json:"hour"`
	Reserved int   `json:"reserved"`
}
type AtlasArtifact struct {
	ID        string                `json:"id"`
	Kind      string                `json:"kind"`
	Checksum  string                `json:"checksum"`
	File      encryptedArtifactFile `json:"file"`
	CreatedAt time.Time             `json:"createdAt"`
}
type AtlasProbeSnapshot struct {
	ID         string    `json:"id"`
	MemberID   string    `json:"memberId"`
	AF         int       `json:"af"`
	ASN        uint32    `json:"asn"`
	AnchorOnly bool      `json:"anchorOnly"`
	FetchedAt  time.Time `json:"fetchedAt"`
	Count      int       `json:"count"`
	Truncated  bool      `json:"truncated"`
	Checksum   string    `json:"checksum"`
}
type AtlasParticipant struct {
	MemberID      string  `json:"memberId"`
	ProbeIDs      []int64 `json:"probeIds"`
	TargetProbeID int64   `json:"targetProbeId"`
}
type AtlasProposal struct {
	Name            string             `json:"name"`
	Mode            string             `json:"mode,omitempty"`
	VLANID          string             `json:"vlanId,omitempty"`
	Participants    []AtlasParticipant `json:"participants,omitempty"`
	SourceMemberID  string             `json:"sourceMemberId,omitempty"`
	TargetMemberID  string             `json:"targetMemberId,omitempty"`
	TargetType      string             `json:"targetType,omitempty"`
	TargetID        string             `json:"targetId,omitempty"`
	Type            string             `json:"type"`
	AF              int                `json:"af"`
	ProbeIDs        []int64            `json:"probeIds,omitempty"`
	IsOneoff        bool               `json:"isOneoff"`
	IntervalSeconds int                `json:"intervalSeconds,omitempty"`
	DurationSeconds int                `json:"durationSeconds,omitempty"`
	StartAt         time.Time          `json:"startAt,omitempty"`
	IsPublic        bool               `json:"isPublic"`
}
type AtlasMeasurement struct {
	ID                 string    `json:"id"`
	SourceMemberID     string    `json:"sourceMemberId"`
	TargetMemberID     string    `json:"targetMemberId"`
	TargetAddress      string    `json:"targetAddress"`
	ProbeIDs           []int64   `json:"probeIds"`
	Status             string    `json:"status"`
	IntentID           string    `json:"intentId,omitempty"`
	Description        string    `json:"description,omitempty"`
	ProviderID         int64     `json:"providerId,omitempty"`
	OwnershipConfirmed bool      `json:"ownershipConfirmed"`
	ProviderStatus     string    `json:"providerStatus,omitempty"`
	ScheduledStart     time.Time `json:"scheduledStart,omitempty"`
	ScheduledStop      time.Time `json:"scheduledStop,omitempty"`
	AttemptedAt        time.Time `json:"attemptedAt,omitempty"`
	StopAttemptedAt    time.Time `json:"stopAttemptedAt,omitempty"`
	LastSyncedAt       time.Time `json:"lastSyncedAt,omitempty"`
	NextSyncAt         time.Time `json:"nextSyncAt,omitempty"`
	LastError          string    `json:"lastError,omitempty"`
	ReceiptArtifactID  string    `json:"receiptArtifactId,omitempty"`
	StatusArtifactID   string    `json:"statusArtifactId,omitempty"`
	ResultsArtifactID  string    `json:"resultsArtifactId,omitempty"`
	EstimatedCredits   int       `json:"estimatedCredits"`
	EstimatedSamples   int       `json:"estimatedSamples"`
	ResultCount        int       `json:"resultCount"`
}
type AtlasRun struct {
	ID                    string             `json:"id"`
	Spec                  AtlasProposal      `json:"spec"`
	Status                string             `json:"status"`
	Checksum              string             `json:"checksum"`
	BaseChecksum          string             `json:"baseChecksum"`
	ProviderOrigin        string             `json:"providerOrigin"`
	Items                 []AtlasMeasurement `json:"items"`
	EstimatedCredits      int                `json:"estimatedCredits"`
	EstimatedSamples      int                `json:"estimatedSamples"`
	CreatedAt             time.Time          `json:"createdAt"`
	CreatedBy             string             `json:"createdBy"`
	ApprovedAt            time.Time          `json:"approvedAt,omitempty"`
	ApprovedBy            string             `json:"approvedBy,omitempty"`
	ExecutionAuthorizedAt time.Time          `json:"executionAuthorizedAt,omitempty"`
	ResumeRequired        bool               `json:"resumeRequired,omitempty"`
	StopRequested         bool               `json:"stopRequested"`
	Pruned                bool               `json:"pruned"`
	PrunedAt              time.Time          `json:"prunedAt,omitempty"`
}
type atlasProbeFile struct {
	Probes []atlas.Probe   `json:"probes"`
	Raw    json.RawMessage `json:"raw"`
}
type atlasResultFile struct {
	Results []atlas.Result  `json:"results"`
	Raw     json.RawMessage `json:"raw"`
}
type atlasReceiptFile struct {
	RunID         string          `json:"runId"`
	MeasurementID string          `json:"measurementId"`
	ProviderID    int64           `json:"providerId"`
	IntentID      string          `json:"intentId"`
	Spec          atlas.Spec      `json:"spec"`
	Response      json.RawMessage `json:"response"`
}
type atlasStatusFile struct {
	Observation atlas.Measurement `json:"observation"`
	Raw         json.RawMessage   `json:"raw"`
}

func atlasRequiresKey(state State) bool { return len(state.Atlas.Artifacts) > 0 }
func AtlasArtifactIDs(state State) []string {
	ids := make([]string, 0, len(state.Atlas.Artifacts))
	for _, a := range state.Atlas.Artifacts {
		ids = append(ids, a.ID)
	}
	return ids
}
