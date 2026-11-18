package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/as112ops"
	"github.com/moedove/ixp-manager/internal/routing"
)

type cliRuntime struct {
	mu          sync.Mutex
	processes   map[int]as112ops.Process
	next        int
	healthy     bool
	failActive  bool
	starts      int
	active      int
	validations int
	stops       []string
}

func newCLIRuntime() *cliRuntime {
	return &cliRuntime{processes: map[int]as112ops.Process{}, next: 100, healthy: true}
}
func (r *cliRuntime) Validate(_ context.Context, p as112ops.Plan, dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.validations++
	for name, expected := range map[string]string{"active.conf": p.ActiveConfig, "standby.conf": p.StandbyConfig, "bundle.json": p.DNSBundle} {
		actual, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(actual) != expected {
			return errors.New("staged bytes changed")
		}
	}
	return nil
}
func (r *cliRuntime) Observe(context.Context, as112ops.Plan) (as112ops.Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := as112ops.Observation{}
	for _, p := range r.processes {
		o.Processes = append(o.Processes, p)
	}
	return o, nil
}
func (r *cliRuntime) start(kind, mode string) (as112ops.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mode == "active" && r.failActive {
		return as112ops.Process{}, errors.New("synthetic unknown start")
	}
	r.next++
	p := as112ops.Process{PID: r.next, StartTime: "fixture", ExecutableSHA256: strings.Repeat("a", 64), Kind: kind, Mode: mode}
	r.processes[p.PID] = p
	r.starts++
	if mode == "active" {
		r.active++
	}
	return p, nil
}
func (r *cliRuntime) StartDNS(context.Context, as112ops.Plan, string) (as112ops.Process, error) {
	return r.start("dns", "dns")
}
func (r *cliRuntime) StartBGP(_ context.Context, _ as112ops.Plan, _ string, active bool) (as112ops.Process, error) {
	mode := "standby"
	if active {
		mode = "active"
	}
	return r.start("bgp", mode)
}
func (r *cliRuntime) Stop(_ context.Context, p as112ops.Process) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.processes[p.PID] != p {
		return errors.New("not an owned generation")
	}
	r.stops = append(r.stops, p.Kind)
	delete(r.processes, p.PID)
	return nil
}
func (r *cliRuntime) Health(_ context.Context, p as112ops.Plan) (as112.HealthResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return as112.HealthResult{RouterID: p.RouterID, Healthy: r.healthy, LocalAddressesPresent: r.healthy}, nil
}
func (r *cliRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processes = map[int]as112ops.Process{}
	return nil
}

