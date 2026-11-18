package as112ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
)

type Controller struct {
	mu      sync.Mutex
	plan    Plan
	state   Status
	runtime Runtime
	lock    *os.File
	now     func() time.Time
	fresh   bool
	closed  bool
}

func Open(p Plan, r Runtime) (*Controller, error) {
	if e := ValidatePlan(p); e != nil {
		return nil, e
	}
	if e := os.Mkdir(p.Root, 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	if e := privateDir(p.Root); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(p.Root, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("AS112 instance already has an owner")
	}
	bad := true
	defer func() {
		if bad {
			f.Close()
		}
	}()
	if r == nil {
		r = NewNativeRuntime()
	}
	c := &Controller{plan: p, runtime: r, lock: f, now: time.Now, fresh: true}
	s, e := ReadStatus(p.Root)
	if e == nil {
		if s.PlanChecksum != p.Checksum {
			return nil, errors.New("instance is bound to a different reviewed plan")
		}
		c.state = s
	} else if os.IsNotExist(e) {
		c.state = Status{Version: 1, PlanChecksum: p.Checksum, RouterID: p.RouterID, Phase: "uninstalled"}
		if e = verifyTarget(p, c.state); e != nil {
			return nil, e
		}
		if e = c.persist(); e != nil {
			return nil, e
		}
	} else {
		return nil, e
	}
	if e = verifyTarget(p, c.state); e != nil {
		return nil, e
	}
	bad = false
	return c, nil
}
func (c *Controller) snapshot() Status {
	s := c.state
	s.Binding = copyBinding(c.state.Binding)
	if s.Pending != nil {
		v := *s.Pending
		s.Pending = &v
	}
	if s.DNS != nil {
		v := *s.DNS
		s.DNS = &v
	}
	if s.BGP != nil {
		v := *s.BGP
		s.BGP = &v
	}
	return s
}
func (c *Controller) result(err error) (Status, error) { return c.snapshot(), err }
func (c *Controller) Status() Status                   { c.mu.Lock(); defer c.mu.Unlock(); return c.snapshot() }
func (c *Controller) fail(reason string) error {
	c.state.Phase = "unknown"
	c.state.Reason = reason
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	_ = c.persist()
	// Fail closed even if the caller keeps the controller alive after an error.
	// This only terminates process handles created by this Runtime. It never
	// replays a configuration mutation or signals an observed foreign PID.
	// The unknown WAL remains unresolved, regardless of emergency-stop success.
	return errors.Join(errors.New(reason), c.runtime.Close())
}
func (c *Controller) ready() error {
	if e := verifyTarget(c.plan, c.state); e != nil {
		return c.fail("AS112 target identity changed")
	}
	if c.closed {
		return errors.New("AS112 controller closed")
	}
	if m, e := ReadRetention(c.plan.Root); e == nil && m.Phase != "complete" {
		return errors.New("AS112 retention pending; explicitly reconcile absence or review remaining cleanup")
	} else if e != nil && !os.IsNotExist(e) {
		return c.fail("retention journal unreadable")
	}
	if c.state.Pending != nil || c.state.Phase == "unknown" {
		return errors.New("AS112 unresolved operation; explicit read-only reconciliation required")
	}
	if m, e := ReadReplacement(c.plan.Root); e == nil {
		if m.Phase != "complete" {
			return errors.New("AS112 replacement pending; explicit replacement reconciliation required")
		}
		if m.NextPlanChecksum != c.plan.Checksum {
			return c.fail("completed replacement marker differs from current plan")
		}
	} else if !os.IsNotExist(e) {
		return c.fail("replacement journal unreadable")
	}
	return nil
}
func (c *Controller) saveStatus() error {
	if e := c.persist(); e != nil {
		return c.fail("status persistence failed")
	}
	return nil
}
func (c *Controller) action(kind string, run func() error) error {
	if e := c.ready(); e != nil {
		return e
	}
	if c.state.Sequence >= 1000000 {
		return c.fail("AS112 journal sequence exhausted")
	}
	c.state.Sequence++
	c.state.Pending = &Intent{c.state.Sequence, kind, c.now().UTC()}
	if e := c.persist(); e != nil {
		return c.fail("intent persistence failed; no operation issued")
	}
	if e := run(); e != nil {
		return c.fail("operation result unknown: " + kind)
	}
	c.state.Pending = nil
	if e := c.persist(); e != nil {
		return c.fail("result persistence failed: " + kind)
	}
	return nil
}
func (c *Controller) initialObservation(ctx context.Context) error {
	if !c.fresh {
		return nil
	}
	o, e := c.runtime.Observe(ctx, c.plan)
	if e != nil || len(o.Processes) != 0 {
		return c.fail("preexisting or unverified instance process; no process adopted or killed")
	}
	c.fresh = false
	c.state.DNS = nil
	c.state.BGP = nil
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	if c.state.Installed {
		if e = c.verifyInstalled(); e != nil {
			return c.fail("installed files changed")
		}
		c.state.Phase = "withdrawn"
	}
	return c.persist()
}
func (c *Controller) Install(ctx context.Context, a Approval) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !a.ExclusiveInstanceAcknowledged || a.PlanChecksum != c.plan.Checksum || a.ActiveChecksum != c.plan.ActiveChecksum || a.StandbyChecksum != c.plan.StandbyChecksum || a.DNSChecksum != c.plan.DNSChecksum {
		return c.snapshot(), errors.New("explicit exact plan, DNS, active and standby approval required")
	}
	if e := c.ready(); e != nil {
		return c.snapshot(), e
	}
	if e := c.initialObservation(ctx); e != nil {
		return c.snapshot(), e
	}
	if c.state.Binding == nil {
		b, e := newBinding(c.plan)
		if e != nil {
			return c.snapshot(), e
		}
		c.state.Binding = b
		if e = c.saveStatus(); e != nil {
			return c.snapshot(), e
		}
	}
	if c.state.Installed {
		if e := c.verifyInstalled(); e != nil {
			return c.result(c.fail("installed files changed; install cannot reauthorize altered bytes"))
		}
		return c.snapshot(), nil
	}
	if e := os.Mkdir(filepath.Join(c.plan.Root, "versions"), 0700); e != nil && !os.IsExist(e) {
		return c.snapshot(), e
	}
	if e := privateDir(filepath.Join(c.plan.Root, "versions")); e != nil {
		return c.snapshot(), e
	}
	dir, e := os.MkdirTemp(filepath.Join(c.plan.Root, "versions"), ".staging-")
	if e != nil {
		return c.snapshot(), e
	}
	defer os.RemoveAll(dir)
	b, _ := as112.Decode(c.plan.DNSBundle)
	if e = as112.WriteNewDirectory(filepath.Join(dir, "dns"), b); e != nil {
		return c.snapshot(), e
	}
	for name, raw := range map[string]string{"active.conf": c.plan.ActiveConfig, "standby.conf": c.plan.StandbyConfig, "bundle.json": c.plan.DNSBundle} {
		if e = writeNew(filepath.Join(dir, name), []byte(raw)); e != nil {
			return c.snapshot(), e
		}
	}
	for _, d := range []string{filepath.Join(dir, "dns", "zones"), filepath.Join(dir, "dns"), dir} {
		if e = syncDir(d); e != nil {
			return c.snapshot(), e
		}
	}
	if e = c.runtime.Validate(ctx, c.plan, dir); e != nil {
		return c.snapshot(), errors.New("native validation failed; nothing published")
	}
	e = c.action("publish-version", func() error {
		if e := c.publish(dir); e != nil {
			return e
		}
		c.state.Installed = true
		c.state.Phase = "withdrawn"
		return nil
	})
	return c.snapshot(), e
}
func (c *Controller) observeOwned(ctx context.Context) error {
	o, e := c.runtime.Observe(ctx, c.plan)
	if e != nil {
		return c.fail("instance observation failed")
	}
	want := []Process{}
	if c.state.DNS != nil {
		want = append(want, *c.state.DNS)
	}
	if c.state.BGP != nil {
		want = append(want, *c.state.BGP)
	}
	for _, p := range o.Processes {
		found := false
		for _, w := range want {
			found = found || reflect.DeepEqual(p, w)
		}
		if !found {
			return c.fail("unexpected process claims this instance")
		}
	}
	for _, w := range want {
		found := false
		for _, p := range o.Processes {
			found = found || reflect.DeepEqual(p, w)
		}
		if !found {
			if w.Kind == "dns" {
				c.state.DNS = nil
			} else {
				c.state.BGP = nil
			}
			c.state.LastHealthy = false
			c.state.HealthyChecks = 0
			c.state.Phase = "withdrawn"
		}
	}
	return nil
}
func (c *Controller) stopBGP(ctx context.Context) error {
	if c.state.BGP == nil {
		return nil
	}
	p := *c.state.BGP
	return c.action("stop-bgp", func() error {
		if e := c.runtime.Stop(ctx, p); e != nil {
			return e
		}
		c.state.BGP = nil
		c.state.Phase = "withdrawn"
		return nil
	})
}
func (c *Controller) Tick(ctx context.Context, recover bool) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.ready(); e != nil {
		return c.snapshot(), e
	}
	if !c.state.Installed {
		return c.snapshot(), errors.New("AS112 plan not installed")
	}
	if e := c.initialObservation(ctx); e != nil {
		return c.snapshot(), e
	}
	if e := c.verifyInstalled(); e != nil {
		_ = c.stopBGP(ctx)
		return c.result(c.fail("installed files changed; recovery refused"))
	}
	if e := c.observeOwned(ctx); e != nil {
		return c.snapshot(), e
	}
	if c.state.DNS == nil {
		if e := c.stopBGP(ctx); e != nil {
			return c.snapshot(), e
		}
		if e := c.action("start-dns", func() error {
			p, e := c.runtime.StartDNS(ctx, c.plan, c.versionDir())
			if e == nil {
				c.state.DNS = &p
			}
			return e
		}); e != nil {
			return c.snapshot(), e
		}
	}
	if c.state.BGP == nil {
		if e := c.action("start-standby", func() error {
			p, e := c.runtime.StartBGP(ctx, c.plan, c.versionDir(), false)
			if e == nil {
				c.state.BGP = &p
			}
			return e
		}); e != nil {
			return c.snapshot(), e
		}
	}
	h, e := c.runtime.Health(ctx, c.plan)
	c.state.LastHealthy = e == nil && h.Healthy && h.LocalAddressesPresent && h.RouterID == c.plan.RouterID
	if !c.state.LastHealthy {
		c.state.HealthyChecks = 0
		if e = c.stopBGP(ctx); e != nil {
			return c.snapshot(), e
		}
		c.state.Phase = "withdrawn"
		c.state.Reason = "dns-or-address-health-unverified"
		return c.result(c.saveStatus())
	}
	if e = c.observeOwned(ctx); e != nil {
		return c.snapshot(), e
	}
	if c.state.DNS == nil {
		c.state.HealthyChecks = 0
		_ = c.stopBGP(ctx)
		return c.result(c.saveStatus())
	}
	if c.state.HealthyChecks < c.plan.RecoveryHealthyChecks {
		c.state.HealthyChecks++
	}
	if !recover || c.state.HealthyChecks < c.plan.RecoveryHealthyChecks {
		if c.state.BGP != nil && c.state.BGP.Mode == "active" && !recover {
			if e = c.stopBGP(ctx); e != nil {
				return c.snapshot(), e
			}
		}
		return c.result(c.saveStatus())
	}
	if c.state.BGP == nil || c.state.BGP.Mode != "active" {
		if e = c.stopBGP(ctx); e != nil {
			return c.snapshot(), e
		}
		if e = c.verifyInstalled(); e != nil {
			return c.result(c.fail("installed files changed before activation"))
		}
		if e = c.action("start-active", func() error {
			p, e := c.runtime.StartBGP(ctx, c.plan, c.versionDir(), true)
			if e == nil {
				c.state.BGP = &p
				c.state.Phase = "active"
				c.state.Reason = ""
			}
			return e
		}); e != nil {
			return c.snapshot(), e
		}
	}
	return c.result(c.saveStatus())
}
func (c *Controller) Withdraw(ctx context.Context) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.ready(); e != nil {
		return c.snapshot(), e
	}
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	e := c.stopBGP(ctx)
	if e == nil {
		c.state.Phase = "withdrawn"
		e = c.saveStatus()
	}
	return c.snapshot(), e
}

