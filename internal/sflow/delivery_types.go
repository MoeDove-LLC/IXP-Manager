package sflow

import "time"

const (
	MaxDeliveries         = 32
	MaxDeliveryTTL        = 5 * time.Second
	MaxRelayDatagramBytes = 1400
)

type Delivery struct {
	ReceiverID           string `json:"receiverId"`
	ReceiverRevision     string `json:"receiverRevision"`
	Address              string `json:"address"`
	Port                 uint16 `json:"port"`
	FlowIndices          []int  `json:"flowIndices"`
	HeaderDataDisclosure bool   `json:"headerDataDisclosure"`
}
type DeliveryEnvelope struct {
	Version         int        `json:"version"`
	RequestNonce    string     `json:"requestNonce"`
	DatagramDigest  string     `json:"datagramDigest"`
	ExporterID      string     `json:"exporterId"`
	SwitchID        string     `json:"switchId"`
	MappingRevision string     `json:"mappingRevision"`
	IssuedAt        time.Time  `json:"issuedAt"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	Deliveries      []Delivery `json:"deliveries"`
}
type ForwardingPolicy struct {
	RelayAgentAddress       string   `json:"relayAgentAddress"`
	AllowedDestinationCIDRs []string `json:"allowedDestinationCidrs"`
	AllowedPorts            []uint16 `json:"allowedPorts"`
	MaxReceivers            int      `json:"maxReceivers"`
	MaxDatagramsPerSecond   int      `json:"maxDatagramsPerSecond"`
	MaxBytesPerSecond       int      `json:"maxBytesPerSecond"`
	MaxBytesPerBatch        int      `json:"maxBytesPerBatch"`
}

// Receipt outcomes acknowledge a local action only. UDP cannot confirm arrival,
// processing or confidentiality at the remote receiver.
type DeliveryReceipt struct {
	ReceiverID       string `json:"receiverId"`
	ReceiverRevision string `json:"receiverRevision"`
	Outcome          string `json:"outcome"`
	Datagrams        int    `json:"datagrams"`
	Bytes            int    `json:"bytes"`
}
type DeliveryReport struct {
	Version         int               `json:"version"`
	RequestNonce    string            `json:"requestNonce"`
	DatagramDigest  string            `json:"datagramDigest"`
	ExporterID      string            `json:"exporterId"`
	SwitchID        string            `json:"switchId"`
	MappingRevision string            `json:"mappingRevision"`
	ObservedAt      time.Time         `json:"observedAt"`
	Receipts        []DeliveryReceipt `json:"receipts"`
}
