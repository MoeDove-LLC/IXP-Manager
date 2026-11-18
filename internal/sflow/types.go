// Package sflow decodes bounded sFlow v5 metadata. It never retains packet
// payloads or assigns tenants; attribution belongs to the authoritative inventory.
package sflow

import "time"

const (
	MaxDatagramBytes = 65507
	MaxSamples       = 256
	MaxRecords       = 256
	MaxHeaderBytes   = 4096
)

type Interface struct {
	Format uint32 `json:"format"`
	Value  uint32 `json:"value"`
}
type Flow struct {
	Sequence              uint32    `json:"sequence"`
	SourceType            uint32    `json:"sourceType"`
	SourceIndex           uint32    `json:"sourceIndex"`
	SamplingRate          uint32    `json:"samplingRate"`
	SamplePool            uint32    `json:"samplePool"`
	Drops                 uint32    `json:"drops"`
	Input                 Interface `json:"input"`
	Output                Interface `json:"output"`
	PacketSize            uint32    `json:"packetSize"`
	SourceMAC             string    `json:"sourceMac,omitempty"`
	DestinationMAC        string    `json:"destinationMac,omitempty"`
	Family                int       `json:"family"`
	HeaderVLANs           []uint16  `json:"headerVlans,omitempty"`
	InputVLAN             *uint32   `json:"inputVlan,omitempty"`
	OutputVLAN            *uint32   `json:"outputVlan,omitempty"`
	EstimatedBytes        string    `json:"estimatedBytes"`
	EstimatedPackets      string    `json:"estimatedPackets"`
	SampleCount           uint32    `json:"sampleCount"`
	SampledTCP179Activity bool      `json:"sampledTcp179Activity"`
	UnattributedReason    string    `json:"unattributedReason,omitempty"`
	UnknownRecords        uint32    `json:"unknownRecords"`
}

// Counter samples remain separate from flow estimates and from the authenticated
// SNMP counter pipeline. Decimal strings preserve all uint64 bits in JSON.
type InterfaceCounters struct {
	IfIndex            uint32 `json:"ifIndex"`
	IfType             uint32 `json:"ifType"`
	IfSpeed            string `json:"ifSpeed"`
	IfDirection        uint32 `json:"ifDirection"`
	IfStatus           uint32 `json:"ifStatus"`
	InOctets           string `json:"inOctets"`
	InUnicast          uint32 `json:"inUnicast"`
	InMulticast        uint32 `json:"inMulticast"`
	InBroadcast        uint32 `json:"inBroadcast"`
	InDiscards         uint32 `json:"inDiscards"`
	InErrors           uint32 `json:"inErrors"`
	InUnknownProtocols uint32 `json:"inUnknownProtocols"`
	OutOctets          string `json:"outOctets"`
	OutUnicast         uint32 `json:"outUnicast"`
	OutMulticast       uint32 `json:"outMulticast"`
	OutBroadcast       uint32 `json:"outBroadcast"`
	OutDiscards        uint32 `json:"outDiscards"`
	OutErrors          uint32 `json:"outErrors"`
	Promiscuous        uint32 `json:"promiscuous"`
}
type CounterSample struct {
	Sequence       uint32              `json:"sequence"`
	SourceType     uint32              `json:"sourceType"`
	SourceIndex    uint32              `json:"sourceIndex"`
	Interfaces     []InterfaceCounters `json:"interfaces"`
	UnknownRecords uint32              `json:"unknownRecords"`
}
type Datagram struct {
	Version        uint32          `json:"version"`
	AgentAddress   string          `json:"agentAddress"`
	SubAgentID     uint32          `json:"subAgentId"`
	Sequence       uint32          `json:"sequence"`
	UptimeMS       uint32          `json:"uptimeMs"`
	Flows          []Flow          `json:"flows"`
	Counters       []CounterSample `json:"counters"`
	UnknownSamples uint32          `json:"unknownSamples"`
	UnknownRecords uint32          `json:"unknownRecords"`
}
type Quality struct {
	SequenceState      string `json:"sequenceState"`
	MissingDatagrams   uint32 `json:"missingDatagrams"`
	SampleSequenceGaps uint64 `json:"sampleSequenceGaps"`
	CounterSamples     uint32 `json:"counterSamples"`
	UDPAuthenticated   bool   `json:"udpAuthenticated"`
}
type Batch struct {
	Version           int       `json:"version"`
	ExporterID        string    `json:"exporterId"`
	SwitchID          string    `json:"switchId"`
	MappingRevision   string    `json:"mappingRevision"`
	ReceivedAt        time.Time `json:"receivedAt"`
	TransportSource   string    `json:"transportSource"`
	Datagram          Datagram  `json:"datagram"`
	Quality           Quality   `json:"quality"`
	RequestNonce      string    `json:"requestNonce,omitempty"`
	DatagramDigest    string    `json:"datagramDigest,omitempty"`
	RequestDeliveries bool      `json:"requestDeliveries,omitempty"`
}
type Binding struct {
	ExporterID        string    `json:"exporterId"`
	SwitchID          string    `json:"switchId"`
	MappingRevision   string    `json:"mappingRevision"`
	IssuedAt          time.Time `json:"issuedAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
	ForwardingAllowed bool      `json:"forwardingAllowed,omitempty"`
}
