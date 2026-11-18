package routing

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const as112WireSocket = "/scratch/as112-wire-peer.sock"

type as112WireDefinition struct {
	id, address string
	asn         uint16
	v6, sender  bool
}

var as112WireDefinitions = []as112WireDefinition{
	{"sender4", "192.0.2.1", 64501, false, true},
	{"observer4", "192.0.2.2", 64502, false, false},
	{"sender6", "2001:db8::1", 64501, true, true},
	{"observer6", "2001:db8::2", 64502, true, false},
}

func as112WireOrigins(v6 bool) []string {
	if v6 {
		return []string{"2620:4f:8000::/48", "2001:4:112::/48"}
	}
	return []string{"192.175.48.0/24", "192.31.196.0/24"}
}
func as112WireSource(v6 bool) string {
	if v6 {
		return "2001:db8::112"
	}
	return "192.0.2.112"
}
func requireAS112WireSandbox(t *testing.T, peer bool) {
	t.Helper()
	if os.Getenv("AS112_NATIVE_WIRE") != "1" {
		t.Skip("explicit private veth namespace wire fixture required")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		want := "0000000000000000"
		if field == "NoNewPrivs" {
			want = "1"
		}
		found := false
		for _, line := range strings.Split(string(status), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field+":" {
				found = parts[1] == want
			}
		}
		if !found {
			t.Fatal("wire target requires zero capabilities and no_new_privs")
		}
	}
	want := "node0"
	if peer {
		want = "peer0"
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, iface := range interfaces {
		if iface.Name == want {
			found = true
		} else if iface.Name != "lo" {
			t.Fatal("wire fixture refuses unexpected interfaces")
		}
	}
	if !found {
		t.Fatal("trusted bootstrap did not provide the private veth topology")
	}
}

func as112WireFrame(kind byte, body []byte) []byte {
	packet := append(bytes.Repeat([]byte{255}, 16), 0, 0, kind)
	binary.BigEndian.PutUint16(packet[16:18], uint16(19+len(body)))
	return append(packet, body...)
}
func as112WireRead(connection net.Conn) (byte, []byte, error) {
	header := make([]byte, 19)
	if _, err := io.ReadFull(connection, header); err != nil {
		return 0, nil, err
	}
	size := int(binary.BigEndian.Uint16(header[16:18]))
	if !bytes.Equal(header[:16], bytes.Repeat([]byte{255}, 16)) || size < 19 || size > 4096 {
		return 0, nil, errors.New("invalid bounded BGP frame")
	}
	body := make([]byte, size-19)
	_, err := io.ReadFull(connection, body)
	return header[18], body, err
}
func as112WireOpen(d as112WireDefinition) []byte {
	body := []byte{4, 0, 0, 0, 90, 192, 0, 2, byte(d.asn % 255), 0}
	binary.BigEndian.PutUint16(body[1:3], d.asn)
	if d.v6 {
		// MP IPv6 only. No AS4, ADD-PATH, graceful restart or route refresh.
		body[9] = 8
		body = append(body, 2, 6, 1, 4, 0, 2, 0, 1)
	}
	return as112WireFrame(1, body)
}
func as112WireCheckOpen(body []byte, v6 bool) error {
	if len(body) < 10 || body[0] != 4 || binary.BigEndian.Uint16(body[1:3]) != 112 || !bytes.Equal(body[5:9], []byte{192, 0, 2, 112}) || len(body) != 10+int(body[9]) {
		return errors.New("unexpected AS112 OPEN identity")
	}
	if hold := binary.BigEndian.Uint16(body[3:5]); hold > 0 && hold < 3 {
		return errors.New("invalid OPEN hold time")
	}
	mp6 := false
	for at := 10; at < len(body); {
		if at+2 > len(body) || body[at] != 2 || at+2+int(body[at+1]) > len(body) {
			return errors.New("invalid OPEN parameter")
		}
		end := at + 2 + int(body[at+1])
		at += 2
		for at < end {
			if at+2 > end || at+2+int(body[at+1]) > end {
				return errors.New("invalid OPEN capability")
			}
			code, size := body[at], int(body[at+1])
			value := body[at+2 : at+2+size]
			if code == 64 || code == 71 {
				return errors.New("AS112 must not advertise GR or LLGR retention")
			}
			if code == 1 && size == 4 && bytes.Equal(value, []byte{0, 2, 0, 1}) {
				mp6 = true
			}
			at += 2 + size
		}
	}
	if v6 && !mp6 {
		return errors.New("IPv6 MP capability not advertised")
	}
	return nil
}
func as112WireNLRI(prefixes []string) []byte {
	var out []byte
	for _, text := range prefixes {
		prefix := netip.MustParsePrefix(text)
		out = append(out, byte(prefix.Bits()))
		out = append(out, prefix.Addr().AsSlice()[:(prefix.Bits()+7)/8]...)
	}
	return out
}
func as112WirePrefixes(raw []byte, v6 bool) ([]string, error) {
	var out []string
	limit := 32
	if v6 {
		limit = 128
	}
	for len(raw) > 0 {
		bits := int(raw[0])
		raw = raw[1:]
		size := (bits + 7) / 8
		if bits > limit || size > len(raw) || len(out) >= 32 || bits%8 != 0 && raw[size-1]&byte((1<<(8-bits%8))-1) != 0 {
			return nil, errors.New("invalid NLRI")
		}
		var address netip.Addr
		if v6 {
			var value [16]byte
			copy(value[:], raw[:size])
			address = netip.AddrFrom16(value)
		} else {
			var value [4]byte
			copy(value[:], raw[:size])
			address = netip.AddrFrom4(value)
		}
		out = append(out, netip.PrefixFrom(address, bits).String())
		raw = raw[size:]
	}
	return out, nil
}
func as112WireMalicious(d as112WireDefinition) []byte {
	attrs := []byte{0x40, 1, 1, 0, 0x40, 2, 4, 2, 1, 0, 0}
	binary.BigEndian.PutUint16(attrs[len(attrs)-2:], d.asn)
	prefixes := []string{"198.51.100.0/24", "192.175.48.0/24"}
	if d.v6 {
		prefixes = []string{"2001:db8:dead::/48", "2620:4f:8000::/48"}
		reach := append([]byte{0, 2, 1, 16}, netip.MustParseAddr(d.address).AsSlice()...)
		reach = append(reach, 0)
		reach = append(reach, as112WireNLRI(prefixes)...)
		attrs = append(attrs, 0x80, 14, byte(len(reach)))
		attrs = append(attrs, reach...)
	} else {
		attrs = append(attrs, 0x40, 3, 4)
		attrs = append(attrs, netip.MustParseAddr(d.address).AsSlice()...)
	}
	body := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint16(body[2:4], uint16(len(attrs)))
	body = append(body, attrs...)
	if !d.v6 {
		body = append(body, as112WireNLRI(prefixes)...)
	}
	return as112WireFrame(2, body)
}
func as112WireUpdate(body []byte, v6 bool) ([]string, []uint16, error) {
	if len(body) < 4 {
		return nil, nil, errors.New("short UPDATE")
	}
	withdrawn := int(binary.BigEndian.Uint16(body[:2]))
	if withdrawn != 0 || withdrawn+4 > len(body) {
		return nil, nil, errors.New("unexpected withdrawal")
	}
	length := int(binary.BigEndian.Uint16(body[2:4]))
	if length+4 > len(body) {
		return nil, nil, errors.New("invalid UPDATE attribute bound")
	}
	attrs, classic := body[4:4+length], body[4+length:]
	if v6 && len(classic) != 0 {
		return nil, nil, errors.New("IPv6 session carried classic NLRI")
	}
	prefixes, err := as112WirePrefixes(classic, false)
	if err != nil {
		return nil, nil, err
	}
	var path []uint16
	origin, nextHop := false, false
	seen := map[byte]bool{}
	for len(attrs) > 0 {
		if len(attrs) < 3 || attrs[0]&15 != 0 || seen[attrs[1]] {
			return nil, nil, errors.New("invalid or repeated path attribute")
		}
		flags, code := attrs[0], attrs[1]
		seen[code] = true
		width := 1
		if flags&0x10 != 0 {
			width = 2
		}
		if len(attrs) < 2+width {
			return nil, nil, errors.New("truncated attribute length")
		}
		n := int(attrs[2])
		if width == 2 {
			n = int(binary.BigEndian.Uint16(attrs[2:4]))
		}
		if 2+width+n > len(attrs) {
			return nil, nil, errors.New("truncated attribute")
		}
		value := attrs[2+width : 2+width+n]
		attrs = attrs[2+width+n:]
		switch code {
		case 1:
			origin = flags&0xe0 == 0x40 && bytes.Equal(value, []byte{0})
		case 2:
			if flags&0xe0 != 0x40 || len(value) != 4 || value[0] != 2 || value[1] != 1 {
				return nil, nil, errors.New("AS_PATH is not a single AS_SEQUENCE")
			}
			path = []uint16{binary.BigEndian.Uint16(value[2:4])}
		case 3:
			nextHop = !v6 && flags&0xe0 == 0x40 && bytes.Equal(value, netip.MustParseAddr(as112WireSource(false)).AsSlice())
		case 14:
			if !v6 || flags&0xe0 != 0x80 || len(value) < 5 || !bytes.Equal(value[:3], []byte{0, 2, 1}) || value[3] != 16 && value[3] != 32 || len(value) < 5+int(value[3]) {
				return nil, nil, errors.New("invalid MP_REACH")
			}
			nextHop = bytes.Equal(value[4:20], netip.MustParseAddr(as112WireSource(true)).AsSlice())
			if value[3] == 32 {
				nextHop = nextHop && bytes.Equal(value[20:36], netip.MustParseAddr("fe80::112").AsSlice())
			}
			prefixes, err = as112WirePrefixes(value[5+int(value[3]):], true)
			if err != nil {
				return nil, nil, err
			}
		case 15:
			if !v6 || flags&0xe0 != 0x80 || !bytes.Equal(value, []byte{0, 2, 1}) {
				return nil, nil, errors.New("unexpected MP withdrawal")
			}
		default:
			if flags&0x80 == 0 && code != 5 && code != 6 {
				return nil, nil, errors.New("unknown well-known attribute")
			}
		}
	}
	if len(prefixes) > 0 && (!origin || !nextHop || len(path) != 1 || path[0] != 112) {
		return nil, nil, errors.New("wire origin/path/next-hop differs from local AS112")
	}
	return prefixes, path, nil
}

type as112WirePeer struct {
	definition as112WireDefinition
	listener   net.Listener
	connection net.Conn
	connected  bool
	closed     bool
	injected   bool
	routes     map[string][]uint16
	err        string
}
type as112WirePeerView struct {
	ID        string              `json:"id"`
	Connected bool                `json:"connected"`
	Closed    bool                `json:"closed"`
	Injected  bool                `json:"injected"`
	Routes    map[string][]uint16 `json:"routes"`
	Error     string              `json:"error"`
}
type as112WireRun struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	peers    []*as112WirePeer
	stopping bool
}

