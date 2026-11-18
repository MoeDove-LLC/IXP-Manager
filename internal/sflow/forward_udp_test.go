package sflow

import (
	"context"
	"flag"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var relayLoopbackFixture = flag.Bool("sflow-loopback-fixture", false, "run only inside the dedicated private-loopback sandbox")

func TestRelayUDPOnIsolatedLoopback(t *testing.T) {
	if !*relayLoopbackFixture {
		t.Skip("requires explicit private loopback sandbox flag")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	caps := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && (parts[0] == "CapEff:" || parts[0] == "CapBnd:") {
			v, err := strconv.ParseUint(parts[1], 16, 64)
			if err != nil || v != 0 {
				t.Fatal("target still has capabilities", parts)
			}
			caps[parts[0]] = true
		}
	}
	if len(caps) != 2 {
		t.Fatal("capability evidence missing")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 {
			t.Fatal("test namespace has a non-loopback interface")
		}
	}
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	destination := listener.LocalAddr().(*net.UDPAddr).AddrPort()
	if !destination.Addr().IsLoopback() {
		t.Fatal("fixture not loopback")
	}
	policy := forwardingFixture()
	policy.RelayAgentAddress = "127.0.0.254"
	policy.AllowedDestinationCIDRs = []string{"127.0.0.1/32"}
	policy.AllowedPorts = []uint16{destination.Port()}
	f, err := NewForwarder(policy, SendUDP, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	at := time.Now()
	p, b := relayBatch(t, mixedRelayDatagram(), at)
	d := grantedReceiver("receiver-loopback", destination.Addr().String(), 0)
	d.Port = destination.Port()
	envelope := grantFor(b, at, d)
	report, err := f.Forward(context.Background(), p, b, envelope, at, at, 0)
	if err != nil || report.Receipts[0].Outcome != "sent-to-socket" {
		t.Fatal(report, err)
	}
	_ = listener.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, MaxRelayDatagramBytes+1)
	n, source, err := listener.ReadFromUDPAddrPort(buffer)
	if err != nil || !source.Addr().IsLoopback() {
		t.Fatal("isolated UDP write not observed", source, err)
	}
	decoded, err := Parse(buffer[:n])
	if err != nil || decoded.AgentAddress != "127.0.0.254" || len(decoded.Flows) != 1 || len(decoded.Counters) != 0 || decoded.Flows[0].SourceMAC != b.Datagram.Flows[0].SourceMAC {
		t.Fatal("unexpected isolated UDP payload", decoded, err)
	}
	if err := SendUDP(context.Background(), netip.AddrPort{}, buffer[:n]); err == nil {
		t.Fatal("invalid literal destination accepted")
	}
}
