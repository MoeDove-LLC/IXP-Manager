// Package routeros builds constrained desired-state plans. It does not open a
// device connection and never promotes a plan or an action estimate to native
// validation, Safe Mode ownership or permission to activate routing.
package routeros

import "github.com/moedove/ixp-manager/internal/routing"

const (
	SchemaVersion        = 1
	DefaultActionLimit   = 80
	MaximumActionLimit   = 80
	MaxPlanBytes         = 16 << 20
	MaxPrepareOperations = 60016
)

type Target struct {
	RouterID string `json:"routerId"` // inventory ID, not BGP router-id
	Version  string `json:"version"`  // exact stable release, not a minimum/range
	DeviceID string `json:"deviceId"` // opaque comparison/hash data, never a path/command
	// Digest of the trusted local transport configuration: literal endpoints,
	// ports, usernames, API TLS SPKI pin AND SSH host-key pin. Not either pin alone.
	TransportIdentitySHA256 string `json:"transportIdentitySha256"`
}
type Authentication struct {
	Enabled  bool   `json:"enabled"`
	Revision string `json:"revision,omitempty"`
}
type Peer struct {
	ID              string                 `json:"id"`
	ASN             uint32                 `json:"asn"`
	Address         string                 `json:"address"`
	MaxPrefixes     int                    `json:"maxPrefixes"`
	AllowedPrefixes []string               `json:"allowedPrefixes"`
	AllowedRoutes   []routing.PrefixOrigin `json:"allowedRoutes,omitempty"`
	Authentication  Authentication         `json:"authentication"`
}
type Desired struct {
	RouterID  string `json:"routerId"`
	ASN       uint32 `json:"asn"`
	LocalIPv4 string `json:"localIpv4,omitempty"`
	LocalIPv6 string `json:"localIpv6,omitempty"`
	Peers     []Peer `json:"peers"` // enabled peers only; omitted old peers are disabled
}
type ObjectRef struct {
	ID      string `json:"id,omitempty"` // verified RouterOS internal *HEX, never display index
	Name    string `json:"name,omitempty"`
	Chain   string `json:"chain,omitempty"`
	Comment string `json:"comment,omitempty"`
	// StateChecksum covers all observed controlled, non-secret attributes in a
	// canonical representation, not comments or a desired manifest alone.
	StateChecksum string `json:"stateChecksum,omitempty"`
	// Exact observed filter body hash. Required for filter roots/generation rules.
	RuleSHA256 string `json:"ruleSha256,omitempty"`
	Disabled   bool   `json:"disabled"`
	Invalid    bool   `json:"invalid"`
}
type Connection struct {
	PeerID         string         `json:"peerId"`
	Name           string         `json:"name"`
	Instance       string         `json:"instance"`
	Family         string         `json:"family"` // ip or ipv6
	LocalAddress   string         `json:"localAddress"`
	RemoteAddress  string         `json:"remoteAddress"`
	RemoteASN      uint32         `json:"remoteAsn"`
	LocalASN       uint32         `json:"localAsn"`
	MaxPrefixes    int            `json:"maxPrefixes"`
	InputChain     string         `json:"inputChain"`
	OutputChain    string         `json:"outputChain"`
	Authentication Authentication `json:"authentication"`
}
type ConnectionState struct {
	Ref                    ObjectRef  `json:"ref"`
	Config                 Connection `json:"config"`
	Disabled               bool       `json:"disabled"`
	AuthenticationVerified bool       `json:"authenticationVerified"`
}
type GenerationState struct {
	Generation     string      `json:"generation"`
	PolicyChecksum string      `json:"policyChecksum"`
	Rules          []ObjectRef `json:"rules"`
}
type Snapshot struct {
	// A mutable /system/identity name is not cross-channel device evidence.
	CrossChannelIdentityVerified bool              `json:"crossChannelIdentityVerified"`
	Target                       Target            `json:"target"`
	Owner                        string            `json:"owner"`
	Managed                      bool              `json:"managed"`
	NamespaceEmpty               bool              `json:"namespaceEmpty"`
	ForeignReferences            bool              `json:"foreignReferences"`
	UnexpectedObjects            bool              `json:"unexpectedObjects"`
	Table                        ObjectRef         `json:"table"`
	TableFIB                     bool              `json:"tableFib"`
	Instance                     ObjectRef         `json:"instance"`
	ASN                          uint32            `json:"asn"`
	RouterID                     string            `json:"routerId"`
	InputEntry                   ObjectRef         `json:"inputEntry"`
	OutputEntry                  ObjectRef         `json:"outputEntry"`
	ActiveGeneration             string            `json:"activeGeneration,omitempty"`
	Connections                  []ConnectionState `json:"connections"`
	Generations                  []GenerationState `json:"generations"`
}
type PlanOptions struct {
	Bootstrap           bool `json:"bootstrap"`
	AllowIdentityChange bool `json:"allowIdentityChange"`
	ActionLimit         int  `json:"actionLimit"`
}
type Phase string

