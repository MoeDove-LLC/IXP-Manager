// Package core describes desired core topology. These objects never assert
// device configuration or operational health.
package core

const (
	ECMP          = "ecmp"
	L2LAG         = "l2-lag"
	L3LAG         = "l3-lag"
	MaxBundles    = 2000
	MaxLinks      = 128
	MaxTotalLinks = 10000
)

type Endpoint struct {
	ID            string  `json:"id"`
	PortID        string  `json:"portId"`
	SpeedMbps     *uint64 `json:"speedMbps,omitempty"`
	Duplex        string  `json:"duplex"`
	Autoneg       bool    `json:"autoneg"`
	Status        string  `json:"status,omitempty"`
	RateLimitMbps *uint64 `json:"rateLimitMbps,omitempty"`
	Notes         string  `json:"notes,omitempty"`
}

type Side struct {
	SwitchID      string  `json:"switchId"`
	OwnerMemberID string  `json:"ownerMemberId,omitempty"`
	InterfaceName string  `json:"interfaceName"`
	Description   string  `json:"description,omitempty"`
	ChannelGroup  *uint32 `json:"channelGroup,omitempty"`
	MTU           uint32  `json:"mtu"`
	Trunk         bool    `json:"trunk"`
	FastLACP      bool    `json:"fastLacp"`
	LAGFraming    bool    `json:"lagFraming"`
}

type Link struct {
	ID         string   `json:"id"`
	Enabled    bool     `json:"enabled"`
	BFD        bool     `json:"bfd"`
	IPv4Subnet string   `json:"ipv4Subnet,omitempty"`
	IPv6Subnet string   `json:"ipv6Subnet,omitempty"`
	SideA      Endpoint `json:"sideA"`
	SideB      Endpoint `json:"sideB"`
}

type Bundle struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	GraphTitle      string  `json:"graphTitle"`
	Type            string  `json:"type"`
	Enabled         bool    `json:"enabled"`
	BFD             bool    `json:"bfd"`
	IPv4Subnet      string  `json:"ipv4Subnet,omitempty"`
	IPv6Subnet      string  `json:"ipv6Subnet,omitempty"`
	STP             bool    `json:"stp"`
	Cost            *uint32 `json:"cost,omitempty"`
	Preference      *uint32 `json:"preference,omitempty"`
	SideA           Side    `json:"sideA"`
	SideB           Side    `json:"sideB"`
	Links           []Link  `json:"links"`
	BindingRevision string  `json:"bindingRevision,omitempty"`
	CreatedAt       string  `json:"createdAt,omitempty"`
	UpdatedAt       string  `json:"updatedAt,omitempty"`
}

type AddressPair struct {
	Subnet string `json:"subnet"`
	SideA  string `json:"sideA"`
	SideB  string `json:"sideB"`
}

type DerivedLink struct {
	LinkID  string       `json:"linkId,omitempty"`
	IPv4    *AddressPair `json:"ipv4,omitempty"`
	IPv6    *AddressPair `json:"ipv6,omitempty"`
	BFD     bool         `json:"bfd"`
	Enabled bool         `json:"enabled"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Derived struct {
	Addresses     []DerivedLink `json:"addresses"`
	CapacityKnown bool          `json:"capacityKnown"`
	CapacityMbps  *uint64       `json:"capacityMbps,omitempty"`
	BGPReady      bool          `json:"bgpReady"`
	LocalASNA     uint32        `json:"localAsnA,omitempty"`
	LocalASNB     uint32        `json:"localAsnB,omitempty"`
	RouterIDA     string        `json:"routerIdA,omitempty"`
	RouterIDB     string        `json:"routerIdB,omitempty"`
	InventoryOnly bool          `json:"inventoryOnly"`
}
