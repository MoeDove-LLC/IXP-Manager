package routing

import (
	"errors"
	"fmt"
	"net/netip"
)

type RPKIConfig struct {
	Host          string `json:"host"`
	Port          uint16 `json:"port"`
	Policy        string `json:"policy"`
	ExpireSeconds uint32 `json:"expireSeconds"`
}

func normalizeRPKI(config *RPKIConfig) (*RPKIConfig, error) {
	if config == nil || config.Policy == "off" {
		return nil, nil
	}
	if config.Policy != "require-valid" {
		return nil, errors.New("runtime RPKI supports off or require-valid only; accepting unknown would fail open after cache expiry")
	}
	host, err := netip.ParseAddr(config.Host)
	if err != nil || host.Zone() != "" || host.Is4In6() || host.IsUnspecified() || host.IsMulticast() || host.IsLinkLocalUnicast() {
		return nil, errors.New("RPKI cache host must be a scoped-independent unicast IP address")
	}
	copy := *config
	copy.Host = host.String()
	if copy.Port == 0 {
		copy.Port = 323
	}
	if copy.ExpireSeconds == 0 {
		copy.ExpireSeconds = 600
	}
	if copy.ExpireSeconds < 60 || copy.ExpireSeconds > 86400 {
		return nil, errors.New("RPKI expiry must be60..86400 seconds")
	}
	return &copy, nil
}
func RuntimeRPKISupported(platform string) bool {
	return platform == "" || platform == "bird2" || platform == "bird3" || platform == "frr"
}
func birdRPKI(config *RPKIConfig) string {
	return fmt.Sprintf("roa4 table md_roa4;\nroa6 table md_roa6;\nprotocol rpki md_rpki {\n  roa4 { table md_roa4; };\n  roa6 { table md_roa6; };\n  remote %s port %d;\n  refresh keep 30;\n  retry keep 5;\n  expire keep %d;\n}\n\n", config.Host, config.Port, config.ExpireSeconds)
}