const (
	Prepare         Phase = "prepare"
	Activate        Phase = "activate"
	Cleanup         Phase = "cleanup"
	DiscardPrepared Phase = "discard-prepared"
)

type OperationKind string

const (
	AddTable               OperationKind = "add-table"
	AddInstance            OperationKind = "add-instance"
	AddRule                OperationKind = "add-rule"
	AddDisabledConnection  OperationKind = "add-disabled-connection"
	SetEntry               OperationKind = "set-entry"
	SetInstance            OperationKind = "set-instance"
	SetConnection          OperationKind = "set-connection"
	SetAuthentication      OperationKind = "set-authentication"
	UnsetUnusedFamilyLimit OperationKind = "unset-unused-family-limit"
	EnableConnection       OperationKind = "enable-connection"
	DisableConnection      OperationKind = "disable-connection"
	RemoveRule             OperationKind = "remove-rule"
	RemoveConnection       OperationKind = "remove-connection"
)

type RuleKind string

const (
	ImportRule    RuleKind = "import"
	ExportRule    RuleKind = "export"
	RecipientRule RuleKind = "recipient"
	RejectRule    RuleKind = "reject"
	JumpRule      RuleKind = "jump"
)

type Rule struct {
	Kind       RuleKind `json:"kind"`
	Chain      string   `json:"chain"`
	Comment    string   `json:"comment"`
	PeerID     string   `json:"peerId,omitempty"`
	Prefix     string   `json:"prefix,omitempty"`
	OriginASN  uint32   `json:"originAsn,omitempty"`
	JumpTarget string   `json:"jumpTarget,omitempty"`
}
type Operation struct {
	ID               string        `json:"id"`
	Phase            Phase         `json:"phase"`
	Kind             OperationKind `json:"kind"`
	Object           ObjectRef     `json:"object"`
	Rule             *Rule         `json:"rule,omitempty"`
	Connection       *Connection   `json:"connection,omitempty"`
	PeerID           string        `json:"peerId,omitempty"`
	EstimatedActions int           `json:"estimatedActions"`
}
type Budget struct {
	EstimatedActions int    `json:"estimatedActions"`
	Limit            int    `json:"limit"`
	Verified         bool   `json:"verified"`
	Basis            string `json:"basis"`
}
type Plan struct {
	Version                  int         `json:"version"`
	Target                   Target      `json:"target"`
	Owner                    string      `json:"owner"`
	Generation               string      `json:"generation"`
	PolicyChecksum           string      `json:"policyChecksum"`
	BaseChecksum             string      `json:"baseChecksum"`
	Checksum                 string      `json:"checksum"`
	Desired                  Desired     `json:"desired"`
	Base                     Snapshot    `json:"base"`
	Options                  PlanOptions `json:"options"`
	Operations               []Operation `json:"operations"`
	Budget                   Budget      `json:"budget"`
	NativeValidated          bool        `json:"nativeValidated"`
	RequiresSafeMode         bool        `json:"requiresSafeMode"`
	RequiresExclusiveControl bool        `json:"requiresExclusiveControl"`
	RequiresTargetValidation bool        `json:"requiresTargetValidation"`
}

type Resource string

const (
	RoutingTable  Resource = "routing-table"
	BGPInstance   Resource = "bgp-instance"
	BGPConnection Resource = "bgp-connection"
	FilterRule    Resource = "filter-rule"
)

type Action string

const (
	Add     Action = "add"
	Set     Action = "set"
	Enable  Action = "enable"
	Disable Action = "disable"
	Remove  Action = "remove"
	Unset   Action = "unset"
)

type Attribute struct {
	Name      string `json:"name"`
	Value     string `json:"-"`
	Sensitive bool   `json:"sensitive"`
}
type CompiledCommand struct {
	OperationID string      `json:"operationId"`
	Phase       Phase       `json:"phase"`
	Resource    Resource    `json:"resource"`
	Action      Action      `json:"action"`
	Object      ObjectRef   `json:"object"`
	Property    string      `json:"property,omitempty"`
	Attributes  []Attribute `json:"attributes"`
}
type CompiledPlan struct {
	PlanChecksum    string            `json:"planChecksum"`
	Commands        []CompiledCommand `json:"commands"`
	NativeValidated bool              `json:"nativeValidated"`
}
type Credential struct {
	Revision string `json:"revision"`
	Password string `json:"-"`
}
type Credentials map[string]Credential