func cliPlan(t *testing.T) (as112ops.Plan, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: "cli_as112", Platform: "bird2", Role: routing.RoleAS112, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: "both", AS112IPv4: true, AS112IPv6: true}
	active, err := routing.RenderFor("bird2", router, []routing.Peer{{ID: "explicit", ASN: 64501, Address: "192.0.2.1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-CLI-Secret", AuthenticationRevision: "abcdefghijklmnopqrstuv"}})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := as112.RenderNode(router.ID, "2026-01-01T00:00:00Z", routing.AS112Service(router), as112.Identity{ServerName: "cli.example.test", ResponsibleMailbox: "hostmaster.example.test", Facility: "Fixture", Location: "Local", UniqueIP: router.LocalIPv4}, []string{router.LocalIPv4, router.LocalIPv6})
	if err != nil {
		t.Fatal(err)
	}
	dns, err := as112.Encode(bundle)
	if err != nil {
		t.Fatal(err)
	}
	activeFile, dnsFile, planFile := filepath.Join(dir, "active.conf"), filepath.Join(dir, "dns.json"), filepath.Join(dir, "plan.json")
	for name, content := range map[string]string{activeFile: active, dnsFile: dns} {
		if err = os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	args := []string{"prepare", "--platform", "bird2", "--root", filepath.Join(dir, "instance"), "--active-file", activeFile, "--active-checksum", routing.Checksum(active), "--dns-file", dnsFile, "--dns-checksum", routing.Checksum(dns), "--plan", planFile, "--router-binary", binary, "--pdns-server", binary, "--named-checkzone", binary, "--health-interval", "1"}
	if err = run(context.Background(), args, &output, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "activeConfig") || strings.Contains(output.String(), "DNSBundle") {
		t.Fatal("prepare exposed private artifact content")
	}
	plan, err := readPlan(planFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(plan.Root); !os.IsNotExist(err) {
		t.Fatal("prepare installed an instance")
	}
	if err = run(context.Background(), args, &output, nil); err == nil {
		t.Fatal("prepare overwrote a plan")
	}
	return plan, planFile
}
func cliApproval(plan as112ops.Plan, file string) []string {
	return []string{"install", "--plan", file, "--plan-checksum", plan.Checksum, "--active-checksum", plan.ActiveChecksum, "--standby-checksum", plan.StandbyChecksum, "--dns-checksum", plan.DNSChecksum, "--exclusive-instance-acknowledged"}
}
func installCLI(t *testing.T, plan as112ops.Plan, file string) {
	t.Helper()
	if err := run(context.Background(), cliApproval(plan, file), &bytes.Buffer{}, func() as112ops.Runtime { return newCLIRuntime() }); err != nil {
		t.Fatal(err)
	}
}
func awaitCLIStatus(t *testing.T, root string, check func(as112ops.Status) bool) as112ops.Status {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		state, err := as112ops.ReadStatus(root)
		if err == nil && check(state) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("CLI lifecycle did not reach expected persisted state")
	return as112ops.Status{}
}

func TestPrepareAndInstallKeepExactPrivateBindings(t *testing.T) {
	plan, file := cliPlan(t)
	host, err := as112ops.CaptureTarget()
	if err != nil || plan.Host != host {
		t.Fatal("prepare did not pin the actual target")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 || plan.ActiveChecksum == plan.StandbyChecksum {
		t.Fatal("private derived standby binding missing")
	}
	called := false
	factory := func() as112ops.Runtime { called = true; return newCLIRuntime() }
	args := cliApproval(plan, file)
	if err := run(context.Background(), args[:len(args)-1], &bytes.Buffer{}, factory); err == nil || called {
		t.Fatal("install without exclusive acknowledgment reached a runtime")
	}
	for _, index := range []int{4, 6, 8, 10} {
		bad := append([]string{}, args...)
		bad[index] = strings.Repeat("0", 64)
		if err := run(context.Background(), bad, &bytes.Buffer{}, factory); err == nil || called {
			t.Fatal("unreviewed checksum reached a runtime")
		}
	}
	model := newCLIRuntime()
	var output bytes.Buffer
	if err := run(context.Background(), args, &output, func() as112ops.Runtime { return model }); err != nil {
		t.Fatal(err)
	}
	state, err := as112ops.ReadStatus(plan.Root)
	if err != nil || !state.Installed || state.DNS != nil || state.BGP != nil || state.Phase != "withdrawn" || model.starts != 0 || model.validations != 1 {
		t.Fatal("installation confused file publication with service activation", state, err)
	}
	if err = run(context.Background(), args, &output, func() as112ops.Runtime { return model }); err != nil || model.validations != 1 || model.starts != 0 {
		t.Fatal("same-plan installation was not idempotent", err)
	}
	var view statusView
	if err = json.NewDecoder(&output).Decode(&view); err != nil || view.LiveHealthConfirmed || view.Source != "operation-result" {
		t.Fatal("installation claimed live health")
	}
}

func TestForegroundRecoveryAndCloseOrdering(t *testing.T) {
	plan, file := cliPlan(t)
	installCLI(t, plan, file)
	standby := newCLIRuntime()
	standbyContext, stopStandby := context.WithCancel(context.Background())
	defer stopStandby()
	standbyDone := make(chan error, 1)
	go func() {
		standbyDone <- run(standbyContext, []string{"run", "--plan", file}, &bytes.Buffer{}, func() as112ops.Runtime { return standby })
	}()
	awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool { return s.LastHealthy && s.HealthyChecks >= 2 })
	stopStandby()
	if err := <-standbyDone; err != nil || standby.active != 0 {
		t.Fatal("default foreground run granted active publication", err)
	}
	model := newCLIRuntime()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"run", "--plan", file, "--recover-ack"}, &output, func() as112ops.Runtime { return model })
	}()
	active := awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool { return s.Phase == "active" && s.BGP != nil })
	model.mu.Lock()
	model.healthy = false
	model.mu.Unlock()
	awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool { return s.Phase == "withdrawn" && !s.LastHealthy && s.BGP == nil })
	model.mu.Lock()
	model.healthy = true
	model.mu.Unlock()
	awaitCLIStatus(t, plan.Root, func(s as112ops.Status) bool {
		return s.Phase == "active" && s.BGP != nil && s.BGP.PID != active.BGP.PID
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := as112ops.ReadStatus(plan.Root)
	if err != nil || state.BGP != nil || state.DNS != nil || state.Phase != "withdrawn" {
		t.Fatal("foreground shutdown retained a claimed process", state, err)
	}
	if len(model.stops) < 2 || model.stops[len(model.stops)-2] != "bgp" || model.stops[len(model.stops)-1] != "dns" {
		t.Fatal("shutdown did not stop BGP before DNS")
	}
	if strings.Contains(output.String(), "Synthetic-CLI-Secret") || strings.Contains(output.String(), "activeConfig") {
		t.Fatal("foreground output exposed configuration")
	}
}

func TestUnknownRequiresExplicitReadonlyReconciliation(t *testing.T) {
	plan, file := cliPlan(t)
	installCLI(t, plan, file)
	model := newCLIRuntime()
	model.failActive = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, []string{"run", "--plan", file, "--recover-ack"}, &bytes.Buffer{}, func() as112ops.Runtime { return model }); err == nil {
		t.Fatal("unknown start was reported as success")
	}
	state, _ := as112ops.ReadStatus(plan.Root)
	if state.Phase != "unknown" || state.Pending == nil {
		t.Fatal("unknown operation was not retained")
	}
	next := newCLIRuntime()
	if err := run(ctx, []string{"run", "--plan", file, "--recover-ack"}, &bytes.Buffer{}, func() as112ops.Runtime { return next }); err == nil || next.starts != 0 {
		t.Fatal("restart replayed an unknown operation")
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"status", "--root", plan.Root}, &output, func() as112ops.Runtime { t.Fatal("status created a runtime"); return nil }); err != nil {
		t.Fatal(err)
	}
	var view statusView
	if json.Unmarshal(output.Bytes(), &view) != nil || view.LiveHealthConfirmed || view.Source != "persisted-journal" || view.Status.Phase != "unknown" {
		t.Fatal("status was not an explicitly historical journal projection")
	}
	if err := run(ctx, []string{"reconcile", "--plan", file, "--plan-checksum", plan.Checksum}, &output, nil); err == nil {
		t.Fatal("reconciliation skipped explicit acknowledgment")
	}
	if err := run(ctx, []string{"reconcile", "--plan", file, "--plan-checksum", plan.Checksum, "--acknowledge"}, &output, func() as112ops.Runtime { return next }); err != nil {
		t.Fatal(err)
	}
	state, _ = as112ops.ReadStatus(plan.Root)
	if state.Pending != nil || state.Phase != "withdrawn" || next.starts != 0 || len(next.stops) != 0 || next.validations != 0 {
		t.Fatal("reconciliation mutated runtime or retained uncertainty")
	}
}

