package routing

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These are test-owned process receipts, not a production publisher or native
// confirmed-commit implementation. Only foreground children created here may
// be signalled. The control socket and BGP listeners must agree with /proc.
type quaggaLifecycleIdentity struct {
	PID        int
	Start      string
	Executable string
	ConfigSHA  string
}
type quaggaLifecycleProcess struct {
	command  *exec.Cmd
	done     chan struct{}
	waitErr  error
	identity quaggaLifecycleIdentity
	socket   os.FileInfo
	log      *as112WireLog
}
type quaggaLifecycle struct {
	t                            *testing.T
	dir, target, pidFile, binary string
	current                      *quaggaLifecycleProcess
	priorSocket                  os.FileInfo
	peers                        map[string]*quaggaWirePeer
	diskSHA                      string
}

func quaggaLifecycleConfigs(t *testing.T, removeReceiver bool) (string, string) {
	t.Helper()
	a, err := quaggaWireConfig()
	if err != nil {
		t.Fatal(err)
	}
	model, err := parseManagedQuagga(a)
	if err != nil {
		t.Fatal(err)
	}
	peers := []Peer{}
	for _, peer := range model.Peers {
		remove := strings.HasSuffix(peer.Address, ".3") || strings.HasSuffix(peer.Address, "::3")
		if removeReceiver {
			remove = strings.HasSuffix(peer.Address, ".2") || strings.HasSuffix(peer.Address, "::2")
		}
		if remove {
			continue
		}
		if strings.HasSuffix(peer.Address, ".1") || strings.HasSuffix(peer.Address, "::1") {
			v6 := strings.Contains(peer.Address, ":")
			peer.AllowedPrefixes = []string{quaggaWirePrefixes(v6)[7]}
			peer.AllowedRoutes = []PrefixOrigin{{Prefix: peer.AllowedPrefixes[0], OriginASN: 64512}}
		}
		peers = append(peers, peer)
	}
	b, err := RenderQuagga(model.Router, peers)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}
