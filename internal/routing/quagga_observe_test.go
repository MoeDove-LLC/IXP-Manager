package routing

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQuaggaSummaryWrappedIPv6AndExactTotals(t *testing.T) {
	const native = "BGP router identifier 192.0.2.112, local AS number 64500\n" +
		"RIB entries 1, using 112 bytes of memory\nPeers 2, using 53 KiB of memory\n\n" +
		"Neighbor        V         AS MsgRcvd MsgSent   TblVer  InQ OutQ Up/Down  State/PfxRcd\n" +
		"2001:db8:1111:2222:3333:4444:5555:1\n" +
		"                4 4200000003       1       3        0    0    0 00:00:01        0\n" +
		"2001:db8::2     4 64502       0       4        0    0    0 never Idle (PfxCt)\n\n" +
		"Total number of neighbors 2\n\nTotal num. Established sessions 1\nTotal num. of routes received     0\n"
	local := netip.MustParseAddr("192.0.2.112")
	rows, present, err := parseQuaggaSummary([]byte(native), local, 64500, true)
	if err != nil || !present || len(rows) != 2 {
		t.Fatal("genuine-format wrapped IPv6 summary not parsed", err)
	}
	up := rows[netip.MustParseAddr("2001:db8:1111:2222:3333:4444:5555:1")]
	down := rows[netip.MustParseAddr("2001:db8::2")]
	if !up.PrefixesKnown || up.ReceivedPrefixes != 0 || up.State != "Established" || up.NeighborASN != 4200000003 || down.PrefixesKnown || down.State != "Idle" {
		t.Fatalf("zero accepted prefixes conflated with missing/down count: %+v %+v", up, down)
	}
	for _, invalid := range []string{
		strings.Replace(native, "local AS number 64500", "local AS number 64501", 1),
		strings.Replace(native, "router identifier 192.0.2.112", "router identifier 192.0.2.113", 1),
		strings.Replace(native, "Total number of neighbors 2", "Total number of neighbors 3", 1),
		strings.Replace(native, "Total num. Established sessions 1", "Total num. Established sessions 0", 1),
		strings.Replace(native, "Idle (PfxCt)", "Idle (some-new-state)", 1),
		strings.Replace(native, "2001:db8::2", "192.0.2.2", 1),
		strings.Replace(native, "2001:db8::2", "2001:db8:1111:2222:3333:4444:5555:1", 1),
		strings.Replace(native, "                4 4200000003", "4 4200000003", 1),
		strings.Replace(native, "Total num. of routes received     0\n", "", 1),
		native + "IPv6 Multicast Summary:\nNo IPv6 neighbor is configured\n",
	} {
		if _, _, err := parseQuaggaSummary([]byte(invalid), local, 64500, true); err == nil {
			t.Fatal("unverified summary accepted")
		}
	}
	rows, present, err = parseQuaggaSummary([]byte("No IPv6 neighbor is configured\n"), local, 64500, true)
	if err != nil || present || len(rows) != 0 {
		t.Fatal("empty address family pretended to prove router identity", err)
	}
}

func TestQuaggaObserveRejectsSocketInodeReplacement(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "bgpd.vty")
	address := &net.UnixAddr{Name: socket, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	hold := make(chan struct{})
	defer close(hold)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			served <- err
			return
		}
		if command, err := bufio.NewReader(conn).ReadString(0); err != nil || command != "show version\x00" {
			served <- errors.New("observer sent another command before verifying version")
			return
		}
		if err := os.Remove(socket); err != nil {
			served <- err
			return
		}
		replacement, err := net.ListenUnix("unix", address)
		if err != nil {
			served <- err
			return
		}
		defer replacement.Close()
		if err := os.Chmod(socket, 0600); err != nil {
			served <- err
			return
		}
		_, err = io.WriteString(conn, "Quagga 1.2.4 (fixture).\nCopyright 1996-2005 Kunihiro Ishiguro, et al.\n\x00\x00\x00\x00")
		served <- err
		<-hold
	}()
	result, err := ObserveQuaggaSessions(context.Background(), QuaggaObservationOptions{Socket: socket, RouterID: "192.0.2.112", RouterASN: 64500}, []string{"192.0.2.1"})
	if serverErr := <-served; serverErr != nil || err == nil || result != nil || !strings.Contains(err.Error(), "changed during observation") {
		t.Fatal("changed private socket inode was accepted", serverErr, err)
	}
}

func TestQuaggaNativeSessionObservation(t *testing.T) {
	l := newQuaggaLifecycle(t)
	kernel := quaggaWireKernel(t)
	a, _ := quaggaLifecycleConfigs(t, false)
	if err := os.Chmod("/scratch/state", 0700); err != nil {
		t.Fatal(err)
	}
	l.install(a)
	l.launch(true)
	opts := QuaggaObservationOptions{Socket: "/scratch/state/bgpd.vty", RouterID: "192.0.2.112", RouterASN: 64500}
	selected := []string{"192.0.2.1", "2001:db8::1", "192.0.2.2", "2001:db8::2", "192.0.2.254"}
	before, err := ObserveQuaggaSessions(context.Background(), opts, selected)
	if err != nil || len(before) != 4 {
		t.Fatal("native configured sessions not scoped to selected peers", err, before)
	}
	for _, row := range before {
		if row.State == "Established" || row.PrefixesKnown {
			t.Fatal("native down session confused with active zero-prefix session", row)
		}
	}
	l.connect(a)
	l.policy(-1, 0, 64511)
	want := map[string]QuaggaObservedSession{}
	for _, ipv6 := range []bool{false, true} {
		for _, index := range []int{1, 2} {
			address := quaggaWireAddress(ipv6, index)
			prefixes := uint64(2 - index)
			want[address] = QuaggaObservedSession{NeighborAddress: address, NeighborASN: uint32(64500 + index), State: "Established", ReceivedPrefixes: prefixes, PrefixesKnown: true}
		}
	}
	actual, err := ObserveQuaggaSessions(context.Background(), opts, selected)
	if err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal("actual two-family VTY counts or peer identities unavailable", err, actual)
	}
	for _, bogus := range []QuaggaObservationOptions{
		{Socket: opts.Socket, RouterID: "192.0.2.113", RouterASN: opts.RouterASN},
		{Socket: opts.Socket, RouterID: opts.RouterID, RouterASN: 64501},
	} {
		if result, err := ObserveQuaggaSessions(context.Background(), bogus, selected); err == nil || result != nil {
			t.Fatal("observations returned for a mismatched route-server identity")
		}
	}
	if _, err := ObserveQuaggaSessions(context.Background(), opts, []string{"192.0.2.1\nshow running-config"}); err == nil {
		t.Fatal("arbitrary VTY command accepted as a peer selector")
	}
	if _, err := ObserveQuaggaSessions(context.Background(), opts, []string{"2001:db8::1", "2001:0db8:0:0:0:0:0:1"}); err == nil {
		t.Fatal("canonical duplicate peer selection accepted")
	}
	l.ceaseAndReap()
	if !reflect.DeepEqual(kernel, quaggaWireKernel(t)) {
		t.Fatal("read-only session observations changed kernel routes")
	}
	t.Log("quagga_read_only_sessions=true same_uid_private_vty=true id_asn_verified=true ipv4_and_ipv6_actual_accepted_counts=true down_prefix_count_unknown=true selected_peers_only=true wrong_target_refused=true no_kernel_change=true")
}
