package as112ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
	"github.com/moedove/ixp-manager/internal/routing"
)

type modelRuntime struct {
	t             *testing.T
	plan          Plan
	processes     []Process
	healthy       bool
	next          int
	calls         []string
	fail          string
	validateFail  bool
	afterHealth   func()
	afterValidate func()
	owned         map[int]bool
	observeFail   bool
}

func (r *modelRuntime) mutation(kind string) error {
	r.t.Helper()
	s, e := ReadStatus(r.plan.Root)
	if e != nil || s.Pending == nil || s.Pending.Kind != kind {
		r.t.Fatalf("operation without durable intent: %s %+v %v", kind, s, e)
	}
	r.calls = append(r.calls, kind)
	if r.fail == kind {
		return errors.New("injected uncertain result")
	}
	return nil
}
func (r *modelRuntime) Validate(_ context.Context, p Plan, dir string) error {
	r.calls = append(r.calls, "validate")
	if r.validateFail {
		return errors.New("native rejected")
	}
	c := Controller{plan: p}
	e := c.verifyVersion(dir)
	if e == nil && r.afterValidate != nil {
		r.afterValidate()
	}
	return e
}
func (r *modelRuntime) Observe(context.Context, Plan) (Observation, error) {
	if r.observeFail {
		return Observation{}, errors.New("injected observation failure")
	}
	return Observation{append([]Process{}, r.processes...)}, nil
}
func (r *modelRuntime) StartDNS(context.Context, Plan, string) (Process, error) {
	if e := r.mutation("start-dns"); e != nil {
		return Process{}, e
	}
	return r.add("dns", "authoritative"), nil
}
func (r *modelRuntime) add(kind, mode string) Process {
	r.next++
	p := Process{PID: r.next, StartTime: "test-generation", ExecutableSHA256: strings.Repeat("a", 64), Kind: kind, Mode: mode}
	r.processes = append(r.processes, p)
	if r.owned == nil {
		r.owned = map[int]bool{}
	}
	r.owned[p.PID] = true
	return p
}
func (r *modelRuntime) StartBGP(_ context.Context, _ Plan, _ string, active bool) (Process, error) {
	mode := "standby"
	if active {
		mode = "active"
	}
	if e := r.mutation("start-" + mode); e != nil {
		return Process{}, e
	}
	return r.add("bgp", mode), nil
}
func (r *modelRuntime) Stop(_ context.Context, p Process) error {
	if e := r.mutation("stop-" + p.Kind); e != nil {
		return e
	}
	for i, v := range r.processes {
		if v == p {
			r.processes = append(r.processes[:i], r.processes[i+1:]...)
			return nil
		}
	}
	return errors.New("not owned")
}
func (r *modelRuntime) Health(_ context.Context, p Plan) (as112.HealthResult, error) {
	if r.afterHealth != nil {
		r.afterHealth()
	}
	return as112.HealthResult{RouterID: p.RouterID, CheckedAt: time.Now(), Healthy: r.healthy, LocalAddressesPresent: r.healthy, Queries: 1}, nil
}
func (r *modelRuntime) Close() error {
	out := []Process{}
	for _, p := range r.processes {
		if !r.owned[p.PID] {
			out = append(out, p)
		}
	}
	r.processes = out
	return nil
}
func testPlan(t *testing.T, platform string) Plan {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instance")
	router := routing.Router{ID: "as112-test", Role: routing.RoleAS112, Platform: platform, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", AS112Service: "both", AS112IPv4: true}
	active, e := routing.RenderFor(platform, router, []routing.Peer{{ID: "peer-a", Address: "192.0.2.1", ASN: 64500, Enabled: true, MaxPrefixes: 10, Password: "SyntheticKey", AuthenticationRevision: "abcdefghijklmnopqrstuv"}})
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := as112.RenderNode(router.ID, "2026-11-15T00:00:00Z", routing.AS112Service(router), as112.Identity{ServerName: "as112.example.net.", ResponsibleMailbox: "hostmaster.example.net.", Facility: "fixture", Location: "test city", UniqueIP: router.LocalIPv4}, []string{router.LocalIPv4})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := as112.Encode(bundle)
	if e != nil {
		t.Fatal(e)
	}
	pin := BinaryPin{Path: "/usr/bin/test-native", SHA256: strings.Repeat("a", 64)}
	host, e := CaptureTarget()
	if e != nil {
		t.Fatal(e)
	}
	p, e := BuildPlan(PlanInput{Root: root, Platform: platform, ActiveConfig: active, DNSBundle: raw, Tools: Tools{pin, pin, pin}, Host: host})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func approved(p Plan) Approval {
	return Approval{p.Checksum, p.ActiveChecksum, p.StandbyChecksum, p.DNSChecksum, true}
}
func testController(t *testing.T, p Plan) (*Controller, *modelRuntime) {
	t.Helper()
	r := &modelRuntime{t: t, plan: p}
	c, e := Open(p, r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, r
}
func TestOperationalLifecycle(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3", "frr"} {
		t.Run(platform, func(t *testing.T) {
			ctx := context.Background()
			p := testPlan(t, platform)
			c, r := testController(t, p)
			if _, e := c.Install(ctx, Approval{}); e == nil {
				t.Fatal("implicit approval")
			}
			if len(r.calls) != 0 {
				t.Fatal("approval failure mutated")
			}
			if s, e := c.Install(ctx, approved(p)); e != nil || !s.Installed || s.Phase != "withdrawn" {
				t.Fatal(s, e)
			}
			if strings.Contains(p.StandbyConfig, "SyntheticKey") {
				t.Fatal("standby retained BGP session")
			}
			if _, e := c.Install(ctx, approved(p)); e != nil {
				t.Fatal(e)
			}
			if len(r.calls) != 1 {
				t.Fatal("idempotent install revalidated/wrote")
			}
			if s, e := c.Tick(ctx, true); e != nil || s.BGP != nil || s.LastHealthy {
				t.Fatal(s, e)
			}
			r.healthy = true
			for range 2 {
				if s, e := c.Tick(ctx, false); e != nil || s.Phase == "active" {
					t.Fatal(s, e)
				}
			}
			if s, e := c.Tick(ctx, true); e != nil || s.Phase != "active" || s.BGP.Mode != "active" {
				t.Fatal(s, e)
			}
			r.healthy = false
			if s, e := c.Tick(ctx, true); e != nil || s.Phase != "withdrawn" || s.BGP != nil || s.HealthyChecks != 0 {
				t.Fatal(s, e)
			}
			r.healthy = true
			if s, e := c.Tick(ctx, true); e != nil || s.Phase == "active" {
				t.Fatal(s, e)
			}
			if s, e := c.Tick(ctx, true); e != nil || s.Phase != "active" {
				t.Fatal(s, e)
			}
			if s, e := c.Withdraw(ctx); e != nil || s.BGP != nil {
				t.Fatal(s, e)
			}
			if e := c.Close(); e != nil {
				t.Fatal(e)
			}
			if len(r.processes) != 0 {
				t.Fatal("close left owned daemon")
			}
		})
	}
}
func TestOperationalUnknownNeverReplays(t *testing.T) {
	ctx := context.Background()
	p := testPlan(t, "bird2")
	c, r := testController(t, p)
	if _, e := c.Install(ctx, approved(p)); e != nil {
		t.Fatal(e)
	}
	r.fail = "start-dns"
	if s, e := c.Tick(ctx, true); e == nil || s.Pending == nil || s.Phase != "unknown" {
		t.Fatal(s, e)
	}
	before := len(r.calls)
	for range 3 {
		if _, e := c.Tick(ctx, true); e == nil {
			t.Fatal("unknown allowed tick")
		}
	}
	if len(r.calls) != before {
		t.Fatal("unknown intent retried")
	}
	r.processes = []Process{{PID: 999, Kind: "foreign"}}
	if _, e := c.Reconcile(ctx); e == nil {
		t.Fatal("reconciled external process")
	}
	if len(r.calls) != before {
		t.Fatal("reconcile wrote runtime")
	}
	r.processes = nil
	r.fail = ""
	if s, e := c.Reconcile(ctx); e != nil || s.Pending != nil || s.Phase != "withdrawn" {
		t.Fatal(s, e)
	}
	if _, e := c.Tick(ctx, false); e != nil {
		t.Fatal(e)
	}
}
func TestOperationalFreshOwnerAndTamper(t *testing.T) {
	ctx := context.Background()
	p := testPlan(t, "frr")
	c, r := testController(t, p)
	if _, e := c.Install(ctx, approved(p)); e != nil {
		t.Fatal(e)
	}
	r.healthy = true
	for range 2 {
		if _, e := c.Tick(ctx, true); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(c.versionDir(), "dns", "pdns.conf"), []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if s, e := c.Tick(ctx, true); e == nil || s.Phase != "unknown" || s.BGP != nil {
		t.Fatal(s, e)
	}
	if _, e := c.Reconcile(ctx); e == nil {
		t.Fatal("tampered instance reconciled")
	}
}
func TestOperationalRestartRequiresAbsenceAndFreshHealth(t *testing.T) {
	ctx := context.Background()
	p := testPlan(t, "bird3")
	c, r := testController(t, p)
	if _, e := c.Install(ctx, approved(p)); e != nil {
		t.Fatal(e)
	}
	r.healthy = true
	for range 2 {
		if _, e := c.Tick(ctx, true); e != nil {
			t.Fatal(e)
		}
	}
	// Simulate sudden owner loss: kernel child cleanup is tested separately.
	c.closed = true
	_ = c.lock.Close()
	fresh := &modelRuntime{t: t, plan: p, healthy: true, processes: append([]Process{}, r.processes...)}
	next, e := Open(p, fresh)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e = next.Tick(ctx, true); e == nil {
		t.Fatal("fresh owner adopted unproven processes")
	}
	if len(fresh.calls) != 0 {
		t.Fatal("fresh owner mutated external instance")
	}
	fresh.processes = nil
	if _, e = next.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if s, e := next.Tick(ctx, true); e != nil || s.Phase == "active" {
		t.Fatal(s, e)
	}
	if s, e := next.Tick(ctx, true); e != nil || s.Phase != "active" {
		t.Fatal(s, e)
	}
}
func TestOperationalFailuresDoNotPublish(t *testing.T) {
	ctx := context.Background()
	p := testPlan(t, "bird2")
	c, r := testController(t, p)
	r.validateFail = true
	if _, e := c.Install(ctx, approved(p)); e == nil {
		t.Fatal("bad native accepted")
	}
	if _, e := os.Lstat(filepath.Join(p.Root, "current")); !os.IsNotExist(e) {
		t.Fatal("published rejected native")
	}
	if c.Status().Pending != nil {
		t.Fatal("read-only validation created intent")
	}
	r.validateFail = false
	if _, e := c.Install(ctx, approved(p)); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(p, r); e == nil {
		t.Fatal("second owner")
	}
	other := p
	other.Host.NetworkNamespace = "1:2"
	other.Checksum = digest(other)
	if _, e := Open(other, r); e == nil {
		t.Fatal("wrong target")
	}
}
func TestOperationalDNSExitBeforeActivation(t *testing.T) {
	ctx := context.Background()
	p := testPlan(t, "bird2")
	c, r := testController(t, p)
	if _, e := c.Install(ctx, approved(p)); e != nil {
		t.Fatal(e)
	}
	r.healthy = true
	if _, e := c.Tick(ctx, true); e != nil {
		t.Fatal(e)
	}
	r.afterHealth = func() {
		out := []Process{}
		for _, p := range r.processes {
			if p.Kind != "dns" {
				out = append(out, p)
			}
		}
		r.processes = out
	}
	if s, e := c.Tick(ctx, true); e != nil || s.Phase == "active" || s.BGP != nil {
		t.Fatal(s, e)
	}
}
func TestOperationalPlanStrictness(t *testing.T) {
	p := testPlan(t, "bird2")
	raw, e := EncodePlan(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = DecodePlan(raw); e != nil {
		t.Fatal(e)
	}
	if _, e = DecodePlan(append([]byte(" "), raw...)); e == nil {
		t.Fatal("noncanonical plan")
	}
	p.StandbyConfig = p.ActiveConfig
	p.StandbyChecksum = p.ActiveChecksum
	p.Checksum = digest(p)
	if e = ValidatePlan(p); e == nil {
		t.Fatal("active masquerades as standby")
	}
	for _, platform := range []string{"openbgpd", "junos", "routeros"} {
		p.Platform = platform
		if e = ValidatePlan(p); e == nil {
			t.Fatal("unsupported supervisor")
		}
	}
}
func TestOperationalActiveFailureWithdrawsBeforeReturn(t *testing.T) {
	for _, kind := range []string{"observe", "wal", "sequence", "install-tamper"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			p := testPlan(t, "frr")
			c, r := testController(t, p)
			if _, e := c.Install(ctx, approved(p)); e != nil {
				t.Fatal(e)
			}
			r.healthy = true
			for range 2 {
				if _, e := c.Tick(ctx, true); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "observe" {
				r.observeFail = true
			} else if kind == "sequence" {
				c.state.Sequence = 1000000
			} else if kind == "install-tamper" {
				if e := os.WriteFile(filepath.Join(c.versionDir(), "standby.conf"), []byte("unexpected"), 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				if e := os.Remove(filepath.Join(p.Root, "status.json")); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(filepath.Join(p.Root, "status.json"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			var e error
			if kind == "observe" {
				_, e = c.Tick(ctx, true)
			} else if kind == "install-tamper" {
				_, e = c.Install(ctx, approved(p))
			} else {
				_, e = c.Withdraw(ctx)
			}
			if e == nil {
				t.Fatal("injected failure accepted")
			}
			if len(r.processes) != 0 {
				t.Fatal("active process survived failed Tick/Withdraw until deferred Close")
			}
			if c.Status().Phase != "unknown" {
				t.Fatal("emergency stop claimed reconciliation")
			}
		})
	}
}
