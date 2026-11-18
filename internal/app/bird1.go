package app

import (
	"errors"
	"net/netip"
	"os/exec"

	"github.com/moedove/ixp-manager/internal/routing"
)

func (a *App) bird1ValidatorBinary(family int) (string, error) {
	switch family {
	case 4:
		if a.bird1Binary != "" {
			return a.bird1Binary, nil
		}
		return "bird1", nil
	case 6:
		if a.bird1IPv6Binary != "" {
			return a.bird1IPv6Binary, nil
		}
		return "bird6", nil
	default:
		return "", errors.New("BIRD 1 requires an explicit single-family reviewed artifact")
	}
}
func (a *App) routerValidatorBinaryForConfig(platform, config string) (string, error) {
	if platform != "bird1" {
		return a.routerValidatorBinary(platform), nil
	}
	family, e := routing.BIRD1Family(config)
	if e != nil {
		return "", e
	}
	return a.bird1ValidatorBinary(family)
}
func (a *App) bird1CheckerAvailability() Record {
	available := Record{}
	for _, pair := range []struct {
		name   string
		family int
	}{{"ipv4", 4}, {"ipv6", 6}} {
		binary, e := a.bird1ValidatorBinary(pair.family)
		if e == nil {
			_, e = exec.LookPath(binary)
		}
		available[pair.name] = e == nil
	}
	return available
}

// Only explicitly edited records are constrained here. Old unsupported BIRD 1
// inventory remains loadable/importable so operators can repair it; generating
// or validating an unsafe mixed-family artifact always fails independently.
func guardBIRD1ResourceMutation(s *State, kind, method string, body Record) error {
	if method != "POST" && method != "PUT" {
		return nil
	}
	if kind == "routers" && routerPlatform(body) == "bird1" && (str(body, "localIpv4") == "") == (str(body, "localIpv6") == "") {
		return errors.New("BIRD 1 requires one local address family; create separate IPv4 and IPv6 router instances")
	}
	if kind == "peers" && body["enabled"] == true {
		router := find(s, "routers", str(body, "routerId"))
		if routerPlatform(router) == "bird1" {
			ipv4, ipv6 := str(router, "localIpv4") != "", str(router, "localIpv6") != ""
			address, e := netip.ParseAddr(str(body, "address"))
			if ipv4 == ipv6 || e != nil || address.Is4() != ipv4 {
				return errors.New("BIRD 1 peer must match a single-family router; repair or split the router first")
			}
		}
	}
	return nil
}
