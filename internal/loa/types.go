package loa

// Data is the immutable content snapshot captured when an LOA is issued. It
// deliberately contains no mutable record lookup or external asset URL.
type Data struct {
	ID           string   `json:"id"`
	ExchangeName string   `json:"exchangeName"`
	MemberName   string   `json:"memberName"`
	MemberASN    uint32   `json:"memberAsn"`
	PanelName    string   `json:"panelName"`
	Location     string   `json:"location"`
	RackName     string   `json:"rackName"`
	Ports        []string `json:"ports"`
	SwitchPort   string   `json:"switchPort"`
	CircuitID    string   `json:"circuitId"`
	AllocationID string   `json:"allocationId"`
	IssuedAt     string   `json:"issuedAt"`
	ExpiresAt    string   `json:"expiresAt"`
	Reason       string   `json:"reason"`
}
