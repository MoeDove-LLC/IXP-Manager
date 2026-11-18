package netconf

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type globalRouting struct {
	XMLName xml.Name
	Options []struct {
		IDs  []string `xml:"router-id"`
		ASNs []struct {
			Numbers []string `xml:"as-number"`
		} `xml:"autonomous-system"`
	} `xml:"routing-options"`
}

func explicitGlobals(raw string) (*netip.Addr, *uint32, error) {
	var c globalRouting
	if err := xml.Unmarshal([]byte(raw), &c); err != nil {
		return nil, nil, errors.New("global routing configuration contains invalid XML")
	}
	if c.XMLName.Local != "configuration" || len(c.Options) > 1 {
		return nil, nil, errors.New("invalid or ambiguous explicit global routing configuration")
	}
	if len(c.Options) == 0 {
		return nil, nil, nil
	}
	o := c.Options[0]
	if len(o.IDs) > 1 || len(o.ASNs) > 1 {
		return nil, nil, errors.New("duplicate explicit global routing identity")
	}
	var id *netip.Addr
	var asn *uint32
	if len(o.IDs) == 1 {
		v, err := netip.ParseAddr(strings.TrimSpace(o.IDs[0]))
		if err != nil || !v.Is4() {
			return nil, nil, errors.New("invalid explicit global router-id")
		}
		id = &v
	}
	if len(o.ASNs) == 1 {
		if len(o.ASNs[0].Numbers) != 1 {
			return nil, nil, errors.New("invalid explicit global autonomous-system")
		}
		v, err := parseASNumber(o.ASNs[0].Numbers[0])
		if err != nil {
			return nil, nil, err
		}
		asn = &v
	}
	return id, asn, nil
}
func parseASNumber(raw string) (uint32, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) == 2 {
		high, e1 := strconv.ParseUint(parts[0], 10, 16)
		low, e2 := strconv.ParseUint(parts[1], 10, 16)
		if e1 != nil || e2 != nil {
			return 0, errors.New("invalid global ASN")
		}
		return uint32(high<<16 | low), nil
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, errors.New("invalid global ASN")
	}
	return uint32(v), nil
}
func checkGlobalRoutingIdentity(running, proposed string) error {
	oldID, oldASN, err := explicitGlobals(running)
	if err != nil {
		return err
	}
	newID, newASN, err := explicitGlobals(proposed)
	if err != nil {
		return err
	}
	if oldID != nil && newID != nil && *oldID != *newID {
		return fmt.Errorf("candidate changes existing global router-id; review the device scope and explicitly use --allow-global-routing-change")
	}
	if oldASN != nil && newASN != nil && *oldASN != *newASN {
		return fmt.Errorf("candidate changes existing global ASN; review the device scope and explicitly use --allow-global-routing-change")
	}
	return nil
}
