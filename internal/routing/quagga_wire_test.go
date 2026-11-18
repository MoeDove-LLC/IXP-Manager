package routing

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type quaggaWireRoute struct {
	Path               []uint16
	NextHop, LinkLocal string
}
type quaggaWirePeer struct {
	mu           sync.Mutex
	write        sync.Mutex
	connection   net.Conn
	address      string
	asn          uint16
	v6           bool
	opens, sent  int
	routes       map[string]quaggaWireRoute
	notification []byte
	err          error
	stopping     bool
	done         chan struct{}
}

func quaggaWireGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("QUAGGA_NATIVE_WIRE") != "1" {
		t.Skip("explicit isolated stock Quagga wire fixture required")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if !quaggaWireCapabilities(status) {
		t.Fatal("wire target requires zero capabilities and no_new_privs")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" {
		t.Fatal("fixture refuses anything except its isolated loopback topology", err)
	}
	if len(os.Getenv("QUAGGA_BGPD_BIN")) == 0 {
		t.Fatal("actual Quagga executable required")
	}
}
func quaggaWireCapabilities(status []byte) bool {
	for _, name := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		want := "0000000000000000"
		if name == "NoNewPrivs" {
			want = "1"
		}
		matched := false
		for _, line := range strings.Split(string(status), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == name+":" {
				matched = f[1] == want
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func quaggaWirePrefixes(v6 bool) []string {
	if v6 {
		return []string{"2001:db8:100::/48", "2001:db8:101::/48", "2001:db8:102::/48", "2001:db8:103::/48", "2001:db8:104::/48", "2001:db8:200::/48", "2001:db8:300::/48", "2001:db8:dead::/48"}
	}
	return []string{"198.51.100.0/26", "198.51.100.64/26", "198.51.100.128/26", "198.51.100.192/26", "203.0.113.0/26", "203.0.113.64/26", "203.0.113.128/26", "203.0.113.192/26"}
}
func quaggaWireAddress(v6 bool, last int) string {
	if v6 {
		return fmt.Sprintf("2001:db8::%d", last)
	}
	return fmt.Sprintf("192.0.2.%d", last)
}
func quaggaWireConfig() (string, error) {
	router := Router{ASN: 64500, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112"}
	peers := []Peer{}
	for _, v6 := range []bool{false, true} {
		prefixes := quaggaWirePrefixes(v6)
		for index := 1; index <= 3; index++ {
			asn := uint32(64501)
			if index == 2 {
				asn = 64502
			}
			p := Peer{ID: fmt.Sprintf("wire-%t-%d", v6, index), ASN: asn, Address: quaggaWireAddress(v6, index), Enabled: true, MaxPrefixes: 20}
			if index == 1 {
				p.AllowedPrefixes = prefixes[:5]
				for i, prefix := range p.AllowedPrefixes {
					origin := uint32(64511)
					if i == 1 {
						origin = 64512
					}
					p.AllowedRoutes = append(p.AllowedRoutes, PrefixOrigin{Prefix: prefix, OriginASN: origin})
				}
			} else {
				p.AllowedPrefixes = []string{prefixes[index+3]}
				p.AllowedRoutes = []PrefixOrigin{{Prefix: p.AllowedPrefixes[0], OriginASN: 64511}}
			}
			peers = append(peers, p)
		}
	}
	return RenderQuagga(router, peers)
}
func quaggaWireUpdate(prefix, nextHop string, path []uint16, v6 bool) []byte {
	aspath := []byte{2, byte(len(path))}
	for _, asn := range path {
		aspath = append(aspath, byte(asn>>8), byte(asn))
	}
	attrs := []byte{0x40, 1, 1, 0, 0x40, 2, byte(len(aspath))}
	attrs = append(attrs, aspath...)
	if v6 {
		reach := append([]byte{0, 2, 1, 32}, netip.MustParseAddr(nextHop).AsSlice()...)
		reach = append(reach, netip.MustParseAddr("fe80::1").AsSlice()...)
		reach = append(reach, 0)
		reach = append(reach, as112WireNLRI([]string{prefix})...)
		attrs = append(attrs, 0x80, 14, byte(len(reach)))
		attrs = append(attrs, reach...)
	} else {
		attrs = append(attrs, 0x40, 3, 4)
		attrs = append(attrs, netip.MustParseAddr(nextHop).AsSlice()...)
	}
	body := []byte{0, 0, byte(len(attrs) >> 8), byte(len(attrs))}
	body = append(body, attrs...)
	if !v6 {
		body = append(body, as112WireNLRI([]string{prefix})...)
	}
	return as112WireFrame(2, body)
}

// Attempt to put the authorized global peer address in the secondary slot
// while the actual global next hop remains an unauthorized third party.
func quaggaWireMalformedSecondary(frame []byte, address string) ([]byte, error) {
	copy := append([]byte(nil), frame...)
	if len(copy) < 23 || copy[18] != 2 || binary.BigEndian.Uint16(copy[19:21]) != 0 {
		return nil, errors.New("fixture update header")
	}
	end := 23 + int(binary.BigEndian.Uint16(copy[21:23]))
	if end > len(copy) {
		return nil, errors.New("fixture attribute bound")
	}
	for at := 23; at+3 <= end; {
		flags, kind := copy[at], copy[at+1]
		width, size := 1, int(copy[at+2])
		if flags&0x10 != 0 {
			if at+4 > end {
				return nil, errors.New("fixture extended length")
			}
			width = 2
			size = int(binary.BigEndian.Uint16(copy[at+2 : at+4]))
		}
		value := at + 2 + width
		if value+size > end {
			return nil, errors.New("fixture attribute length")
		}
		if kind == 14 && size >= 37 && copy[value+3] == 32 {
			encoded := netip.MustParseAddr(address).As16()
			for i, b := range encoded {
				copy[value+20+i] = b
			}
			return copy, nil
		}
		at = value + size
	}
	return nil, errors.New("fixture MP_REACH secondary absent")
}
func quaggaWireDecode(body []byte, v6 bool) ([]string, []string, quaggaWireRoute, error) {
	var route quaggaWireRoute
	if len(body) < 4 {
		return nil, nil, route, errors.New("short update")
	}
	withdrawn := int(binary.BigEndian.Uint16(body))
	if withdrawn+4 > len(body) {
		return nil, nil, route, errors.New("withdrawal overflow")
	}
	removed, err := as112WirePrefixes(body[2:2+withdrawn], false)
	if err != nil || v6 && withdrawn != 0 {
		return nil, nil, route, errors.New("invalid classic withdrawals")
	}
	offset := 2 + withdrawn
	size := int(binary.BigEndian.Uint16(body[offset:]))
	offset += 2
	if offset+size > len(body) {
		return nil, nil, route, errors.New("attribute overflow")
	}
	attrs, classic := body[offset:offset+size], body[offset+size:]
	if v6 && len(classic) > 0 {
		return nil, nil, route, errors.New("classic NLRI in IPv6 session")
	}
	added, err := as112WirePrefixes(classic, false)
	if err != nil {
		return nil, nil, route, err
	}
	seen := map[byte]bool{}
	origin := false
	for len(attrs) > 0 {
		if len(attrs) < 3 || seen[attrs[1]] {
			return nil, nil, route, errors.New("invalid repeated attribute")
		}
		flags, kind := attrs[0], attrs[1]
		seen[kind] = true
		width := 1
		if flags&0x10 != 0 {
			width = 2
		}
		if len(attrs) < 2+width {
			return nil, nil, route, errors.New("short attribute length")
		}
		length := int(attrs[2])
		if width == 2 {
			length = int(binary.BigEndian.Uint16(attrs[2:]))
		}
		if 2+width+length > len(attrs) {
			return nil, nil, route, errors.New("attribute bounds")
		}
		value := attrs[2+width : 2+width+length]
		attrs = attrs[2+width+length:]
		switch kind {
		case 1:
			origin = bytes.Equal(value, []byte{0})
		case 2:
			if len(value) < 2 || value[0] != 2 || int(value[1])*2+2 != len(value) || value[1] == 0 {
				return nil, nil, route, errors.New("unexpected AS path encoding")
			}
			for at := 2; at < len(value); at += 2 {
				route.Path = append(route.Path, binary.BigEndian.Uint16(value[at:]))
			}
		case 3:
			if v6 || len(value) != 4 {
				return nil, nil, route, errors.New("bad v4 next hop")
			}
			var address [4]byte
			copy(address[:], value)
			route.NextHop = netip.AddrFrom4(address).String()
		case 14:
			if !v6 || len(value) < 21 || !bytes.Equal(value[:3], []byte{0, 2, 1}) || value[3] != 16 && value[3] != 32 || len(value) < 5+int(value[3]) {
				return nil, nil, route, errors.New("bad MP_REACH")
			}
			var address [16]byte
			copy(address[:], value[4:20])
			route.NextHop = netip.AddrFrom16(address).String()
			if value[3] == 32 {
				copy(address[:], value[20:36])
				route.LinkLocal = netip.AddrFrom16(address).String()
			}
			added, err = as112WirePrefixes(value[5+int(value[3]):], true)
			if err != nil {
				return nil, nil, route, err
			}
		case 15:
			if !v6 || len(value) < 3 || !bytes.Equal(value[:3], []byte{0, 2, 1}) {
				return nil, nil, route, errors.New("bad MP_UNREACH")
			}
			removed, err = as112WirePrefixes(value[3:], true)
			if err != nil {
				return nil, nil, route, err
			}
		}
	}
	if len(added) > 0 && (!origin || len(route.Path) == 0 || route.NextHop == "") {
		return nil, nil, route, errors.New("missing transparent route attributes")
	}
	return added, removed, route, nil
}
func (p *quaggaWirePeer) send(frame []byte) error {
	p.write.Lock()
	defer p.write.Unlock()
	_ = p.connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return as112WireWrite(p.connection, frame)
}
func (p *quaggaWirePeer) stop() {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	_ = p.connection.Close()
	<-p.done
}
func (p *quaggaWirePeer) state() (map[string]quaggaWireRoute, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := map[string]quaggaWireRoute{}
	for k, v := range p.routes {
		copy[k] = v
	}
	return copy, append([]byte(nil), p.notification...), p.err
}
func quaggaWireConnect(address string, asn uint16, v6 bool) (*quaggaWirePeer, error) {
	network := "tcp4"
	if v6 {
		network = "tcp6"
	}
	dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(address)}}
	connection, err := dialer.Dial(network, net.JoinHostPort(quaggaWireAddress(v6, 112), "1179"))
	if err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	def := as112WireDefinition{address: address, asn: asn, v6: v6}
	open := as112WireOpen(def)
	ip := netip.MustParseAddr(address).AsSlice()
	last := ip[len(ip)-1]
	if v6 {
		last += 10
	}
	copy(open[24:28], []byte{192, 0, 2, last})
	if err = as112WireWrite(connection, open); err != nil {
		connection.Close()
		return nil, err
	}
	kind, body, err := as112WireRead(connection)
	if err != nil || kind != 1 || len(body) < 10 || body[0] != 4 || binary.BigEndian.Uint16(body[1:3]) != 64500 || !bytes.Equal(body[5:9], []byte{192, 0, 2, 112}) {
		connection.Close()
		return nil, fmt.Errorf("native OPEN identity unavailable: type %d error %v", kind, err)
	}
	if err = as112WireWrite(connection, as112WireFrame(4, nil)); err != nil {
		connection.Close()
		return nil, err
	}
	kind, _, err = as112WireRead(connection)
	if err != nil || kind != 4 {
		connection.Close()
		return nil, fmt.Errorf("native KEEPALIVE unavailable: type %d error %v", kind, err)
	}
	_ = connection.SetDeadline(time.Now().Add(80 * time.Second))
	p := &quaggaWirePeer{connection: connection, address: address, asn: asn, v6: v6, opens: 1, routes: map[string]quaggaWireRoute{}, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer connection.Close()
		// These peers never advertise GR. Session loss removes their learned
		// routes; a retained test map must not masquerade as native GR.
		defer func() { p.mu.Lock(); p.routes = map[string]quaggaWireRoute{}; p.mu.Unlock() }()
		for count := 0; count < 256; count++ {
			kind, body, err := as112WireRead(connection)
			p.mu.Lock()
			if err != nil {
				if !p.stopping && len(p.notification) == 0 {
					p.err = err
				}
				p.mu.Unlock()
				return
			}
			if kind == 4 {
				p.mu.Unlock()
				continue
			}
			if kind == 3 {
				p.notification = append([]byte(nil), body...)
				p.mu.Unlock()
				return
			}
			if kind != 2 {
				p.err = fmt.Errorf("unexpected native message %d", kind)
				p.mu.Unlock()
				return
			}
			added, removed, route, err := quaggaWireDecode(body, v6)
			if err != nil {
				p.err = err
				p.mu.Unlock()
				return
			}
			for _, prefix := range removed {
				delete(p.routes, prefix)
			}
			for _, prefix := range added {
				p.routes[prefix] = route
			}
			p.mu.Unlock()
		}
		p.mu.Lock()
		p.err = errors.New("wire message bound exceeded")
		p.mu.Unlock()
	}()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				_ = p.send(as112WireFrame(4, nil))
			}
		}
	}()
	return p, nil
}
func quaggaWireVTY(command string) (string, error) {
	connection, err := net.DialTimeout("unix", "/scratch/state/bgpd.vty", time.Second)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if err := as112WireWrite(connection, append([]byte(command), 0)); err != nil {
		return "", err
	}
	var raw []byte
	buffer := make([]byte, 4096)
	for len(raw) < 1<<20 {
		n, err := connection.Read(buffer)
		if err != nil {
			tail := raw
			if len(tail) > 16 {
				tail = tail[len(tail)-16:]
			}
			return string(raw), fmt.Errorf("%w (partial bytes %d, tail %x)", err, len(raw), tail)
		}
		raw = append(raw, buffer[:n]...)
		if len(raw) >= 4 && bytes.Equal(raw[len(raw)-4:len(raw)-1], []byte{0, 0, 0}) {
			if raw[len(raw)-1] != 0 {
				return "", errors.New("read-only VTY command rejected")
			}
			return string(raw[:len(raw)-4]), nil
		}
	}
	return "", errors.New("VTY output bound")
}
func quaggaWireKernel(t *testing.T) []string {
	t.Helper()
	out := []string{}
	for _, file := range []string{"/proc/net/route", "/proc/net/ipv6_route"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if file == "/proc/net/route" && len(f) == 11 && f[0] != "Iface" {
				out = append(out, strings.Join([]string{f[0], f[1], f[2], f[3], f[6], f[7]}, " "))
			}
			if file == "/proc/net/ipv6_route" && len(f) == 10 {
				out = append(out, strings.Join([]string{f[0], f[1], f[2], f[3], f[4], f[5], f[8], f[9]}, " "))
			}
		}
	}
	sort.Strings(out)
	return out
}
func TestQuaggaNativeWire(t *testing.T) {
	quaggaWireGuard(t)
	beforeKernel := quaggaWireKernel(t)
	config, err := quaggaWireConfig()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "quagga.conf")
	if err := os.WriteFile(file, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Getenv("QUAGGA_BGPD_BIN"), "--skip_runas", "--no_kernel", "--bgp_port", "1179", "--vty_port", "0", "--socket", filepath.Join(dir, "unused-zebra.sock"), "--pid_file", filepath.Join(dir, "bgpd.pid"), "--config_file", file)
	command.Dir = dir
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log := &as112WireLog{}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	var exited atomic.Bool
	go func() { done <- command.Wait(); exited.Store(true); close(done) }()
	defer func() {
		if !exited.Load() {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat("/scratch/state/bgpd.vty"); err == nil {
			break
		}
		if exited.Load() || time.Now().After(deadline) {
			t.Fatalf("native startup failed: %s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	nativeStatus, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", command.Process.Pid))
	if err != nil || !quaggaWireCapabilities(nativeStatus) {
		t.Fatal("stock daemon did not retain capability-zero/no_new_privs confinement", err)
	}
	peers := map[string]*quaggaWirePeer{}
	defer func() {
		for _, peer := range peers {
			peer.stop()
		}
	}()
	for _, v6 := range []bool{false, true} {
		for index := 1; index <= 3; index++ {
			asn := uint16(64501)
			if index == 2 {
				asn = 64502
			}
			address := quaggaWireAddress(v6, index)
			for deadline := time.Now().Add(12 * time.Second); ; {
				state, readErr := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + address)
				if readErr != nil {
					// Stock zclient_start synchronously retries the absent Zebra
					// socket five times at startup. A VTY inode is not readiness.
					if time.Now().Before(deadline) {
						time.Sleep(40 * time.Millisecond)
						continue
					}
					t.Fatal("native readiness read failed", readErr, state, quaggaWireDiagnostic(command.Process.Pid))
				}
				if strings.Contains(state, "BGP state = Active") || strings.Contains(state, "BGP state = Connect") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native peer never left startup Idle", state)
				}
				time.Sleep(40 * time.Millisecond)
			}
			peer, err := quaggaWireConnect(address, asn, v6)
			if err != nil {
				detail, _ := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + address)
				t.Fatalf("peer %s failed: %v; %s; %s; %s", address, err, log.String(), detail, quaggaWireDiagnostic(command.Process.Pid))
			}
			peers[address] = peer
		}
	}
	send := func(v6 bool, index int, prefix, nh string, path []uint16) {
		t.Helper()
		peer := peers[quaggaWireAddress(v6, index)]
		if err := peer.send(quaggaWireUpdate(prefix, nh, path, v6)); err != nil {
			t.Fatal(err)
		}
		peer.sent++
	}
	for _, v6 := range []bool{false, true} {
		p := quaggaWirePrefixes(v6)
		send(v6, 1, p[0], quaggaWireAddress(v6, 1), []uint16{64501, 64511})
	}
	for deadline := time.Now().Add(35 * time.Second); ; {
		ready := true
		for _, v6 := range []bool{false, true} {
			routes, notification, err := peers[quaggaWireAddress(v6, 2)].state()
			if err != nil || len(notification) > 0 {
				t.Fatal("observer failed", err, notification)
			}
			prefix := quaggaWirePrefixes(v6)[0]
			r, ok := routes[prefix]
			if !ok {
				ready = false
				continue
			}
			if len(routes) != 1 || !reflect.DeepEqual(r.Path, []uint16{64501, 64511}) || r.NextHop != quaggaWireAddress(v6, 1) || v6 && r.LinkLocal != "fe80::1" {
				t.Fatal("wire transparency or exact prefix failed", routes)
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("positive wire route not observed; %s", log.String())
		}
		time.Sleep(40 * time.Millisecond)
	}
	for _, v6 := range []bool{false, true} {
		p := quaggaWirePrefixes(v6)
		own := quaggaWireAddress(v6, 1)
		send(v6, 3, p[1], quaggaWireAddress(v6, 3), []uint16{64501, 64512})
		send(v6, 1, p[2], own, []uint16{64501, 64512})
		send(v6, 1, p[3], quaggaWireAddress(v6, 3), []uint16{64501, 64511})
		send(v6, 1, p[7], own, []uint16{64501, 64511})
		sub := "198.51.100.0/27"
		if v6 {
			sub = "2001:db8:100::/64"
		}
		send(v6, 1, sub, own, []uint16{64501, 64511})
		if v6 {
			bad, err := quaggaWireMalformedSecondary(quaggaWireUpdate(p[3], quaggaWireAddress(true, 3), []uint16{64501, 64511}, true), own)
			if err != nil {
				t.Fatal(err)
			}
			peer := peers[own]
			if err := peer.send(bad); err != nil {
				t.Fatal(err)
			}
			peer.sent++
		}
	}
	updates := regexp.MustCompile(`(?m)^\s+Updates:\s+([0-9]+)\s+([0-9]+)\s*$`)
	for deadline := time.Now().Add(8 * time.Second); ; {
		ready := true
		for _, peer := range peers {
			if peer.sent == 0 {
				continue
			}
			text, err := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + peer.address)
			if err != nil {
				t.Fatal(err)
			}
			match := updates.FindStringSubmatch(text)
			if match == nil {
				t.Fatal("native update counter missing", text)
			}
			count, _ := strconv.Atoi(match[2])
			if count < peer.sent {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native did not confirm processing every negative update")
		}
		time.Sleep(30 * time.Millisecond)
	}
	for _, v6 := range []bool{false, true} {
		cmd := "show ip bgp view MOEDOVE_QUAGGA rsclient "
		if v6 {
			cmd = "show bgp view MOEDOVE_QUAGGA ipv6 rsclient "
		}
		rib, err := quaggaWireVTY(cmd + quaggaWireAddress(v6, 2))
		if err != nil {
			t.Fatal(err)
		}
		p := quaggaWirePrefixes(v6)
		observed := map[string]bool{}
		for _, match := range regexp.MustCompile(`[0-9a-fA-F:.]+/[0-9]{1,3}`).FindAllString(rib, -1) {
			if prefix, err := netip.ParsePrefix(match); err == nil {
				observed[prefix.String()] = true
			}
		}
		if len(observed) != 1 || !observed[p[0]] {
			t.Fatal("RS-client RIB contained missing or unapproved prefixes", observed, rib)
		}
		if !strings.Contains(rib, p[0]) {
			t.Fatal("positive missing from native RS-client RIB", rib)
		}
		for _, forbidden := range p[1:] {
			if strings.Contains(rib, forbidden) {
				t.Fatal("unapproved route reached RS-client RIB", forbidden, rib)
			}
		}
		routes, notification, err := peers[quaggaWireAddress(v6, 2)].state()
		if err != nil || len(notification) > 0 || len(routes) != 1 {
			t.Fatal("unapproved wire route or receiver failure", routes, notification, err)
		}
	}
	// Wrong first-AS is a semantic malformed-AS_PATH, not a fabricated local
	// denial. The real daemon must return its UPDATE/Malformed-AS-PATH notify.
	for _, v6 := range []bool{false, true} {
		send(v6, 1, quaggaWirePrefixes(v6)[4], quaggaWireAddress(v6, 1), []uint16{64599, 64511})
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		ready := true
		for _, v6 := range []bool{false, true} {
			_, notification, err := peers[quaggaWireAddress(v6, 1)].state()
			if len(notification) == 0 {
				if err != nil {
					t.Fatal("sender closed without native NOTIFICATION", err)
				}
				ready = false
			} else if len(notification) < 2 || notification[0] != 3 || notification[1] != 11 {
				t.Fatal("unexpected native first-AS rejection", notification)
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native malformed-AS-PATH notification absent")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after := quaggaWireKernel(t); !reflect.DeepEqual(beforeKernel, after) {
		t.Fatal("Quagga changed kernel routes", beforeKernel, after)
	}
	t.Log("stock_quagga_1_2_4=true actual_dual_stack_bgp=true six_peers=true transparent_as_path_next_hop=true ipv6_link_local_preserved=true exact_prefix_origin_source_next_hop_filters=true same_asn_wrong_source_rejected=true malformed_ipv6_secondary_cannot_substitute_global=true negative_updates_confirmed_processed=true rsclient_rib_checked=true first_as_notification_3_11=true kernel_routes_unchanged=true target_caps_zero=true no_new_privs=true")
}

func quaggaWireDiagnostic(pid int) string {
	var output strings.Builder
	for _, name := range []string{"wchan", "stat", "syscall"} {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/%s", pid, name))
		fmt.Fprintf(&output, "%s: %s (%v)\n", name, strings.TrimSpace(string(raw)), err)
	}
	raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "State:") || strings.HasPrefix(line, "Cap") || strings.HasPrefix(line, "NoNewPrivs:") || strings.HasPrefix(line, "VmRSS:") {
			output.WriteString(line + "\n")
		}
	}
	entries, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	for i, entry := range entries {
		if i >= 16 {
			break
		}
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
		fmt.Fprintf(&output, "fd %s: %s (%v)\n", entry.Name(), target, err)
	}
	return output.String()
}
