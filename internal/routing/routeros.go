package routing

import (
	"fmt"
	"net/netip"
	"strings"
)

// renderRouterOS targets the BGP instance model introduced in RouterOS 7.20.
// This is a preview/bootstrap artifact, not a transactional updater. Every
// connection stays disabled pending device-side validation and safe-mode review.
func renderRouterOS(router Router, peers []Peer) string {
	peers = enabledPeers(peers)
	var b strings.Builder
	owner := "md_" + Checksum(router.ID)[:16]
	b.WriteString("# MoeDove IXP Manager - RouterOS 7.20+ bootstrap preview\n# All connections remain disabled. Validate on the target before activation.\n# Re-import is refused; no existing device objects are removed.\n")
	fmt.Fprintf(&b, ":if ([:len [/routing/bgp/instance find where name=\"%s\"]] > 0) do={ :error \"Managed instance already exists; reconcile the reviewed difference instead of importing twice\" }\n", owner)
	fmt.Fprintf(&b, ":if ([:len [/routing/filter/rule find where comment=\"%s\"]] > 0) do={ :error \"Managed policy already exists; inspect previous import\" }\n", owner)
	fmt.Fprintf(&b, ":if ([:len [/routing/table find where name=\"%s\"]] > 0) do={ :error \"Managed routing table already exists\" }\n", owner)
	// fib is a CLI switch, not a yes/no argument. CHR 7.24.6 rejects fib=no;
	// omission leaves it unset. This remains a preview and the target reader
	// must verify the resulting table before permitting any activation.
	fmt.Fprintf(&b, "/routing/table add name=%s\n/routing/bgp/instance add name=%s as=%d router-id=%s routing-table=%s\n", owner, owner, router.ASN, router.RouterID, owner)
	for _, p := range peers {
		name := owner + "_" + Checksum(p.ID)[:12]
		ip, _ := netip.ParseAddr(p.Address)
		local := router.LocalIPv4
		family := "ip"
		limit := "ipv4"
		if ip.Is6() {
			local = router.LocalIPv6
			family = "ipv6"
			limit = "ipv6"
		}
		for _, group := range originGroups(p) {
			origin := ""
			if group.ASN != 0 {
				origin = fmt.Sprintf(" && bgp-as-path %d\\$", group.ASN)
			}
			for _, prefix := range group.Prefixes {
				fmt.Fprintf(&b, "/routing/filter/rule add comment=%s chain=%s_in rule=\"if (dst == %s && bgp-path-len > 0 && bgp-path-len <= 64 && bgp-as-path ^%d%s) { accept }\"\n", owner, name, prefix, p.ASN, origin)
			}
		}
		fmt.Fprintf(&b, "/routing/filter/rule add comment=%s chain=%s_in rule=\"reject\"\n", owner, name)
		fmt.Fprintf(&b, "/routing/filter/rule add comment=%s chain=%s_out rule=\"if (protocol bgp && bgp-path-len > 0 && bgp-path-len <= 64 && bgp-input-remote-as != %d) { accept }\"\n/routing/filter/rule add comment=%s chain=%s_out rule=\"reject\"\n", owner, name, p.ASN, owner, name)
		authentication := ""
		if p.Password != "" {
			authentication = fmt.Sprintf(" tcp-md5-key=\"%s\"", p.Password)
		}
		if comment := authenticationComment(p, false); comment != "" {
			fmt.Fprintf(&b, "# %s\n", comment)
		}
		// A connection defaults to main even when its instance names a separate
		// table. Bind both levels explicitly; native readback must agree.
		fmt.Fprintf(&b, "/routing/bgp/connection add name=%s instance=%s routing-table=%s templates=\"\" disabled=yes afi=%s local.address=%s local.role=ebgp-rs remote.address=%s remote.as=%d nexthop-choice=propagate input.filter=%s_in input.limit-process-routes-%s=%d output.filter-chain=%s_out output.default-prepend=0 output.default-originate=never%s\n", name, owner, owner, family, local, ip, p.ASN, name, limit, p.MaxPrefixes, name, authentication)
	}
	return b.String()
}