func (r *as112WireRun) view() []as112WirePeerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []as112WirePeerView{}
	for _, peer := range r.peers {
		routes := map[string][]uint16{}
		for prefix, path := range peer.routes {
			routes[prefix] = append([]uint16{}, path...)
		}
		out = append(out, as112WirePeerView{ID: peer.definition.id, Connected: peer.connected, Closed: peer.closed, Injected: peer.injected, Routes: routes, Error: peer.err})
	}
	return out
}
func (r *as112WireRun) stop() {
	r.mu.Lock()
	r.stopping = true
	for _, peer := range r.peers {
		_ = peer.listener.Close()
		if peer.connection != nil {
			_ = peer.connection.Close()
		}
	}
	r.mu.Unlock()
	r.wg.Wait()
}
func as112WireWrite(connection net.Conn, frame []byte) error {
	for len(frame) > 0 {
		n, err := connection.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}
func (r *as112WireRun) serve(peer *as112WirePeer) {
	defer r.wg.Done()
	var failure error
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if failure != nil && !r.stopping {
			peer.err = failure.Error()
		}
	}()
	connection, err := peer.listener.Accept()
	if err != nil {
		failure = err
		return
	}
	defer func() {
		_ = connection.Close()
		r.mu.Lock()
		peer.closed = true
		peer.connected = false
		// This fixture never negotiates GR/LLGR. A terminated TCP session
		// therefore removes its routes from the receiving peer's RIB.
		peer.routes = map[string][]uint16{}
		r.mu.Unlock()
	}()
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return
	}
	peer.connection = connection
	r.mu.Unlock()
	remote, _, _ := net.SplitHostPort(connection.RemoteAddr().String())
	if remote != as112WireSource(peer.definition.v6) {
		failure = errors.New("unexpected native TCP source address")
		return
	}
	_ = connection.SetDeadline(time.Now().Add(60 * time.Second))
	kind, body, err := as112WireRead(connection)
	if err != nil || kind != 1 {
		failure = errors.New("native OPEN unavailable")
		return
	}
	if failure = as112WireCheckOpen(body, peer.definition.v6); failure != nil {
		return
	}
	if failure = as112WireWrite(connection, as112WireOpen(peer.definition)); failure != nil {
		return
	}
	if failure = as112WireWrite(connection, as112WireFrame(4, nil)); failure != nil {
		return
	}
	for count := 0; count < 1024; count++ {
		kind, body, err = as112WireRead(connection)
		if err != nil {
			failure = err
			return
		}
		if kind == 4 && len(body) == 0 {
			r.mu.Lock()
			peer.connected = true
			r.mu.Unlock()
			continue
		}
		if kind != 2 {
			failure = fmt.Errorf("unexpected native BGP message type %d", kind)
			return
		}
		r.mu.Lock()
		established := peer.connected
		r.mu.Unlock()
		if !established {
			failure = errors.New("native UPDATE preceded its KEEPALIVE")
			return
		}
		prefixes, path, err := as112WireUpdate(body, peer.definition.v6)
		if err != nil {
			failure = err
			return
		}
		r.mu.Lock()
		for _, prefix := range prefixes {
			allowed := false
			for _, expected := range as112WireOrigins(peer.definition.v6) {
				allowed = allowed || prefix == expected
			}
			if !allowed {
				failure = errors.New("native exported a non-service/transit prefix")
				break
			}
			peer.routes[prefix] = path
		}
		r.mu.Unlock()
		if failure != nil {
			return
		}
	}
	failure = errors.New("wire message bound exceeded")
}
func newAS112WireRun() (*as112WireRun, error) {
	run := &as112WireRun{}
	for _, definition := range as112WireDefinitions {
		network := "tcp4"
		if definition.v6 {
			network = "tcp6"
		}
		listener, err := net.Listen(network, net.JoinHostPort(definition.address, "179"))
		if err != nil {
			run.stop()
			return nil, err
		}
		run.peers = append(run.peers, &as112WirePeer{definition: definition, listener: listener, routes: map[string][]uint16{}})
	}
	for _, peer := range run.peers {
		run.wg.Add(1)
		go run.serve(peer)
	}
	return run, nil
}

