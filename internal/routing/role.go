package routing

import "errors"

const (
	RoleRouteServer         = "route-server"
	RoleRouteCollector      = "route-collector"
	CollectorObserve        = "observe"
	CollectorAuthorizedOnly = "authorized-only"
)

func normalizedRole(router Router) (role, policy string, err error) {
	role, policy = router.Role, router.CollectorPolicy
	if role == "" {
		role = RoleRouteServer
	}
	switch role {
	case RoleAS112:
		if policy != "" {
			return "", "", errors.New("collectorPolicy is not applicable to AS112")
		}
		if err := AS112Service(router).Validate(); err != nil {
			return "", "", err
		}
	case RoleRouteServer:
		if policy != "" {
			return "", "", errors.New("collectorPolicy is only valid for a route-collector")
		}
	case RoleRouteCollector:
		if policy == "" {
			policy = CollectorObserve
		}
		if policy != CollectorObserve && policy != CollectorAuthorizedOnly {
			return "", "", errors.New("collectorPolicy must be observe or authorized-only")
		}
	default:
		return "", "", errors.New("role must be route-server, route-collector or as112")
	}
	if role != RoleAS112 && (router.AS112Service != "" || router.AS112IPv4 || router.AS112IPv6 || router.AS112NextHop4 != "" || router.AS112NextHop6 != "") {
		return "", "", errors.New("AS112 settings require the as112 role")
	}
	return role, policy, nil
}

// RoleSupported reports only implemented isolation and policy combinations.
func RoleSupported(platform, role string) bool {
	if platform == "" {
		platform = "bird2"
	}
	if role == "" {
		role = RoleRouteServer
	}
	if role == RoleRouteCollector {
		return platform == "bird1" || platform == "bird2" || platform == "bird3" || platform == "frr" || platform == "openbgpd"
	}
	if role == RoleAS112 {
		return platform == "bird2" || platform == "bird3" || platform == "frr" || platform == "openbgpd" || platform == "junos"
	}
	return role == RoleRouteServer && (platform == "quagga" || platform == "bird1" || platform == "bird2" || platform == "bird3" || platform == "frr" || platform == "openbgpd" || platform == "junos" || platform == "routeros7")
}

func RFC1997ControlSupported(platform string) bool {
	return platform == "" || platform == "bird2" || platform == "bird3" || platform == "openbgpd"
}
