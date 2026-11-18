package routing

import (
	"github.com/moedove/ixp-manager/internal/as112"
	"strings"
	"testing"
)

func TestAS112HistoricalGracefulRestartProfile(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3", "frr"} {
		t.Run(platform, func(t *testing.T) {
			r := Router{ID: "as112-history", Role: RoleAS112, Platform: platform, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", AS112Service: "both", AS112IPv4: true}
			peers := []Peer{{ID: "p", Address: "192.0.2.1", ASN: 64500, Enabled: true, MaxPrefixes: 10, Password: "HistorySyntheticKey", AuthenticationRevision: "abcdefghijklmnopqrstuv"}}
			old, e := renderAS112Version(platform, r, peers, true)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = parseAS112Artifact(platform, old); e == nil {
				t.Fatal("historical GR-aware config accepted for new deployment")
			}
			redacted, e := RedactConfig(platform, old)
			if e != nil || strings.Contains(redacted, "HistorySyntheticKey") || !strings.Contains(redacted, RedactedBGPPassword) {
				t.Fatal("historical secret-safe preview unavailable", e)
			}
			if _, e = parseAS112Version(platform, old+"\n", true); e == nil {
				t.Fatal("historical parser was relaxed")
			}
			fresh, e := renderAS112(platform, r, peers)
			if e != nil {
				t.Fatal(e)
			}
			m, e := parseAS112Artifact(platform, fresh)
			if e != nil || m.Version != 2 {
				t.Fatal(m.Version, e)
			}
			if platform == "frr" {
				if !strings.Contains(fresh, "neighbor 192.0.2.1 graceful-restart-disable") {
					t.Fatal("GR not disabled")
				}
			} else {
				if !strings.Contains(fresh, "graceful restart off;\n  long lived graceful restart off;") {
					t.Fatal("GR/LLGR not disabled")
				}
			}
			b, e := as112.RenderBound(r.ID, "2026-11-15T00:00:00Z", AS112Service(r), as112.Identity{ServerName: "dns.example.net.", ResponsibleMailbox: "hostmaster.example.net.", Facility: "test", Location: "test", UniqueIP: r.LocalIPv4})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = AS112Standby(platform, old, b); e == nil {
				t.Fatal("historical config allowed ops")
			}
			standby, e := AS112Standby(platform, fresh, b)
			if e != nil {
				t.Fatal(e)
			}
			sm, e := parseAS112Artifact(platform, standby)
			if e != nil || len(sm.Peers) != 0 {
				t.Fatal("standby has peers", e)
			}
		})
	}
}