func TestPrivateFilesAndReviewOnlyUnitBoundaries(t *testing.T) {
	plan, file := cliPlan(t)
	for _, mode := range []string{"symlink", "hardlink", "public", "writable-parent"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "input")
			raw, _ := os.ReadFile(file)
			switch mode {
			case "symlink":
				if err := os.Symlink(file, input); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, input); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(input)
			default:
				if err := os.WriteFile(input, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "public" {
					_ = os.Chmod(input, 0644)
				} else {
					_ = os.Chmod(dir, 0777)
					defer os.Chmod(dir, 0700)
				}
			}
			if _, err := readPlan(input); err == nil {
				t.Fatal("unsafe private input accepted")
			}
		})
	}
	for _, args := range [][]string{{"install", "--command", "forbidden"}, {"status", "--root", plan.Root, "unexpected"}, {"prepare", "--platform", "openbgpd"}, {"prepare", "--platform", "junos"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, nil); err == nil {
			t.Fatal("unsupported command surface accepted")
		}
	}
	for _, action := range []string{"prepare", "install", "run", "status", "reconcile", "unit"} {
		if err := run(context.Background(), []string{action, "--help"}, &bytes.Buffer{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	plan.Root = "/var/lib/moedove-as112/node"
	plan.Tools.Router.Path, plan.Tools.PDNS.Path, plan.Tools.NamedCheckzone.Path = "/usr/sbin/bird", "/usr/sbin/pdns_server", "/usr/bin/named-checkzone"
	content, err := unitContent(plan, "/var/lib/moedove-as112/plan.json", as112ops.BinaryPin{Path: "/usr/local/bin/ixp-as112", SHA256: strings.Repeat("a", 64)}, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Restart=on-failure", "StartLimitIntervalSec=60", "StartLimitBurst=3", "KillMode=control-group", "not an ExecStart hash check", "ProtectHome=yes", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE"} {
		if !strings.Contains(content, required) {
			t.Fatalf("review unit omitted %s", required)
		}
	}
	if strings.Contains(content, "--recover-ack") || strings.Contains(content, "Synthetic-CLI-Secret") || strings.Contains(content, "PrivateNetwork=yes\n") {
		t.Fatal("unit silently granted publication or changed the pinned network")
	}
	for _, unsafe := range []string{"/home/operator/plan.json", "/root/plan.json", "/tmp/plan.json", "/var/lib/%i/plan.json", "/var/lib/a b/plan.json"} {
		if _, err = unitContent(plan, unsafe, as112ops.BinaryPin{Path: "/usr/local/bin/ixp-as112"}, false, 0); err == nil {
			t.Fatal("unsafe unit path accepted")
		}
	}
}
