// Package snmp collects a fixed read-only set of standard device MIBs. Secret
// configuration is separate from snapshots and never sent to the manager API.
package snmp

import (
	"github.com/moedove/ixp-manager/internal/traffic"
	"time"
)

type PortMapping struct {
	OwnershipRevision string `json:"ownershipRevision,omitempty"`
	PortID            string `json:"portId"`
	IfIndex           uint32 `json:"ifIndex"`
}
type Config struct {
	Endpoint          string        `json:"endpoint"`
	SwitchID          string        `json:"switchId"`
	Target            string        `json:"target"`
	Port              uint16        `json:"port"`
	Version           string        `json:"version"`
	Username          string        `json:"username"`
	AuthProtocol      string        `json:"authProtocol"`
	AuthPassphrase    string        `json:"authPassphrase"`
	PrivacyProtocol   string        `json:"privacyProtocol"`
	PrivacyPassphrase string        `json:"privacyPassphrase"`
	Community         string        `json:"community"`
	AllowV2c          bool          `json:"allowV2c"`
	ContextName       string        `json:"contextName"`
	TimeoutSeconds    int           `json:"timeoutSeconds"`
	Retries           int           `json:"retries"`
	Mappings          []PortMapping `json:"mappings"`
	CollectEntities   bool          `json:"collectEntities"`
	CollectSensors    bool          `json:"collectSensors"`
	CollectFDB        bool          `json:"collectFdb"`
}
type Identity struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ObjectID    string `json:"objectId"`
	Location    string `json:"location"`
	Contact     string `json:"contact"`
}
type Interface struct {
	OwnershipRevision  string               `json:"ownershipRevision,omitempty"`
	IfIndex            uint32               `json:"ifIndex"`
	PortID             string               `json:"portId,omitempty"`
	Name               string               `json:"name"`
	Alias              string               `json:"alias,omitempty"`
	Description        string               `json:"description,omitempty"`
	Type               uint32               `json:"type"`
	MTU                uint32               `json:"mtu"`
	SpeedMbps          uint64               `json:"speedMbps"`
	AdminStatus        string               `json:"adminStatus"`
	OperStatus         string               `json:"operStatus"`
	PhysicalAddress    string               `json:"physicalAddress,omitempty"`
	InOctets           string               `json:"inOctets,omitempty"`
	OutOctets          string               `json:"outOctets,omitempty"`
	CounterBits        int                  `json:"counterBits,omitempty"`
	DiscontinuityTicks string               `json:"discontinuityTicks,omitempty"`
	LastChangeTicks    string               `json:"lastChangeTicks,omitempty"`
	InErrors           string               `json:"inErrors,omitempty"`
	OutErrors          string               `json:"outErrors,omitempty"`
	InDiscards         string               `json:"inDiscards,omitempty"`
	OutDiscards        string               `json:"outDiscards,omitempty"`
	Unicast            *traffic.CounterPair `json:"unicast,omitempty"`
	Multicast          *traffic.CounterPair `json:"multicast,omitempty"`
	Broadcast          *traffic.CounterPair `json:"broadcast,omitempty"`
	Errors             *traffic.CounterPair `json:"errors,omitempty"`
	Discards           *traffic.CounterPair `json:"discards,omitempty"`
}
type Entity struct {
	Index            uint32 `json:"index"`
	ParentIndex      uint32 `json:"parentIndex"`
	IfIndex          uint32 `json:"ifIndex,omitempty"`
	Class            uint32 `json:"class"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	Manufacturer     string `json:"manufacturer,omitempty"`
	Model            string `json:"model,omitempty"`
	Serial           string `json:"serial,omitempty"`
	HardwareRevision string `json:"hardwareRevision,omitempty"`
	FirmwareRevision string `json:"firmwareRevision,omitempty"`
	SoftwareRevision string `json:"softwareRevision,omitempty"`
}
type Sensor struct {
	EntityIndex uint32   `json:"entityIndex"`
	IfIndex     uint32   `json:"ifIndex,omitempty"`
	Name        string   `json:"name"`
	Type        int      `json:"type"`
	Scale       int      `json:"scale"`
	Precision   int      `json:"precision"`
	RawValue    string   `json:"rawValue"`
	Value       *float64 `json:"value,omitempty"`
	Unit        string   `json:"unit"`
	Status      string   `json:"status"`
}
type FDBEntry struct {
	FDBID      uint32   `json:"fdbId"`
	VLANIDs    []uint16 `json:"vlanIds"`
	MAC        string   `json:"mac"`
	BridgePort uint32   `json:"bridgePort"`
	IfIndex    uint32   `json:"ifIndex,omitempty"`
	Status     string   `json:"status"`
}
type Snapshot struct {
	EngineIdentity string      `json:"engineIdentity,omitempty"`
	Version        int         `json:"version"`
	SwitchID       string      `json:"switchId"`
	ObservedAt     time.Time   `json:"observedAt"`
	Security       string      `json:"security"`
	Identity       Identity    `json:"identity"`
	UptimeTicks    string      `json:"uptimeTicks"`
	EngineEpoch    string      `json:"engineEpoch,omitempty"`
	Interfaces     []Interface `json:"interfaces"`
	Entities       []Entity    `json:"entities"`
	Sensors        []Sensor    `json:"sensors"`
	FDB            []FDBEntry  `json:"fdb"`
	Warnings       []string    `json:"warnings"`
	ErrorCode      string      `json:"errorCode,omitempty"`
}
