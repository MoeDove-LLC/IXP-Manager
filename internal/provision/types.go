// Package provision renders constrained DNS and monitoring configuration.
// Rendered means generated, never installed or active on a target service.
package provision

import (
	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/core"
)

type Options struct {
	RouterID         string           `json:"routerId,omitempty"`
	AS112            *as112.Identity  `json:"as112,omitempty"`
	Kind             string           `json:"kind"` // dns-arpa, nagios, smokeping, switch-core
	SwitchID         string           `json:"switchId,omitempty"`
	VLANID           string           `json:"vlanId,omitempty"`
	InfrastructureID string           `json:"infrastructureId,omitempty"`
	Families         []int            `json:"families,omitempty"` // default [4,6]
	DNS              DNSOptions       `json:"dns,omitempty"`
	Nagios           NagiosOptions    `json:"nagios,omitempty"`
	Smokeping        SmokepingOptions `json:"smokeping,omitempty"`
}
type DNSOptions struct {
	Prefix             string   `json:"prefix"`
	PrimaryNS          string   `json:"primaryNS"`
	ResponsibleMailbox string   `json:"responsibleMailbox"` // DNS RNAME: hostmaster.example.net.
	NameServers        []string `json:"nameServers"`
	Serial             uint32   `json:"serial"`               // assigned/frozen by controller, never derived in Render
	TTL                uint32   `json:"ttl,omitempty"`        // defaults 86400
	Refresh            uint32   `json:"refresh,omitempty"`    // defaults 43200
	Retry              uint32   `json:"retry,omitempty"`      // defaults 7200
	Expire             uint32   `json:"expire,omitempty"`     // defaults 1209600
	Minimum            uint32   `json:"minimum,omitempty"`    // defaults 7200
	NameSuffix         string   `json:"nameSuffix,omitempty"` // optional deterministic fallback hostname suffix
	ActiveOnly         bool     `json:"activeOnly,omitempty"` // false preserves old assigned PTR coverage
}
type NagiosOptions struct {
	MemberHostTemplate      string `json:"memberHostTemplate,omitempty"`
	SwitchHostTemplate      string `json:"switchHostTemplate,omitempty"`
	RouterHostTemplate      string `json:"routerHostTemplate,omitempty"`
	ServiceTemplate         string `json:"serviceTemplate,omitempty"`
	PingServiceTemplate     string `json:"pingServiceTemplate,omitempty"`
	BusyPingServiceTemplate string `json:"busyPingServiceTemplate,omitempty"`
	BGPServiceTemplate      string `json:"bgpServiceTemplate,omitempty"`
	RouterServiceTemplate   string `json:"routerServiceTemplate,omitempty"`
	SwitchServiceTemplate   string `json:"switchServiceTemplate,omitempty"`
	CoreServiceTemplate     string `json:"coreServiceTemplate,omitempty"`
}
type SmokepingOptions struct {
	Level  int    `json:"level,omitempty"`  // start heading depth 1..4, default 1
	Probe4 string `json:"probe4,omitempty"` // default FPing
	Probe6 string `json:"probe6,omitempty"` // default FPing6
}
type Target struct {
	ID               string   `json:"id"`   // unique per address, e.g. connection-id-v4
	Kind             string   `json:"kind"` // member, switch, router
	Name             string   `json:"name"`
	Address          string   `json:"address"`
	DNSName          string   `json:"dnsName,omitempty"`
	MemberID         string   `json:"memberId,omitempty"`
	ASN              uint32   `json:"asn,omitempty"`
	Monitor          bool     `json:"monitor"`
	Active           bool     `json:"active"`
	CanPing          bool     `json:"canPing"`
	Busy             bool     `json:"busy,omitempty"`
	VLANID           string   `json:"vlanId,omitempty"`
	VLANName         string   `json:"vlanName,omitempty"`
	InfrastructureID string   `json:"infrastructureId,omitempty"`
	SwitchID         string   `json:"switchId,omitempty"`  // actual switch record for switch target
	RouterID         string   `json:"routerId,omitempty"`  // actual router record for router target
	SwitchIDs        []string `json:"switchIds,omitempty"` // LAG-aware membership
	RackIDs          []string `json:"rackIds,omitempty"`
	LocationIDs      []string `json:"locationIds,omitempty"`
	VendorID         string   `json:"vendorId,omitempty"`
	Model            string   `json:"model,omitempty"`
}
type Session struct {
	ID         string `json:"id"`       // actual peer record ID
	TargetID   string `json:"targetId"` // member address target above
	RouterID   string `json:"routerId"`
	RouterRole string `json:"routerRole,omitempty"` // route-server, route-collector, as112
	Enabled    bool   `json:"enabled"`
	Monitor    bool   `json:"monitor"`
}
type CoreBundle struct {
	ID       string `json:"id"`
	TargetID string `json:"targetId"` // switch target above
	Enabled  bool   `json:"enabled"`
}
type Input struct {
	AS112NodeAddresses   []string         `json:"as112NodeAddresses,omitempty"`
	AS112Service         *as112.Service   `json:"as112Service,omitempty"`
	AS112RouterCreatedAt string           `json:"as112RouterCreatedAt,omitempty"`
	CoreConfig           *core.SwitchPlan `json:"coreConfig,omitempty"`
	Options              Options          `json:"options"`
	Targets              []Target         `json:"targets"`
	Sessions             []Session        `json:"sessions,omitempty"`
	CoreBundles          []CoreBundle     `json:"coreBundles,omitempty"`
}
type Notice struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	TargetID string `json:"targetId,omitempty"`
}
type Artifact struct {
	Kind            string   `json:"kind"`
	Content         string   `json:"content"`
	Filename        string   `json:"filename"`
	Zone            string   `json:"zone,omitempty"`
	ValidationLevel string   `json:"validationLevel"` // native for complete DNS; structural for include snippets
	Complete        bool     `json:"complete"`
	Notices         []Notice `json:"notices"`
	TargetCount     int      `json:"targetCount"`
}
type Validation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
type ValidatorOptions struct {
	PDNSServer     string
	NamedCheckzone string
	Nagios         string
	Smokeping      string
	MainConfig     string
}

// Implementations are in render.go and validate.go. The command invocations are
// fixed; names/paths in the renderer Options never select an executable.
