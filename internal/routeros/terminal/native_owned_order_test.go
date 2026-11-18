package terminal

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// The proxy never decrypts, reorders or discards SSH bytes. After arming it
// buffers client->server ciphertext, including the original stream's EOF,
// while the client Write itself proceeds normally. Release sends data then EOF.
type ownedOrderProxy struct {
	listener         *net.TCPListener
	mu               sync.Mutex
	client, upstream *net.TCPConn
	hold             bool
	buffer           []byte
	release          chan struct{}
	eof              chan struct{}
	flushed          chan struct{}
	done             chan struct{}
	stop             chan struct{}
	once             sync.Once
	releaseOnce      sync.Once
	eofOnce          sync.Once
	flushedOnce      sync.Once
	failed           atomic.Bool
	heldBytes        atomic.Int64
}

func startOwnedOrderProxy(t *testing.T) *ownedOrderProxy {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12223})
	if err != nil {
		t.Fatal("owned_order_proxy_listen=false")
	}
	p := &ownedOrderProxy{listener: listener, release: make(chan struct{}), eof: make(chan struct{}), flushed: make(chan struct{}), done: make(chan struct{}), stop: make(chan struct{})}
	go p.serve()
	t.Cleanup(p.close)
	return p
}
func (p *ownedOrderProxy) arm() { p.mu.Lock(); p.hold = true; p.mu.Unlock() }
func (p *ownedOrderProxy) serve() {
	defer close(p.done)
	client, err := p.listener.AcceptTCP()
	if err != nil {
		return
	}
	upstream, err := net.DialTCP("tcp", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12222})
	if err != nil {
		p.failed.Store(true)
		client.Close()
		return
	}
	p.mu.Lock()
	p.client = client
	p.upstream = upstream
	p.mu.Unlock()
	_ = client.SetDeadline(time.Now().Add(100 * time.Second))
	_ = upstream.SetDeadline(time.Now().Add(100 * time.Second))
	var worker sync.WaitGroup
	worker.Add(1)
	defer func() { _ = client.Close(); _ = upstream.Close(); worker.Wait() }()
	go func() {
		defer worker.Done()
		buf := make([]byte, 8192)
		total := 0
		discard := false
		for {
			n, e := upstream.Read(buf)
			if n > 0 {
				total += n
				if total > 2<<20 {
					p.failed.Store(true)
					return
				}
				if !discard {
					if !proxyWriteAll(client, buf[:n]) {
						discard = true
					}
				}
			}
			if e != nil {
				return
			}
		}
	}()
	buf := make([]byte, 8192)
	total := 0
	for {
		n, readErr := client.Read(buf)
		if n > 0 {
			total += n
			if total > 2<<20 {
				p.failed.Store(true)
				break
			}
			p.mu.Lock()
			hold := p.hold
			if hold {
				if len(p.buffer)+n > 256<<10 {
					p.mu.Unlock()
					p.failed.Store(true)
					break
				}
				p.buffer = append(p.buffer, buf[:n]...)
				p.heldBytes.Add(int64(n))
			}
			p.mu.Unlock()
			if !hold && !proxyWriteAll(upstream, buf[:n]) {
				p.failed.Store(true)
				break
			}
		}
		if readErr != nil {
			p.mu.Lock()
			hold := p.hold
			p.mu.Unlock()
			if !hold {
				if !errors.Is(readErr, io.EOF) {
					p.failed.Store(true)
				}
				break
			}
			// Killing the local ssh process can produce EOF or reset. Both
			// end this exact client stream; already read ciphertext is retained.
			p.eofOnce.Do(func() { close(p.eof) })
			select {
			case <-p.release:
			case <-p.stop:
				return
			}
			p.mu.Lock()
			held := append([]byte(nil), p.buffer...)
			p.mu.Unlock()
			if !proxyWriteAll(upstream, held) {
				clear(held)
				p.failed.Store(true)
				break
			}
			clear(held)
			if upstream.CloseWrite() != nil {
				p.failed.Store(true)
				break
			}
			p.flushedOnce.Do(func() { close(p.flushed) })
			// Leave the upstream read side alive. A downstream write failure
			// must not close the owner channel before queued data/EOF ordering.
			<-p.stop
			break
		}
	}
	_ = client.Close()
	_ = upstream.Close()
	worker.Wait()
}
func openOwnedOrderSSH(ctx context.Context, opts SSHOptions, limits Limits, proxy *ownedOrderProxy) (*Client, *ownedOrderSession, error) {
	args, err := SSHArgs(opts)
	if err != nil {
		return nil, nil, err
	}
	args = append([]string{"-o", "Compression=no"}, args...)
	if _, err = checkedLimits(limits); err != nil {
		return nil, nil, err
	}
	cmd := sshCommand(ctx, args)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, failure(TransportFailure)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, nil, failure(TransportFailure)
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		return nil, nil, failure(TransportFailure)
	}
	transport := &sshSession{cmd: cmd, in: in, out: out}
	// Install before Open starts its read pump; mutating Client.session later
	// would race the pump even if all mutation writes were serialized.
	wrapper := &ownedOrderSession{Session: transport, proxy: proxy}
	client, err := Open(wrapper, limits)
	if err != nil {
		transport.Close()
		return nil, nil, err
	}
	return client, wrapper, nil
}
func proxyWriteAll(w io.Writer, data []byte) bool {
	for len(data) > 0 {
		n, e := w.Write(data)
		if e != nil || n <= 0 || n > len(data) {
			return false
		}
		data = data[n:]
	}
	return true
}
func (p *ownedOrderProxy) close() {
	p.once.Do(func() {
		close(p.stop)
		p.releaseOnce.Do(func() { close(p.release) })
		p.listener.Close()
		p.mu.Lock()
		if p.client != nil {
			p.client.Close()
		}
		if p.upstream != nil {
			p.upstream.Close()
		}
		clear(p.buffer)
		p.mu.Unlock()
	})
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

type ownedOrderSession struct {
	Session
	proxy     *ownedOrderProxy
	submitted atomic.Int64
	armed     atomic.Bool
}

func (s *ownedOrderSession) Write(data []byte) (int, error) {
	if strings.Contains(string(data), ":do { /routing/filter/rule/set") && s.armed.CompareAndSwap(false, true) {
		s.proxy.arm()
	}
	n, err := s.Session.Write(data)
	if s.armed.Load() {
		s.submitted.Add(int64(n))
	}
	return n, err
}

func ownedOrderPrivateFile(t *testing.T, path string, limit int, private bool) []byte {
	t.Helper()
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > int64(limit) || info.Mode().Perm()&022 != 0 || private && info.Mode().Perm() != 0600 {
		t.Fatal("owned_order_fixture_file=false")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		t.Fatal("owned_order_fixture_owner=false")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		t.Fatal("owned_order_fixture_read=false")
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		t.Fatal("owned_order_fixture_changed=false")
	}
	data, e := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if e != nil || len(data) > limit {
		t.Fatal("owned_order_fixture_bound=false")
	}
	return data
}
func ownedOrderGuard(t *testing.T) {
	t.Helper()
	if os.Getenv("MOEDOVE_CHR_NATIVE_TEST") != "1" {
		t.Skip("native_chr_fixture_enabled=false")
	}
	scratch, err := os.Lstat("/scratch")
	if err != nil || !scratch.IsDir() || scratch.Mode().Perm() != 0700 || scratch.Mode()&os.ModeSymlink != 0 {
		t.Fatal("owned_order_private_scratch=false")
	}
	scratchStat, ok := scratch.Sys().(*syscall.Stat_t)
	if !ok || scratchStat.Uid != uint32(os.Geteuid()) {
		t.Fatal("owned_order_private_scratch_owner=false")
	}
	status, e := os.ReadFile("/proc/self/status")
	if e != nil || len(status) > 64<<10 {
		t.Fatal("owned_order_capabilities=false")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), string(rune(10))) {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "CapEff:" || fields[0] == "CapBnd:") {
			v, e := strconv.ParseUint(fields[1], 16, 64)
			if e != nil || v != 0 || seen[fields[0]] {
				t.Fatal("owned_order_zero_caps=false")
			}
			seen[fields[0]] = true
		}
	}
	if !seen["CapEff:"] || !seen["CapBnd:"] {
		t.Fatal("owned_order_zero_caps=false")
	}
	ifaces, e := net.Interfaces()
	if e != nil || len(ifaces) != 1 || ifaces[0].Name != "lo" || ifaces[0].Flags&net.FlagLoopback == 0 || ifaces[0].Flags&net.FlagUp == 0 {
		t.Fatal("owned_order_loopback_only=false")
	}
	raw := ownedOrderPrivateFile(t, "/scratch/native-context.json", 8192, false)
	var cfg struct {
		Version       string `json:"version"`
		ImageSHA256   string `json:"imageSha256"`
		APIAddress    string `json:"apiAddress"`
		ServerName    string `json:"serverName"`
		CAFile        string `json:"caFile"`
		PasswordFile  string `json:"passwordFile"`
		ConsoleSocket string `json:"consoleSocket"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || cfg.Version != "7.24.6" || cfg.ImageSHA256 != "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878" || cfg.APIAddress != "127.0.0.1:18729" || cfg.ServerName != "chr-fixture.invalid" || cfg.CAFile != "/scratch/api.crt" || cfg.PasswordFile != "/scratch/api-password" || cfg.ConsoleSocket != "/scratch/console.sock" {
		t.Fatal("owned_order_fixed_fixture=false")
	}
}

func TestNativeCHROwnedDelayedStreamOrder(t *testing.T) {
	ownedOrderGuard(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ca := ownedOrderPrivateFile(t, "/scratch/api.crt", 64<<10, false)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("owned_order_ca=false")
	}
	clear(ca)
	password := ownedOrderPrivateFile(t, "/scratch/api-password", 1024, true)
	defer clear(password)
	client, e := api.Dial(ctx, api.Config{Address: "127.0.0.1:18729", ServerName: "chr-fixture.invalid", Username: "admin", Password: password, RootCAs: roots, DialTimeout: 5 * time.Second, CommandTimeout: 5 * time.Second})
	clear(password)
	if e != nil {
		t.Fatal("owned_order_api_verified=false")
	}
	defer client.Close()
	resource, e := client.Read(ctx, api.SystemResource)
	if e != nil || len(resource) != 1 || (resource[0]["version"] != "7.24.6" && resource[0]["version"] != "7.24.6 (long-term)") {
		t.Fatal("owned_order_version=false")
	}
	licenses, e := client.Read(ctx, api.SystemLicense)
	if e != nil || len(licenses) != 1 || !licenseSystemIDPattern.MatchString(licenses[0]["system-id"]) {
		t.Fatal("owned_order_identity=false")
	}
	idDigest := sha256.Sum256([]byte(licenses[0]["system-id"]))
	mode, e := client.ReadSafeMode(ctx)
	if e != nil || mode.Enabled || mode.Current {
		t.Fatal("owned_order_initial_idle=false")
	}
	const marker = "md_owned_delayed_order"
	rows, e := client.Read(ctx, api.FilterRule)
	if e != nil {
		t.Fatal("owned_order_baseline_read=false")
	}
	for _, row := range rows {
		if row["chain"] == marker || row["comment"] == marker {
			t.Fatal("owned_order_fixture_absent=false")
		}
	}
	created, e := client.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Add, Attributes: []api.Attribute{{Name: "chain", Value: marker}, {Name: "comment", Value: marker}, {Name: "rule", Value: "reject;"}}})
	if e != nil || !created.Acknowledged || !strings.HasPrefix(created.ID, "*") {
		t.Fatal("owned_order_fixture_create=false")
	}
	hosts := ownedOrderPrivateFile(t, "/scratch/known_hosts", 32<<10, false)
	fields := strings.Fields(string(hosts))
	if len(fields) != 3 || fields[0] != "[127.0.0.1]:12222" {
		t.Fatal("owned_order_public_pin_exact=false")
	}
	const proxyHosts = "/scratch/known_hosts_owned_order_proxy"
	f, e := os.OpenFile(proxyHosts, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		t.Fatal("owned_order_proxy_pin=false")
	}
	if _, e = io.WriteString(f, "[127.0.0.1]:12223 "+fields[1]+" "+fields[2]+string(rune(10))); e != nil {
		f.Close()
		t.Fatal("owned_order_proxy_pin=false")
	}
	f.Close()
	t.Cleanup(func() { _ = os.Remove(proxyHosts) })
	key := ownedOrderPrivateFile(t, "/scratch/ssh_identity", 32<<10, true)
	clear(key)
	proxy := startOwnedOrderProxy(t)
	limits := DefaultLimits()
	limits.Lifetime = 90 * time.Second
	limits.OperationTimeout = 10 * time.Second
	term, wrapper, e := openOwnedOrderSSH(ctx, SSHOptions{Host: "127.0.0.1", Port: 12223, User: "admin", Identity: "/scratch/ssh_identity", KnownHosts: proxyHosts}, limits, proxy)
	if e != nil {
		t.Fatal("owned_order_ssh_verified=false")
	}
	defer term.Close()
	if _, e = term.Synchronize(ctx); e != nil {
		t.Fatal("owned_order_ssh_sync=false")
	}
	identity, e := term.Query(ctx, LicenseSystemID)
	if e != nil || identity.IdentifierFingerprint != hex.EncodeToString(idDigest[:]) {
		t.Fatal("owned_order_same_device=false")
	}
	if _, e = term.EnterSafeMode(ctx); e != nil {
		t.Fatal("owned_order_take=false")
	}
	commandCtx, stopCommand := context.WithTimeout(ctx, 2*time.Second)
	receipt, e := term.ExecuteOwned(commandCtx, api.Mutation{Resource: api.FilterRule, Action: api.Set, ID: created.ID, Attributes: []api.Attribute{{Name: "rule", Value: "accept;"}}})
	stopCommand()
	if e == nil || receipt.Completed || !term.Snapshot().Closed || !wrapper.armed.Load() || wrapper.submitted.Load() == 0 {
		t.Fatal("owned_order_submission_and_deadline=false")
	}
	select {
	case <-proxy.eof:
	case <-ctx.Done():
		t.Fatal("owned_order_client_stream_ended=false")
	case <-time.After(5 * time.Second):
		t.Fatal("owned_order_client_stream_ended=false")
	}
	if proxy.failed.Load() || proxy.heldBytes.Load() < wrapper.submitted.Load() {
		t.Fatal("owned_order_ciphertext_held=false")
	}
	// The server has not received the delayed command OR EOF. It must retain
	// ownership; seeing baseline at this point is expressly not rollback proof.
	safe, e := client.ReadSafeMode(ctx)
	rule, re := client.ReadProperty(ctx, api.FilterRule, created.ID, "rule")
	if e != nil || re != nil || !safe.Enabled || safe.Current || !rule.Known || rule.Value != "reject;" {
		t.Fatal("owned_order_before_release_still_owned=false")
	}
	t.Log("owned_order_before_release_still_owned=true baseline_observed=true rollback_claimed=false")
	proxy.releaseOnce.Do(func() { close(proxy.release) })
	select {
	case <-proxy.flushed:
	case <-ctx.Done():
		t.Fatal("owned_order_data_before_eof=false")
	case <-time.After(5 * time.Second):
		t.Fatal("owned_order_data_before_eof=false")
	}
	until := time.Now().Add(35 * time.Second)
	released := false
	for time.Now().Before(until) && ctx.Err() == nil {
		safe, e = client.ReadSafeMode(ctx)
		rule, re = client.ReadProperty(ctx, api.FilterRule, created.ID, "rule")
		if e == nil && re == nil && !safe.Enabled && !safe.Current && rule.Known && rule.Value == "reject;" {
			released = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !released || proxy.failed.Load() {
		t.Fatal("owned_order_after_release_rollback=false")
	}
	// Bounded fault observation, not a claim that arbitrary future writes or
	// device implementations are fenced forever.
	stableUntil := time.Now().Add(2 * time.Second)
	for time.Now().Before(stableUntil) {
		safe, e = client.ReadSafeMode(ctx)
		rule, re = client.ReadProperty(ctx, api.FilterRule, created.ID, "rule")
		if e != nil || re != nil || safe.Enabled || safe.Current || !rule.Known || rule.Value != "reject;" {
			t.Fatal("owned_order_no_late_write=false")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("owned_order_data_before_eof=true owner_released=true checkpoint_restored=true late_write_observed=false api_activation_write=false")
	result, e := client.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Remove, ID: created.ID})
	if e != nil || !result.Acknowledged {
		t.Fatal("owned_order_cleanup_after_proof=false")
	}
}
