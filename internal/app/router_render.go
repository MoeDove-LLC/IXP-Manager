package app

import (
	"fmt"

	"github.com/moedove/ixp-manager/internal/routing"
)

func renderRouter(s *State, id string) (string, error) {
	return renderRouterWithAuthentication(s, id, nil)
}

func (a *App) renderRouter(s *State, id string) (string, error) {
	router, peers, err := a.routerRenderInputs(s, id)
	if err != nil {
		return "", err
	}
	return routing.RenderFor(router.Platform, router, peers)
}

func (a *App) routerRenderInputs(s *State, id string) (routing.Router, []routing.Peer, error) {
	return routerRenderInputs(s, id, func(p Record) (string, string, error) {
		return a.peerAuthentication(s, p)
	})
}

func renderRouterWithAuthentication(s *State, id string, authentication func(Record) (string, string, error)) (string, error) {
	router, peers, err := routerRenderInputs(s, id, authentication)
	if err != nil {
		return "", err
	}
	return routing.RenderFor(router.Platform, router, peers)
}

func routerRenderInputs(s *State, id string, authentication func(Record) (string, string, error)) (routing.Router, []routing.Peer, error) {
	r := find(s, "routers", id)
	if r == nil {
		return routing.Router{}, nil, fmt.Errorf("router not found")
	}
	router := routing.Router{ID: id, Platform: routerPlatform(r), Role: routerRole(r), CollectorPolicy: str(r, "collectorPolicy"), Name: str(r, "name"), ASN: uint32(number(r, "asn")), RouterID: str(r, "routerId"), LocalIPv4: str(r, "localIpv4"), LocalIPv6: str(r, "localIpv6")}
	router.AS112Service = str(r, "as112Service")
	router.AS112IPv4, _ = r["as112IPv4"].(bool)
	router.AS112IPv6, _ = r["as112IPv6"].(bool)
	router.AS112NextHop4, router.AS112NextHop6 = str(r, "as112NextHop4"), str(r, "as112NextHop6")
	if value, ok := r["rfc1997Passthrough"].(bool); ok {
		router.RFC1997Passthrough = &value
	}
	if str(r, "rpkiPolicy") == "require-valid" {
		port := number(r, "rpkiPort")
		if port == 0 {
			port = 323
		}
		expiry := number(r, "rpkiExpireSeconds")
		if expiry == 0 {
			expiry = 600
		}
		router.RPKI = &routing.RPKIConfig{Host: str(r, "rpkiHost"), Port: uint16(port), Policy: "require-valid", ExpireSeconds: uint32(expiry)}
	}
	peers := []routing.Peer{}
	for _, p := range s.Resources["peers"] {
		if str(p, "routerId") != id {
			continue
		}
		enabled, _ := p["enabled"].(bool)
		m := find(s, "members", str(p, "memberId"))
		if enabled && str(m, "status") != "active" {
			return routing.Router{}, nil, fmt.Errorf("peer %s member is not active", str(p, "name"))
		}
		if enabled && !peerHasActiveAttachment(s, r, p) {
			return routing.Router{}, nil, fmt.Errorf("peer %s requires an active connection on the router VLAN with the same address", str(p, "name"))
		}
		prefixes := []string{}
		for _, v := range p["allowedPrefixes"].([]any) {
			prefixes = append(prefixes, v.(string))
		}
		bindings := []routing.PrefixOrigin{}
		if items, ok := p["allowedRoutes"].([]any); ok {
			for _, item := range items {
				v := item.(map[string]any)
				bindings = append(bindings, routing.PrefixOrigin{Prefix: v["prefix"].(string), OriginASN: uint32(v["originAsn"].(float64))})
			}
		}
		var password, revision string
		if authentication != nil {
			var err error
			password, revision, err = authentication(p)
			if err != nil {
				return routing.Router{}, nil, err
			}
		} else if s.PeerCredentials[str(p, "id")].Ciphertext != "" {
			return routing.Router{}, nil, fmt.Errorf("credential-aware router renderer is required")
		}
		peers = append(peers, routing.Peer{Password: password, AuthenticationRevision: revision, AllowedRoutes: bindings, ID: str(p, "id"), Name: str(p, "name"), ASN: uint32(number(p, "asn")), Address: str(p, "address"), Enabled: enabled, MaxPrefixes: int(number(p, "maxPrefixes")), AllowedPrefixes: prefixes})
	}
	return router, peers, nil
}

func peerHasActiveAttachment(s *State, router, peer Record) bool {
	if router == nil || str(peer, "address") == "" {
		return false
	}
	for _, conn := range s.Resources["connections"] {
		if str(conn, "memberId") == str(peer, "memberId") && str(conn, "vlanId") == str(router, "vlanId") &&
			str(conn, "status") == "active" && (str(conn, "ipv4") == str(peer, "address") || str(conn, "ipv6") == str(peer, "address")) {
			return true
		}
	}
	return false
}
