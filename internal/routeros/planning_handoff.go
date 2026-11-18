package routeros

import (
	"encoding/json"
	"errors"

	"github.com/moedove/ixp-manager/internal/routing"
)

// PlanningHandoff carries the desired policy of an exact IXP Manager draft to
// the local planner. Device identity, credentials and consent come from the
// local supervisor, private inputs and operator.
type PlanningHandoff struct {
	Version          int     `json:"version"`
	RouterID         string  `json:"routerId"`
	DeploymentID     string  `json:"deploymentId"`
	ReviewIdentifier string  `json:"reviewIdentifier"`
	Desired          Desired `json:"desired"`
	IntentSHA256     string  `json:"intentSha256"`
}

func NewPlanningHandoff(router routing.Router, peers []routing.Peer, deploymentID, reviewIdentifier string) (PlanningHandoff, error) {
	desired, err := normalizeDesired(router, peers)
	if err != nil {
		return PlanningHandoff{}, err
	}
	handoff := PlanningHandoff{Version: 1, RouterID: router.ID, DeploymentID: deploymentID, ReviewIdentifier: reviewIdentifier, Desired: desired}
	handoff.IntentSHA256 = handoff.checksum()
	if err := handoff.Validate(); err != nil {
		return PlanningHandoff{}, err
	}
	return handoff, nil
}

func (handoff PlanningHandoff) checksum() string {
	// The digest covers the credential-free envelope with an empty digest slot.
	// It detects corruption or accidental edits during transfer.
	handoff.IntentSHA256 = ""
	return hash(handoff)
}

func (handoff PlanningHandoff) Validate() error {
	if handoff.Version != 1 || !identifier.MatchString(handoff.RouterID) || !identifier.MatchString(handoff.DeploymentID) ||
		!digestPattern.MatchString(handoff.ReviewIdentifier) || handoff.Desired.Peers == nil || handoff.IntentSHA256 != handoff.checksum() {
		return errors.New("RouterOS handoff metadata or intent checksum does not match")
	}
	return nil
}

func (handoff PlanningHandoff) Encode() ([]byte, error) {
	encoded, err := json.Marshal(handoff)
	if err != nil || len(encoded)+1 > MaxPlanBytes {
		return nil, errors.New("RouterOS handoff exceeds the JSON file limit")
	}
	return append(encoded, '\n'), nil
}
