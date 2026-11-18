package app

import (
	"errors"

	"github.com/moedove/ixp-manager/internal/routing"
)

func init() {
	Schemas["routers"]["role"] = enum("route-server", "route-collector", "as112")
	Schemas["routers"]["as112Service"] = enum("direct-delegation", "dname", "both")
	Schemas["routers"]["as112IPv4"] = Field{Type: "boolean"}
	Schemas["routers"]["as112IPv6"] = Field{Type: "boolean"}
	Schemas["routers"]["as112NextHop4"] = Field{Type: "ip"}
	Schemas["routers"]["as112NextHop6"] = Field{Type: "ip"}
	Schemas["routers"]["collectorPolicy"] = enum("observe", "authorized-only")
	Schemas["routers"]["rfc1997Passthrough"] = Field{Type: "boolean"}
}

func routerRole(r Record) string {
	if role := str(r, "role"); role != "" {
		return role
	}
	return "route-server"
}

func routerCollectorPolicy(r Record) string {
	if routerRole(r) != "route-collector" {
		return ""
	}
	if policy := str(r, "collectorPolicy"); policy != "" {
		return policy
	}
	return "observe"
}

func validateRouterRole(r Record) error {
	if routerRole(r) == routing.RoleAS112 {
		if !routing.RoleSupported(routerPlatform(r), routing.RoleAS112) {
			return errors.New("this platform has no AS112 origin-only adapter")
		}
		if number(r, "asn") != 112 {
			return errors.New("AS112 requires fixed ASN 112")
		}
		if str(r, "collectorPolicy") != "" || str(r, "rpkiPolicy") != "" || str(r, "rpkiHost") != "" || r["rpkiPort"] != nil || r["rpkiExpireSeconds"] != nil || r["rfc1997Passthrough"] != nil {
			return errors.New("AS112 uses fixed origin-only policy; remove collector, RPKI and RFC1997 controls")
		}
		v4, _ := r["as112IPv4"].(bool)
		v6, _ := r["as112IPv6"].(bool)
		if err := routing.AS112Service(routing.Router{AS112Service: str(r, "as112Service"), AS112IPv4: v4, AS112IPv6: v6}).Validate(); err != nil {
			return err
		}
		_, err := routing.RenderFor(routerPlatform(r), routing.Router{ID: str(r, "id"), Role: routing.RoleAS112, ASN: 112, RouterID: str(r, "routerId"), LocalIPv4: str(r, "localIpv4"), LocalIPv6: str(r, "localIpv6"), AS112Service: str(r, "as112Service"), AS112IPv4: v4, AS112IPv6: v6, AS112NextHop4: str(r, "as112NextHop4"), AS112NextHop6: str(r, "as112NextHop6")}, nil)
		return err
	}
	if str(r, "as112Service") != "" || r["as112IPv4"] == true || r["as112IPv6"] == true || str(r, "as112NextHop4") != "" || str(r, "as112NextHop6") != "" {
		return errors.New("AS112 settings require the as112 role")
	}
	if value, present := r["rfc1997Passthrough"]; present && value != nil {
		if _, ok := value.(bool); !ok {
			return errors.New("rfc1997Passthrough must be boolean or omitted")
		}
		if !routing.RFC1997ControlSupported(routerPlatform(r)) {
			return errors.New("this platform has no verified RFC1997 passthrough control; omit the field to retain its native behavior")
		}
	}
	if routerRole(r) == "route-server" {
		if str(r, "collectorPolicy") != "" {
			return errors.New("collectorPolicy is only available for route-collector routers")
		}
		return nil
	}
	if !routing.RoleSupported(routerPlatform(r), routerRole(r)) {
		return errors.New("this platform has no verified route-collector adapter")
	}
	return nil
}

func peerCanObserveWithoutAllowlist(s *State, p Record) bool {
	router := find(s, "routers", str(p, "routerId"))
	if routerRole(router) == routing.RoleAS112 {
		return true
	}
	return routerRole(router) == "route-collector" && routerCollectorPolicy(router) == "observe"
}