type as112WireReply struct {
	Peers []as112WirePeerView `json:"peers"`
	Error string              `json:"error,omitempty"`
}

// The trusted bootstrap starts this helper in peer0's private namespace only
// after dropping every capability. The same read-only test binary is used on
// both sides of the veth; IPC accepts no addresses, commands or packet bytes.
func TestAS112NativeBGPWirePeer(t *testing.T) {
	if os.Getenv("AS112_WIRE_PEER") != "1" {
		t.Skip("peer helper process only")
	}
	requireAS112WireSandbox(t, true)
	listener, err := net.Listen("unix", as112WireSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(as112WireSocket)
	if err = os.Chmod(as112WireSocket, 0600); err != nil {
		t.Fatal(err)
	}
	var run *as112WireRun
	defer func() {
		if run != nil {
			run.stop()
		}
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		var request struct {
			Action string `json:"action"`
		}
		decoder := json.NewDecoder(io.LimitReader(connection, 512))
		decoder.DisallowUnknownFields()
		reply := as112WireReply{}
		shutdown := false
		if err = decoder.Decode(&request); err != nil {
			reply.Error = "invalid control request"
		} else {
			switch request.Action {
			case "start":
				if run != nil {
					run.stop()
				}
				run, err = newAS112WireRun()
				if err != nil {
					reply.Error = err.Error()
				}
			case "state":
			case "inject":
				if run == nil {
					reply.Error = "no active fixture"
					break
				}
				run.mu.Lock()
				for _, peer := range run.peers {
					if !peer.definition.sender {
						continue
					}
					if !peer.connected || peer.injected || peer.err != "" {
						reply.Error = "sender not ready for its one bounded update"
						break
					}
					_ = peer.connection.SetWriteDeadline(time.Now().Add(2 * time.Second))
					if err = as112WireWrite(peer.connection, as112WireMalicious(peer.definition)); err != nil {
						reply.Error = err.Error()
						break
					}
					peer.injected = true
				}
				run.mu.Unlock()
			case "stop", "shutdown":
				if run != nil {
					run.stop()
					run = nil
				}
				shutdown = request.Action == "shutdown"
			default:
				reply.Error = "unapproved control action"
			}
		}
		if run != nil {
			reply.Peers = run.view()
		}
		_ = json.NewEncoder(connection).Encode(reply)
		_ = connection.Close()
		if shutdown {
			return
		}
	}
}

func as112WireControl(action string) (as112WireReply, error) {
	var reply as112WireReply
	connection, err := net.DialTimeout("unix", as112WireSocket, time.Second)
	if err != nil {
		return reply, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err = json.NewEncoder(connection).Encode(map[string]string{"action": action}); err != nil {
		return reply, err
	}
	if err = json.NewDecoder(io.LimitReader(connection, 64<<10)).Decode(&reply); err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}
func as112WireReady(reply as112WireReply) error {
	if len(reply.Peers) != 4 {
		return errors.New("four wire peers not present")
	}
	for i, peer := range reply.Peers {
		definition := as112WireDefinitions[i]
		if peer.ID != definition.id || !peer.Connected || peer.Error != "" || len(peer.Routes) != 2 {
			return fmt.Errorf("peer %s not established with exactly two origins: connected=%v routes=%d error=%s", peer.ID, peer.Connected, len(peer.Routes), peer.Error)
		}
		for _, prefix := range as112WireOrigins(definition.v6) {
			path := peer.Routes[prefix]
			if len(path) != 1 || path[0] != 112 {
				return errors.New("wire AS112 origin/path mismatch")
			}
		}
	}
	return nil
}

type as112WireLog struct {
	mu   sync.Mutex
	data []byte
}

func (l *as112WireLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	if remaining := (64 << 10) - len(l.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		l.data = append(l.data, p...)
	}
	return n, nil
}
func (l *as112WireLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.data)
}
func as112WireVTY(socket, command string) (map[string]any, error) {
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if err = as112WireWrite(connection, append([]byte(command), 0)); err != nil {
		return nil, err
	}
	raw := []byte{}
	buffer := make([]byte, 4096)
	for len(raw) < 1<<20 {
		n, err := connection.Read(buffer)
		if err != nil {
			return nil, err
		}
		raw = append(raw, buffer[:n]...)
		if len(raw) >= 4 && bytes.Equal(raw[len(raw)-4:len(raw)-1], []byte{0, 0, 0}) {
			if raw[len(raw)-1] != 0 {
				return nil, errors.New("native VTY command rejected")
			}
			var value map[string]any
			if err := json.Unmarshal(raw[:len(raw)-4], &value); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	return nil, errors.New("native VTY output exceeded bound")
}
func as112WireCounter(value any) float64 {
	switch typed := value.(type) {
	case map[string]any:
		if count, ok := typed["updatesRecv"].(float64); ok {
			return count
		}
		for _, entry := range typed {
			if count := as112WireCounter(entry); count >= 0 {
				return count
			}
		}
	case []any:
		for _, entry := range typed {
			if count := as112WireCounter(entry); count >= 0 {
				return count
			}
		}
	}
	return -1
}
func as112WireRIB(platform, binary, socket string) error {
	for _, sender := range []as112WireDefinition{as112WireDefinitions[0], as112WireDefinitions[2]} {
		family := "ipv4"
		if sender.v6 {
			family = "ipv6"
		}
		if platform == "frr" {
			neighbor, err := as112WireVTY(socket, "show bgp view MOEDOVE neighbors "+sender.address+" json")
			if err != nil || as112WireCounter(neighbor) < 1 {
				return errors.New("FRR has not processed the actual sender UPDATE")
			}
			data, err := as112WireVTY(socket, "show bgp view MOEDOVE "+family+" unicast json")
			if err != nil {
				return err
			}
			routes, ok := data["routes"].(map[string]any)
			if !ok || len(routes) != 2 {
				return errors.New("FRR RIB contains missing or unexpected prefixes")
			}
			for _, prefix := range as112WireOrigins(sender.v6) {
				paths, ok := routes[prefix].([]any)
				if !ok || len(paths) != 1 {
					return errors.New("FRR RIB contains an unexpected alternate path")
				}
				row, ok := paths[0].(map[string]any)
				aspath, present := row["path"].(string)
				if !ok || !present || aspath != "" {
					return errors.New("FRR admitted a non-local path into its AS112 RIB")
				}
			}
			continue
		}
		client := filepath.Join(filepath.Dir(binary), "birdc")
		protocol := "peer_" + Checksum(sender.id)[:20]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stats, err := run(ctx, client, "-s", socket, "show protocols all "+protocol)
		cancel()
		match := regexp.MustCompile(`(?m)^\s*Import updates:\s+([0-9]+)`).FindStringSubmatch(stats)
		if err != nil || len(match) != 2 {
			return errors.New("BIRD ingress counter unavailable")
		}
		count, _ := strconv.Atoi(match[1])
		if count < 2 {
			return errors.New("BIRD has not processed the two actual sender NLRIs")
		}
		table := "master4"
		if sender.v6 {
			table = "master6"
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		all, err := run(ctx, client, "-s", socket, "show route table "+table+" all")
		cancel()
		if err != nil {
			return err
		}
		for _, prefix := range as112WireOrigins(sender.v6) {
			if !strings.Contains(all, prefix) {
				return errors.New("BIRD RIB did not contain its native static origin")
			}
		}
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		learned, err := run(ctx, client, "-s", socket, "show route table "+table+" protocol "+protocol+" all")
		cancel()
		if err != nil || strings.Contains(learned, "198.51.100.0/24") || strings.Contains(learned, "2001:db8:dead::/48") || strings.Contains(learned, "192.175.48.0/24") || strings.Contains(learned, "2620:4f:8000::/48") {
			return errors.New("BIRD admitted a sender route into its RIB")
		}
	}
	return nil
}

func TestAS112NativeBGPWire(t *testing.T) {
	requireAS112WireSandbox(t, false)
	for until := time.Now().Add(15 * time.Second); ; {
		if _, err := as112WireControl("state"); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("cap0 peer namespace helper unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer as112WireControl("shutdown")
	for _, target := range []struct{ platform, variable string }{{"bird2", "BIRD_BIN"}, {"bird3", "BIRD3_BIN"}, {"frr", "FRR_BIN"}} {
		t.Run(target.platform, func(t *testing.T) {
			binary := os.Getenv(target.variable)
			if binary == "" {
				t.Skip("actual native daemon path is required")
			}
			if _, err := as112WireControl("start"); err != nil {
				t.Fatal(err)
			}
			defer as112WireControl("stop")
			dir := t.TempDir()
			router := Router{ID: "wire_as112", Platform: target.platform, Role: RoleAS112, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: "both", AS112IPv4: true, AS112IPv6: true}
			peers := []Peer{}
			for _, definition := range as112WireDefinitions {
				peers = append(peers, Peer{ID: definition.id, ASN: uint32(definition.asn), Address: definition.address, Enabled: true, MaxPrefixes: 10})
			}
			config, err := RenderFor(target.platform, router, peers)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "generated.conf")
			if err = os.WriteFile(file, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "bird.ctl")
			args := []string{"-f", "-c", file, "-s", socket, "-P", filepath.Join(dir, "bird.pid")}
			if target.platform == "frr" {
				socket = filepath.Join(dir, "bgpd.vty")
				args = []string{"--skip_runas", "--bgp_port", "179", "--no_zebra", "--no_kernel", "--vty_socket", dir, "--pid_file", filepath.Join(dir, "bgpd.pid"), "--config_file", file}
			}
			command := exec.Command(binary, args...)
			command.Dir = dir
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			log := &as112WireLog{}
			command.Stdout, command.Stderr = log, log
			if err = command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait(); close(done) }()
			defer func() {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
					<-done
				}
			}()
			for deadline := time.Now().Add(25 * time.Second); ; {
				select {
				case err := <-done:
					t.Fatalf("native daemon exited: %v\n%s", err, log.String())
				default:
				}
				state, err := as112WireControl("state")
				if err == nil {
					for _, peer := range state.Peers {
						if peer.Error != "" {
							t.Fatalf("native wire peer %s failed: %s\n%s", peer.ID, peer.Error, log.String())
						}
					}
					err = as112WireReady(state)
				}
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("native origin wire proof unavailable: %v\n%s", err, log.String())
				}
				time.Sleep(30 * time.Millisecond)
			}
			if _, err = as112WireControl("inject"); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(10 * time.Second); ; {
				err = as112WireRIB(target.platform, binary, socket)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("native ingress/RIB proof unavailable: %v\n%s", err, log.String())
				}
				time.Sleep(30 * time.Millisecond)
			}
			state, err := as112WireControl("state")
			if err != nil {
				t.Fatal(err)
			}
			if err = as112WireReady(state); err != nil {
				t.Fatal(err)
			}
			t.Log("actual_dual_stack_bgp=true four_peers=true exact_service_origins=true wire_as_sequence_112=true received_malicious_update_verified=true malicious_route_absent_from_accepted_rib=true no_transit=true capability_zero=true no_new_privs=true")
		})
	}
}
