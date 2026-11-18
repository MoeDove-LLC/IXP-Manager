// Package lookingglass runs bounded, typed read-only routing queries. It never
// accepts caller-provided CLI fragments and never uploads raw daemon output.
package lookingglass

import "time"

const (
	ViewAccepted   = "accepted"
	ViewFiltered   = "filtered"
	ViewPrePolicy  = "pre-policy"
	ViewExported   = "exported"
	ViewTable      = "table"
	MaxRows        = 200
	MaxOutputBytes = 4 << 20
)

type Query struct {
	RouterID          string `json:"routerId"`
	PeerID            string `json:"peerId,omitempty"`
	Platform          string `json:"platform"`
	NeighborAddress   string `json:"neighborAddress,omitempty"`
	NeighborASN       uint32 `json:"neighborAsn,omitempty"`
	ExpectedRouterID  string `json:"expectedRouterId,omitempty"`
	ExpectedRouterASN uint32 `json:"expectedRouterAsn,omitempty"`
	Family            int    `json:"family"`
	View              string `json:"view"`
	Prefix            string `json:"prefix,omitempty"`
	MaxRows           int    `json:"maxRows"`
	OwnershipRevision string `json:"ownershipRevision"`
}

type Lease struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Query     Query     `json:"query"`
}

type PathSegment struct {
	Type string   `json:"type"` // sequence, set, confed-sequence, confed-set
	ASNs []uint32 `json:"asns"`
}

type Route struct {
	Prefix                string        `json:"prefix"`
	NextHops              []string      `json:"nextHops"`
	ASPath                []PathSegment `json:"asPath"`
	Communities           [][2]uint32   `json:"communities"`
	LargeCommunities      [][3]uint32   `json:"largeCommunities"`
	Filtered              *bool         `json:"filtered,omitempty"` // nil means not known, never inferred from a community
	UnavailableAttributes []string      `json:"unavailableAttributes,omitempty"`
}

type Identity struct {
	Address   string `json:"address,omitempty"`
	ASN       uint32 `json:"asn,omitempty"`
	State     string `json:"state,omitempty"`
	RouterID  string `json:"routerId,omitempty"`
	RouterASN uint32 `json:"routerAsn,omitempty"`
}

type Result struct {
	Query      Query     `json:"query"`
	ObservedAt time.Time `json:"observedAt"`
	Status     string    `json:"status"` // ok, truncated, unsupported, error
	Complete   bool      `json:"complete"`
	Truncated  bool      `json:"truncated"`
	Identity   Identity  `json:"identity"`
	Routes     []Route   `json:"routes"`
	ErrorCode  string    `json:"errorCode,omitempty"`
}

type Capability struct {
	Platform string   `json:"platform"`
	Views    []string `json:"views"`
	Identity bool     `json:"identity"`
	Notes    string   `json:"notes"`
}

// Config is local administrator configuration, never part of a server lease.
// A mapped BIRD peer selects its protocol name; FRR/OpenBGPD mappings are IPs.
// Filtered retention requires native runtime evidence, never a zero-row guess.
// Table queries additionally require AllowTable and native identity proof.
type Config struct {
	RouterID   string            `json:"routerId"`
	Platform   string            `json:"platform"`
	Peers      map[string]string `json:"peers"`
	Birdc      string            `json:"birdc,omitempty"`
	BirdSocket string            `json:"birdSocket,omitempty"`
	Vtysh      string            `json:"vtysh,omitempty"`
	VtySocket  string            `json:"vtySocket,omitempty"`
	Bgpctl     string            `json:"bgpctl,omitempty"`
	BgpSocket  string            `json:"bgpSocket,omitempty"`
	AllowTable bool              `json:"allowTable,omitempty"`
}

type ClaimRequest struct {
	RouterID string `json:"routerId"`
}
type ClaimResponse struct {
	Lease *Lease `json:"lease"`
}
type Completion struct {
	LeaseID    string `json:"leaseId"`
	LeaseToken string `json:"leaseToken"`
	Result     Result `json:"result"`
}
