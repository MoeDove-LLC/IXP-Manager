package as112ops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

type opsPeer struct {
	ID        string              `json:"id"`
	Connected bool                `json:"connected"`
	Closed    bool                `json:"closed"`
	Routes    map[string][]uint16 `json:"routes"`
	Error     string              `json:"error"`
}
type opsPeerReply struct {
	Peers []opsPeer `json:"peers"`
	Error string    `json:"error"`
}

func opsPeerControl(action string) (opsPeerReply, error) {
	var reply opsPeerReply
	c, e := net.DialTimeout("unix", "/scratch/as112-wire-peer.sock", time.Second)
	if e != nil {
		return reply, e
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if e = json.NewEncoder(c).Encode(map[string]string{"action": action}); e != nil {
		return reply, e
	}
	e = json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&reply)
	if e == nil && reply.Error != "" {
		e = errors.New(reply.Error)
	}
	return reply, e
}
func opsWireWait(t *testing.T, closed bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last opsPeerReply
	for time.Now().Before(deadline) {
		reply, e := opsPeerControl("state")
		last = reply
		if e != nil {
			t.Fatal(e)
		}
		good := len(reply.Peers) == 4
		for _, peer := range reply.Peers {
			if closed {
				good = good && peer.Closed && !peer.Connected && len(peer.Routes) == 0
			} else {
				good = good && !peer.Closed && peer.Connected && peer.Error == "" && len(peer.Routes) == 2
				for _, path := range peer.Routes {
					good = good && len(path) == 1 && path[0] == 112
				}
			}
		}
		if good {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wire state closed=%v not established: %+v", closed, last)
}
func opsRequireNative(t *testing.T) {
	t.Helper()
	if os.Getenv("AS112_OPS_NATIVE") != "1" {
		t.Skip("explicit isolated real DNS+BGP operations fixture required")
	}
	raw, e := os.ReadFile("/proc/self/status")
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		want := "0000000000000000"
		if field == "NoNewPrivs" {
			want = "1"
		}
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field+":" {
				found = parts[1] == want
			}
		}
		if !found {
			t.Fatal("ops native requires zero capabilities and no_new_privs")
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range interfaces {
		if v.Name == "node0" {
			found = true
		} else if v.Name != "lo" {
			t.Fatal("unexpected external interface")
		}
	}
	if !found {
		t.Fatal("trusted private veth namespace missing")
	}
}

// Real service operations, not a parser or a mocked health decision. The
// trusted bootstrap supplies private addresses/veth then drops all privileges.
// The peer helper rejects GR/LLGR capabilities and clears its RIB on disconnect.
func TestNativeOperationalDNSFailureWithdrawsAndRecovers(t *testing.T) {
	opsRequireNative(t)
	defer opsPeerControl("shutdown")
	for _, platform := range []string{"bird2", "bird3", "frr"} {
		t.Run(platform, func(t *testing.T) {
			paths := map[string]string{"bird2": "/native/common/usr/sbin/bird", "bird3": "/native/bird3/usr/sbin/bird", "frr": "/native/common/usr/lib/frr/bgpd"}
			routerPin, e := PinBinary(paths[platform])
			if e != nil {
				t.Fatal(e)
			}
			dnsPin, e := PinBinary("/native/common/usr/sbin/pdns_server")
			if e != nil {
				t.Fatal(e)
			}
			zonePin, e := PinBinary("/native/common/usr/bin/named-checkzone")
			if e != nil {
				t.Fatal(e)
			}
			router := routing.Router{ID: "wire_as112", Role: routing.RoleAS112, Platform: platform, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: "both", AS112IPv4: true, AS112IPv6: true}
			peers := []routing.Peer{{ID: "sender4", Address: "192.0.2.1", ASN: 64501, Enabled: true, MaxPrefixes: 10}, {ID: "observer4", Address: "192.0.2.2", ASN: 64502, Enabled: true, MaxPrefixes: 10}, {ID: "sender6", Address: "2001:db8::1", ASN: 64501, Enabled: true, MaxPrefixes: 10}, {ID: "observer6", Address: "2001:db8::2", ASN: 64502, Enabled: true, MaxPrefixes: 10}}
			active, e := routing.RenderFor(platform, router, peers)
			if e != nil {
				t.Fatal(e)
			}
			b, e := as112.RenderNode(router.ID, "2026-11-15T00:00:00Z", routing.AS112Service(router), as112.Identity{ServerName: "ops.example.net.", ResponsibleMailbox: "hostmaster.example.net.", Facility: "isolated operations fixture", Location: "private namespace", UniqueIP: router.LocalIPv4}, []string{router.LocalIPv4, router.LocalIPv6})
			if e != nil {
				t.Fatal(e)
			}
			raw, e := as112.Encode(b)
			if e != nil {
				t.Fatal(e)
			}
			host, e := CaptureTarget()
			if e != nil {
				t.Fatal(e)
			}
			root, e := os.MkdirTemp("/scratch/tmp", "as112-ops-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(root)
			p, e := BuildPlan(PlanInput{Root: root, Platform: platform, ActiveConfig: active, DNSBundle: raw, Tools: Tools{Router: routerPin, PDNS: dnsPin, NamedCheckzone: zonePin}, Host: host})
			if e != nil {
				t.Fatal(e)
			}
			runtime := NewNativeRuntime()
			c, e := Open(p, runtime)
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if _, e = c.Install(ctx, approved(p)); e != nil {
				t.Fatal("native install", e)
			}
			if _, e = opsPeerControl("start"); e != nil {
				t.Fatal(e)
			}
			defer opsPeerControl("stop")
			for i := 0; i < p.RecoveryHealthyChecks; i++ {
				if _, e = c.Tick(ctx, true); e != nil {
					t.Fatal("native initial health", e)
				}
			}
			state := c.Status()
			if state.Phase != "active" || state.DNS == nil || state.BGP == nil {
				t.Fatal(state)
			}
			opsWireWait(t, false)
			oldBGP := *state.BGP
			// Freeze only this test's owned DNS child. The real UDP probe must fail;
			// its PID remains present, so process-liveness checks cannot pass the test.
			dns, e := os.FindProcess(state.DNS.PID)
			if e != nil {
				t.Fatal(e)
			}
			if e = dns.Signal(syscall.SIGSTOP); e != nil {
				t.Fatal(e)
			}
			defer dns.Signal(syscall.SIGCONT)
			state, e = c.Tick(ctx, true)
			if e != nil || state.Phase != "withdrawn" || state.LastHealthy || state.BGP != nil {
				t.Fatal("unhealthy DNS did not withdraw before returning", state, e)
			}
			if _, e = procStart(oldBGP.PID); !os.IsNotExist(e) {
				t.Fatal("old BGP process survived withdrawal", e)
			}
			opsWireWait(t, true)
			if e = dns.Signal(syscall.SIGCONT); e != nil {
				t.Fatal(e)
			}
			if _, e = opsPeerControl("start"); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < p.RecoveryHealthyChecks; i++ {
				state, e = c.Tick(ctx, true)
				if e != nil {
					t.Fatal("native health recovery", e)
				}
				if i+1 < p.RecoveryHealthyChecks && state.Phase == "active" {
					t.Fatal("recovered before health threshold")
				}
			}
			if state.Phase != "active" || state.BGP == nil || *state.BGP == oldBGP {
				t.Fatal("recovery did not create new owned process", state)
			}
			opsWireWait(t, false)
			if _, e = c.Withdraw(ctx); e != nil {
				t.Fatal(e)
			}
			opsWireWait(t, true)
			if e = c.Close(); e != nil {
				t.Fatal(e)
			}
			observation, e := runtime.Observe(ctx, p)
			if e != nil || len(observation.Processes) != 0 {
				t.Fatal("native close left processes", observation, e)
			}
			t.Logf("platform=%s native_installed=true actual_udp_tcp_health=true dns_sigstop_detected=true original_bgp_exited=true remote_rib_cleared=true recovery_new_sessions=true graceful_restart_capability_absent=true capability_zero=true no_new_privs=true", platform)
		})
	}
}
