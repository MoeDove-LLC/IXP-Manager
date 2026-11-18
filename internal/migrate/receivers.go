// Package migrate holds typed, offline conversion helpers. It grants no runtime
// authority and does not connect to databases, collectors or destination hosts.
package migrate

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/sflow"
)

type SFlowReceiverImport struct {
	Name string         `json:"name"`
	Spec sflow.Receiver `json:"spec"`
}
type ReceiverSource struct {
	ID                 int64
	VirtualInterfaceID int64
	MemberID           string
	Address            string
	Port               int64
	Enabled            bool
}
type ReceiverConnection struct {
	ID                 string
	MemberID           string
	VLANID             string
	VirtualInterfaceID int64
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// ConvertReceiver requires all attachments of the legacy VI. The table adapter
// must prove no source VLI was dropped before calling this helper.
func ConvertReceiver(source ReceiverSource, connections []ReceiverConnection) (SFlowReceiverImport, error) {
	empty := SFlowReceiverImport{}
	if source.ID < 1 || source.VirtualInterfaceID < 1 || !identifier.MatchString(source.MemberID) || len(connections) == 0 || len(connections) > 64 {
		return empty, errors.New("receiver requires a complete bounded member/VI/connection scope")
	}
	address, err := netip.ParseAddr(source.Address)
	if err != nil || source.Address != strings.TrimSpace(source.Address) || address.Is4In6() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() || address == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || source.Port < 1 || source.Port > 65535 {
		return empty, errors.New("receiver requires a literal unicast destination and port1..65535")
	}
	spec := sflow.Receiver{ID: fmt.Sprintf("legacy-sflow-receiver-%d", source.ID), MemberID: source.MemberID, Address: address.String(), Port: uint16(source.Port), Enabled: source.Enabled, ConnectionIDs: []string{}, VLANIDs: []string{}}
	seenConnections, seenVLANs := map[string]bool{}, map[string]bool{}
	for _, connection := range connections {
		if !identifier.MatchString(connection.ID) || !identifier.MatchString(connection.VLANID) || connection.MemberID != source.MemberID || connection.VirtualInterfaceID != source.VirtualInterfaceID || seenConnections[connection.ID] || seenVLANs[connection.VLANID] {
			return empty, errors.New("receiver attachments have missing, duplicate or conflicting ownership/VLAN references")
		}
		seenConnections[connection.ID] = true
		seenVLANs[connection.VLANID] = true
		spec.ConnectionIDs = append(spec.ConnectionIDs, connection.ID)
		spec.VLANIDs = append(spec.VLANIDs, connection.VLANID)
	}
	sort.Strings(spec.ConnectionIDs)
	sort.Strings(spec.VLANIDs)
	check := spec
	check.Revision = strings.Repeat("0", 64)
	if err = sflow.ValidateReceiver(check); err != nil {
		return empty, errors.New("receiver cannot be represented by the typed import schema")
	}
	// Revision remains empty. The application assigns a new revision and draft
	// or disabled status; neither approval nor delivery evidence is imported.
	return SFlowReceiverImport{Name: fmt.Sprintf("Legacy sFlow receiver %d", source.ID), Spec: spec}, nil
}

// RegisteredMAC recognizes an explicit EUI-48 registration. A learned MAC table
// is never a valid caller/source for this conversion.
func RegisteredMAC(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("registered MAC must be an explicit EUI-48 address")
	}
	var mac net.HardwareAddr
	if len(value) == 12 {
		raw, err := hex.DecodeString(value)
		if err != nil {
			return "", errors.New("invalid registered MAC encoding")
		}
		mac = net.HardwareAddr(raw)
	} else {
		parsed, err := net.ParseMAC(value)
		if err != nil {
			return "", errors.New("invalid registered MAC encoding")
		}
		mac = parsed
	}
	if len(mac) != 6 || mac[0]&1 != 0 {
		return "", errors.New("registered MAC must be a unicast EUI-48 address")
	}
	nonzero := false
	for _, octet := range mac {
		nonzero = nonzero || octet != 0
	}
	if !nonzero {
		return "", errors.New("zero MAC cannot establish an owner")
	}
	return mac.String(), nil
}