// Reconcile performs no process mutation. An unresolved attempt is never
// repeated: only total observed absence plus exact published files can close it.
func (c *Controller) Reconcile(ctx context.Context) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.snapshot(), errors.New("closed")
	}
	if m, e := ReadRetention(c.plan.Root); e == nil && m.Phase != "complete" {
		return c.snapshot(), errors.New("use explicit retention reconciliation for pending cleanup")
	} else if e != nil && !os.IsNotExist(e) {
		return c.snapshot(), e
	}
	if m, e := ReadReplacement(c.plan.Root); e == nil && m.Phase != "complete" {
		return c.snapshot(), errors.New("use explicit replacement reconciliation for this pending file transaction")
	} else if e != nil && !os.IsNotExist(e) {
		return c.snapshot(), e
	}
	if e := verifyTarget(c.plan, c.state); e != nil {
		return c.snapshot(), errors.New("AS112 target identity changed; reconciliation refused")
	}
	o, e := c.runtime.Observe(ctx, c.plan)
	if e != nil || len(o.Processes) != 0 {
		return c.snapshot(), errors.New("reconciliation requires verified absence; unknown processes are not killed")
	}
	if _, e = os.Lstat(filepath.Join(c.plan.Root, "current")); e == nil {
		if e = c.verifyInstalled(); e != nil {
			return c.snapshot(), e
		}
		c.state.Installed = true
	} else if !os.IsNotExist(e) {
		return c.snapshot(), e
	} else {
		if _, e = os.Lstat(c.versionDir()); e == nil {
			return c.snapshot(), errors.New("published version without pointer requires operator review")
		} else if !os.IsNotExist(e) {
			return c.snapshot(), errors.New("published version absence cannot be verified")
		}
		c.state.Installed = false
	}
	c.state.Pending = nil
	c.state.DNS = nil
	c.state.BGP = nil
	c.state.HealthyChecks = 0
	c.state.LastHealthy = false
	c.state.Phase = "withdrawn"
	if !c.state.Installed {
		c.state.Phase = "uninstalled"
	}
	c.state.Reason = "read-only-reconciled-no-processes"
	c.fresh = false
	return c.result(c.saveStatus())
}
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var first error
	if c.state.Pending == nil && c.state.Phase != "unknown" {
		first = c.stopBGP(ctx)
		if first == nil && c.state.DNS != nil {
			p := *c.state.DNS
			first = c.action("stop-dns", func() error {
				e := c.runtime.Stop(ctx, p)
				if e == nil {
					c.state.DNS = nil
				}
				return e
			})
		}
	}
	e := c.runtime.Close()
	if first == nil {
		first = e
	}
	c.closed = true
	_ = syscall.Flock(int(c.lock.Fd()), syscall.LOCK_UN)
	_ = c.lock.Close()
	return first
}