func newQuaggaLifecycle(t *testing.T) *quaggaLifecycle {
	t.Helper()
	quaggaWireGuard(t)
	dir := t.TempDir()
	l := &quaggaLifecycle{t: t, dir: dir, target: filepath.Join(dir, "bgpd.conf"), pidFile: filepath.Join(dir, "bgpd.pid"), binary: os.Getenv("QUAGGA_BGPD_BIN"), peers: map[string]*quaggaWirePeer{}}
	t.Cleanup(func() {
		for _, peer := range l.peers {
			peer.stop()
		}
		if p := l.current; p != nil {
			select {
			case <-p.done:
			default:
				_ = p.command.Process.Kill()
				<-p.done
			}
		}
	})
	return l
}
func (l *quaggaLifecycle) install(config string) {
	l.t.Helper()
	if ValidateManagedQuagga(config) != nil {
		l.t.Fatal("lifecycle refuses an unrecognized candidate")
	}
	if raw, err := os.ReadFile(l.target); err == nil {
		if l.diskSHA == "" || Checksum(string(raw)) != l.diskSHA {
			l.t.Fatal("foreign target bytes refused")
		}
		info, err := os.Lstat(l.target)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			l.t.Fatal("target identity/permissions changed")
		}
	} else if !os.IsNotExist(err) || l.diskSHA != "" {
		l.t.Fatal("owned target disappeared")
	}
	f, err := os.CreateTemp(l.dir, "candidate-")
	if err != nil {
		l.t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(config); err != nil {
		l.t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		l.t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		l.t.Fatal(err)
	}
	if err = os.Rename(f.Name(), l.target); err != nil {
		l.t.Fatal(err)
	}
	parent, err := os.Open(l.dir)
	if err != nil {
		l.t.Fatal(err)
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		l.t.Fatal(err)
	}
	l.diskSHA = Checksum(config)
}
func quaggaLifecycleListeners() (map[string]bool, error) {
	inodes := map[string]bool{}
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 || f[3] != "0A" {
				continue
			}
			address := strings.Split(f[1], ":")
			if len(address) != 2 {
				continue
			}
			port, err := strconv.ParseUint(address[1], 16, 16)
			if err == nil && port == 1179 {
				inodes[f[9]] = true
			}
		}
	}
	return inodes, nil
}
func quaggaLifecycleSocketPeer() (int, uint32, error) {
	c, err := net.DialTimeout("unix", "/scratch/state/bgpd.vty", time.Second)
	if err != nil {
		return 0, 0, err
	}
	defer c.Close()
	raw, err := c.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var inspect error
	err = raw.Control(func(fd uintptr) {
		cred, inspect = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || inspect != nil || cred == nil {
		return 0, 0, errors.New("control credentials unavailable")
	}
	return int(cred.Pid), cred.Uid, nil
}
func (l *quaggaLifecycle) identity(p *quaggaLifecycleProcess) (quaggaLifecycleIdentity, error) {
	pid := p.command.Process.Pid
	base := fmt.Sprintf("/proc/%d", pid)
	executable, err := os.Readlink(base + "/exe")
	if err != nil || executable != l.binary {
		return quaggaLifecycleIdentity{}, errors.New("native executable identity changed")
	}
	cmd, err := os.ReadFile(base + "/cmdline")
	if err != nil || !reflect.DeepEqual(strings.Split(strings.TrimSuffix(string(cmd), "\x00"), "\x00"), p.command.Args) {
		return quaggaLifecycleIdentity{}, errors.New("native launch arguments changed")
	}
	stat, err := os.ReadFile(base + "/stat")
	at := strings.LastIndexByte(string(stat), ')')
	if err != nil || at < 0 {
		return quaggaLifecycleIdentity{}, errors.New("native process generation missing")
	}
	fields := strings.Fields(string(stat[at+1:]))
	if len(fields) < 20 {
		return quaggaLifecycleIdentity{}, errors.New("native process generation malformed")
	}
	status, err := os.ReadFile(base + "/status")
	if err != nil || !quaggaWireCapabilities(status) {
		return quaggaLifecycleIdentity{}, errors.New("native confinement changed")
	}
	raw, err := os.ReadFile(l.target)
	if err != nil || Checksum(string(raw)) != l.diskSHA {
		return quaggaLifecycleIdentity{}, errors.New("native target bytes changed")
	}
	pidFile, err := os.ReadFile(l.pidFile)
	if err != nil || strings.TrimSpace(string(pidFile)) != strconv.Itoa(pid) {
		return quaggaLifecycleIdentity{}, errors.New("PID file does not identify owned process")
	}
	controlPID, uid, err := quaggaLifecycleSocketPeer()
	if err != nil || controlPID != pid || uid != uint32(os.Geteuid()) {
		return quaggaLifecycleIdentity{}, errors.New("control socket does not belong to owned daemon")
	}
	listeners, err := quaggaLifecycleListeners()
	if err != nil || len(listeners) != 2 {
		return quaggaLifecycleIdentity{}, errors.New("dual-family exclusive listener set unavailable")
	}
	fd, err := os.ReadDir(base + "/fd")
	if err != nil {
		return quaggaLifecycleIdentity{}, err
	}
	owned := map[string]bool{}
	for _, entry := range fd {
		link, _ := os.Readlink(base + "/fd/" + entry.Name())
		if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			owned[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	for inode := range listeners {
		if !owned[inode] {
			return quaggaLifecycleIdentity{}, errors.New("foreign BGP listener refused")
		}
	}
	return quaggaLifecycleIdentity{PID: pid, Start: fields[19], Executable: executable, ConfigSHA: l.diskSHA}, nil
}
func (l *quaggaLifecycle) launch(waitReady bool) *quaggaLifecycleProcess {
	l.t.Helper()
	if l.current != nil {
		l.t.Fatal("cannot overlap daemon generations")
	}
	listeners, err := quaggaLifecycleListeners()
	if err != nil || len(listeners) != 0 {
		l.t.Fatal("listener already occupied; no takeover")
	}
	if info, err := os.Lstat("/scratch/state/bgpd.vty"); err == nil {
		if l.priorSocket == nil || !os.SameFile(info, l.priorSocket) {
			l.t.Fatal("foreign control path refused")
		}
		if c, err := net.DialTimeout("unix", "/scratch/state/bgpd.vty", 50*time.Millisecond); err == nil {
			c.Close()
			l.t.Fatal("old control socket still live")
		}
		if err := os.Remove("/scratch/state/bgpd.vty"); err != nil {
			l.t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		l.t.Fatal(err)
	}
	command := exec.Command(l.binary, "--skip_runas", "--no_kernel", "--bgp_port", "1179", "--vty_port", "0", "--socket", filepath.Join(l.dir, "unused-zebra.sock"), "--pid_file", l.pidFile, "--config_file", l.target)
	command.Dir = l.dir
	log := &as112WireLog{}
	command.Stdout, command.Stderr = log, log
	p := &quaggaLifecycleProcess{command: command, done: make(chan struct{}), log: log}
	if err := command.Start(); err != nil {
		l.t.Fatal(err)
	}
	l.current = p
	go func() { p.waitErr = command.Wait(); close(p.done) }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		select {
		case <-p.done:
			l.t.Fatal("owned daemon failed before control creation", p.waitErr, log.String())
		default:
		}
		if info, err := os.Lstat("/scratch/state/bgpd.vty"); err == nil {
			p.socket = info
			l.priorSocket = info
			break
		}
		if time.Now().After(deadline) {
			l.t.Fatal("control path not created")
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.identity, err = l.identity(p)
	if err != nil {
		l.t.Fatal(err)
	}
	if waitReady {
		l.waitReady(p, "192.0.2.1")
	}
	return p
}
func (l *quaggaLifecycle) waitReady(p *quaggaLifecycleProcess, address string) {
	l.t.Helper()
	for deadline := time.Now().Add(12 * time.Second); ; {
		select {
		case <-p.done:
			l.t.Fatal("owned daemon exited during readiness", p.waitErr, p.log.String())
		default:
		}
		text, err := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + address)
		if err == nil && (strings.Contains(text, "BGP state = Active") || strings.Contains(text, "BGP state = Connect") || strings.Contains(text, "BGP state = Established")) {
			now, err := l.identity(p)
			if err != nil || now.PID != p.identity.PID || now.Start != p.identity.Start || now.Executable != p.identity.Executable {
				l.t.Fatal("process identity changed during readiness", err)
			}
			return
		}
		if time.Now().After(deadline) {
			l.t.Fatal("native readiness unavailable", err, text, quaggaWireDiagnostic(p.identity.PID))
		}
		time.Sleep(30 * time.Millisecond)
	}
}
func (l *quaggaLifecycle) connect(config string) {
	l.t.Helper()
	model, err := parseManagedQuagga(config)
	if err != nil {
		l.t.Fatal(err)
	}
	for _, definition := range model.Peers {
		l.waitReady(l.current, definition.Address)
		peer, err := quaggaWireConnect(definition.Address, uint16(definition.ASN), strings.Contains(definition.Address, ":"))
		if err != nil {
			l.t.Fatal("native peer connection failed", definition.Address, err)
		}
		l.peers[definition.Address] = peer
	}
}
func (l *quaggaLifecycle) ceaseAndReap() {
	l.t.Helper()
	p := l.current
	if p == nil {
		l.t.Fatal("no owned generation")
	}
	identity, err := l.identity(p)
	if err != nil || identity != p.identity {
		l.t.Fatal("refusing signal after identity/target drift", err)
	}
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		l.t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		ready := true
		for _, peer := range l.peers {
			routes, notification, err := peer.state()
			if err != nil {
				l.t.Fatal("session ended without expected native Cease", err)
			}
			if len(notification) < 2 || notification[0] != 6 || notification[1] != 3 {
				ready = false
			}
			if len(routes) != 0 {
				ready = false
			}
			select {
			case <-peer.done:
			default:
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			l.t.Fatal("native TERM did not close sessions with Cease/Peer Unconfigured and clear non-GR RIBs")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-p.done:
		if p.waitErr != nil {
			l.t.Fatal("normal termination was not clean", p.waitErr)
		}
	case <-time.After(2 * time.Second):
		l.t.Fatal("owned daemon did not terminate")
	}
	l.reaped(p)
}
func (l *quaggaLifecycle) reaped(p *quaggaLifecycleProcess) {
	l.t.Helper()
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", p.identity.PID)); !os.IsNotExist(err) {
		l.t.Fatal("old process remains after wait")
	}
	listeners, err := quaggaLifecycleListeners()
	if err != nil || len(listeners) != 0 {
		l.t.Fatal("old listeners remain", err)
	}
	for _, peer := range l.peers {
		peer.stop()
	}
	l.peers = map[string]*quaggaWirePeer{}
	l.current = nil
}
func (l *quaggaLifecycle) policy(oldPrefix, newPrefix int, origin uint16) {
	l.t.Helper()
	for _, v6 := range []bool{false, true} {
		sender := l.peers[quaggaWireAddress(v6, 1)]
		prefixes := quaggaWirePrefixes(v6)
		if oldPrefix >= 0 {
			oldOrigin := uint16(64511)
			if oldPrefix == 7 {
				oldOrigin = 64512
			}
			if err := sender.send(quaggaWireUpdate(prefixes[oldPrefix], sender.address, []uint16{64501, oldOrigin}, v6)); err != nil {
				l.t.Fatal(err)
			}
		}
		if err := sender.send(quaggaWireUpdate(prefixes[newPrefix], sender.address, []uint16{64501, origin}, v6)); err != nil {
			l.t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(35 * time.Second); ; {
		ready := true
		for _, v6 := range []bool{false, true} {
			receiver := l.peers[quaggaWireAddress(v6, 2)]
			routes, notification, err := receiver.state()
			if err != nil || len(notification) > 0 {
				l.t.Fatal("receiver failed", notification, err)
			}
			prefixes := quaggaWirePrefixes(v6)
			value, ok := routes[prefixes[newPrefix]]
			if !ok {
				ready = false
				continue
			}
			if len(routes) != 1 || !reflect.DeepEqual(value.Path, []uint16{64501, origin}) || value.NextHop != quaggaWireAddress(v6, 1) || v6 && value.LinkLocal != "fe80::1" {
				l.t.Fatal("fresh generation exported wrong policy/attributes", routes)
			}
			if oldPrefix >= 0 {
				if _, ok := routes[prefixes[oldPrefix]]; ok {
					l.t.Fatal("removed old policy survived fresh process")
				}
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			l.t.Fatal("fresh generation route unavailable")
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
			l.t.Fatal(err)
		}
		prefixes := quaggaWirePrefixes(v6)
		if !strings.Contains(rib, prefixes[newPrefix]) || oldPrefix >= 0 && strings.Contains(rib, prefixes[oldPrefix]) {
			l.t.Fatal("native RS-client RIB did not match fresh generation", rib)
		}
	}
}

func TestQuaggaNativeFreshReplacementAndFailedStartRecovery(t *testing.T) {
	l := newQuaggaLifecycle(t)
	kernel := quaggaWireKernel(t)
	a, b := quaggaLifecycleConfigs(t, false)
	l.install(a)
	first := l.launch(true)
	l.connect(a)
	l.policy(-1, 0, 64511)
	l.ceaseAndReap()
	l.install(b)
	second := l.launch(true)
	if second.identity.PID == first.identity.PID || second.identity.Start == first.identity.Start {
		t.Fatal("fresh generation reused old process identity")
	}
	l.connect(b)
	l.policy(0, 7, 64512)
	for _, v6 := range []bool{false, true} {
		if peer, err := quaggaWireConnect(quaggaWireAddress(v6, 3), 64501, v6); err == nil {
			peer.stop()
			t.Fatal("removed peer still connected after fresh replacement")
		}
	}
	l.ceaseAndReap()
	l.install(a)
	third := l.launch(true)
	if third.identity.PID == second.identity.PID {
		t.Fatal("rollback did not create a fresh process")
	}
	l.connect(a)
	l.policy(7, 0, 64511)
	l.ceaseAndReap()
	// Inject a candidate process failure after its genuine sockets/PID exist,
	// before readiness. Config, binary and launch arguments remain unchanged.
	l.install(b)
	failed := l.launch(false)
	if err := failed.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed.done:
		if failed.waitErr == nil {
			t.Fatal("candidate failure was not observed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed candidate did not exit")
	}
	l.reaped(failed)
	l.install(a)
	recovered := l.launch(true)
	if recovered.identity.PID == failed.identity.PID {
		t.Fatal("recovery adopted failed process")
	}
	l.connect(a)
	l.policy(7, 0, 64511)
	l.ceaseAndReap()
	raw, err := os.ReadFile(l.target)
	if err != nil || !bytes.Equal(raw, []byte(a)) {
		t.Fatal("exact private baseline bytes were not restored")
	}
	if !reflect.DeepEqual(kernel, quaggaWireKernel(t)) {
		t.Fatal("lifecycle changed kernel routes")
	}
	t.Log("stock_quagga_fresh_A_B_A=true term_cease_6_3=true non_gr_sessions_clear_rib=true exact_config_path_and_private_bytes=true pid_start_exe_args_socket_credentials_verified=true exclusive_dual_family_listeners=true new_policy_and_removed_peer_verified=true candidate_killed_before_ready=true failed_candidate_reaped_before_baseline_restart=true baseline_wire_policy_recovered=true no_zebra=true kernel_routes_unchanged=true cap0_nnp=true no_product_gate_change=true")
}

// This is a narrow counterexample probe, not a supported reload method.
func TestQuaggaNativeHUPDeletedReceiverSemantics(t *testing.T) {
	l := newQuaggaLifecycle(t)
	kernel := quaggaWireKernel(t)
	a, b := quaggaLifecycleConfigs(t, true)
	l.install(a)
	process := l.launch(true)
	l.connect(a)
	l.policy(-1, 0, 64511)
	identity, err := l.identity(process)
	if err != nil || identity != process.identity {
		t.Fatal(err)
	}
	l.install(b)
	if err := process.command.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Second); ; {
		closed := true
		for _, peer := range l.peers {
			_, notification, err := peer.state()
			if err != nil {
				t.Fatal("HUP closed without expected notification", err)
			}
			if len(notification) < 2 || notification[0] != 6 || notification[1] != 3 {
				closed = false
			}
		}
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HUP did not reset established peers")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, peer := range l.peers {
		peer.stop()
	}
	l.peers = map[string]*quaggaWirePeer{}
	process.identity.ConfigSHA = l.diskSHA
	l.waitReady(process, "192.0.2.1")
	// Connect using the old peer set: B has removed the two receivers. Their
	// actual acceptance demonstrates retained runtime configuration after HUP.
	l.connect(a)
	l.policy(0, 7, 64512)
	seen := []string{}
	for _, v6 := range []bool{false, true} {
		address := quaggaWireAddress(v6, 2)
		text, err := quaggaWireVTY("show ip bgp view MOEDOVE_QUAGGA neighbors " + address)
		if err != nil || !strings.Contains(text, "BGP state = Established") {
			t.Fatal("stock HUP deletion hypothesis not established", err, text)
		}
		seen = append(seen, address)
	}
	sort.Strings(seen)
	if current, err := l.identity(process); err != nil || current.PID != identity.PID || current.Start != identity.Start {
		t.Fatal("HUP unexpectedly replaced daemon", err)
	}
	l.ceaseAndReap()
	if !reflect.DeepEqual(kernel, quaggaWireKernel(t)) {
		t.Fatal("HUP probe changed kernel routes")
	}
	t.Logf("stock_quagga_hup_counterexample=true same_pid_generation=true candidate_removed_receivers=%v retained_receivers_reconnected=true candidate_only_routes_reached_removed_receivers=true no_reload_adapter_authorized=true", seen)
}
