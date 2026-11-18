package routing

import (
	"errors"
	"reflect"

	"github.com/moedove/ixp-manager/internal/as112"
)

// AS112Standby derives an exact generated configuration with no BGP sessions.
// The original active bytes and their authentication are never rewritten.
// Removing all sessions also makes standby safe before DNS has started.
func AS112Standby(platform, active string, bundle as112.Bundle) (string, error) {
	m, err := parseAS112Artifact(platform, active)
	if err != nil {
		return "", err
	}
	if err = as112.Validate(bundle); err != nil {
		return "", err
	}
	if bundle.RouterCreatedAt == "" || m.Router.ID != bundle.RouterID || AS112Service(m.Router).Mode != bundle.Service || !reflect.DeepEqual(AS112Service(m.Router).Families(), bundle.Families) {
		return "", errors.New("AS112 active artifact and bound DNS entity differ")
	}
	wanted := []string{}
	for _, v := range []struct {
		enabled     bool
		local, next string
	}{{m.Router.AS112IPv4, m.Router.LocalIPv4, m.Router.AS112NextHop4}, {m.Router.AS112IPv6, m.Router.LocalIPv6, m.Router.AS112NextHop6}} {
		if v.enabled {
			if platform == "junos" {
				wanted = append(wanted, v.next)
			} else {
				wanted = append(wanted, v.local)
			}
		}
	}
	if len(wanted) != len(bundle.NodeAddresses) {
		return "", errors.New("AS112 DNS node binding differs")
	}
	for _, address := range wanted {
		found := false
		for _, node := range bundle.NodeAddresses {
			found = found || address == node
		}
		if !found {
			return "", errors.New("AS112 DNS node binding differs")
		}
	}
	return renderAS112(platform, m.Router, nil)
}
