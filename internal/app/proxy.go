package app

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	result := []netip.Prefix{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			address, e := netip.ParseAddr(raw)
			if e != nil || address.Zone() != "" || address.Is4In6() {
				return nil, fmt.Errorf("trusted proxy must be an IP or canonical CIDR")
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix != prefix.Masked() || prefix.Addr().Is4In6() || prefix.Bits() == 0 {
			return nil, fmt.Errorf("trusted proxy CIDR must be canonical and not a default route")
		}
		result = append(result, prefix)
	}
	return result, nil
}
func (a *App) trustedProxy(address netip.Addr) bool {
	for _, prefix := range a.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	address = address.Unmap()
	fallback := address.String()
	if !a.trustedProxy(address) {
		return fallback
	}
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" || len(forwarded) > 4096 {
		return fallback
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return fallback
	}
	chain := make([]netip.Addr, len(parts))
	for i, raw := range parts {
		parsed, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || parsed.Zone() != "" {
			return fallback
		}
		chain[i] = parsed.Unmap()
	}
	// Walk from the immediate proxy toward the client. A forged leading address
	// never outranks the first untrusted hop appended by a correctly configured proxy.
	current := address
	for i := len(chain) - 1; i >= 0 && a.trustedProxy(current); i-- {
		current = chain[i]
	}
	return current.String()
}
