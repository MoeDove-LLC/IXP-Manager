package live

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

func writeEvidenceFixture(t *testing.T) (*Runtime, *fakeAPI, control.ReadRequest, routeros.APIMutation, control.Observation) {
	t.Helper()
	r, a, _, request := observerFixture()
	o, err := r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	p, err := routeros.BuildPlan(r.target, routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}, []routing.Peer{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}, *o.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil || r.BindPlan(p, nil) != nil {
		t.Fatal("cannot bind evidence fixture", err)
	}
	r.profile.PreparationVerified = true
	o, err = r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.bridge.Mutation(r.operations[0].ID, o.Resolver)
	if err != nil {
		t.Fatal(err)
	}
	if r.writeEvidence.Load() == nil {
		t.Fatal("complete observation did not retain private evidence")
	}
	return r, a, request, m, o
}

func TestWriteEvidenceUsesOnlyFreshFullObserveAndConsumesOnce(t *testing.T) {
	r, a, _, m, o := writeEvidenceFixture(t)
	defer r.Close()
	before := a.calls
	// The returned graph is caller-owned. It is not a mutable write authority.
	o.Inventory.Resources[0]["version"] = "caller-mutated"
	o.Inventory.Capabilities[api.FilterRule] = api.Unsupported
	o.Inventory.SafeMode.Enabled = true
	result, err := r.Apply(context.Background(), m)
	if err != nil || !result.Acknowledged || a.writes != 1 || a.calls != before || r.writeEvidence.Load() != nil {
		t.Fatal("fresh internal evidence did not remove only the redundant read", err)
	}
	if _, err = r.Apply(context.Background(), m); err == nil || a.writes != 1 {
		t.Fatal("completed mutation replayed")
	}
}

func TestWriteEvidenceExpiredOrBindingChangedFallsBackToCompleteRead(t *testing.T) {
	for _, mode := range []string{"expired", "future", "session", "plan", "operation", "epoch", "unbound-observation"} {
		t.Run(mode, func(t *testing.T) {
			r, a, _, m, _ := writeEvidenceFixture(t)
			defer r.Close()
			e := *r.writeEvidence.Load()
			switch mode {
			case "expired":
				r.config.OperationTimeoutSeconds = 60
				e.completedAt = time.Now().Add(-2 * time.Second)
			case "future":
				e.completedAt = time.Now().Add(time.Second)
			case "session":
				e.sessionID = "another-session"
			case "plan":
				e.planChecksum = strings.Repeat("f", 64)
			case "operation":
				e.operationIndex++
			case "epoch":
				e.epoch--
			case "unbound-observation":
				r.writeEvidence.Store(nil)
			}
			if mode != "unbound-observation" {
				r.writeEvidence.Store(&e)
			}
			before := a.calls
			result, err := r.Apply(context.Background(), m)
			if err != nil || !result.Acknowledged || a.calls != before+1 || a.writes != 1 || r.writeEvidence.Load() != nil {
				t.Fatal("invalid evidence did not use original full read", err)
			}
		})
	}
}

func TestWriteEvidenceWrongOperationAndWrongAttributesConsumeWithoutWrite(t *testing.T) {
	for _, mode := range []string{"order", "attributes"} {
		t.Run(mode, func(t *testing.T) {
			r, a, _, m, _ := writeEvidenceFixture(t)
			defer r.Close()
			bad := m
			if mode == "order" {
				bad.OperationID = "not-the-next-operation"
			} else {
				bad.Request.Attributes = append([]api.Attribute(nil), m.Request.Attributes...)
				bad.Request.Attributes[0].Value = "foreign"
			}
			if _, err := r.Apply(context.Background(), bad); err == nil || a.writes != 0 || r.writeEvidence.Load() != nil {
				t.Fatal("bad attempt retained or used authority")
			}
			before := a.calls
			if result, err := r.Apply(context.Background(), m); err != nil || !result.Acknowledged || a.calls != before+1 {
				t.Fatal("attempt replay reused old evidence", err)
			}
		})
	}
}

func TestWriteEvidenceFailureAndOwnerCloseInvalidate(t *testing.T) {
	for _, mode := range []string{"observation", "bind", "write", "close", "late-observation"} {
		t.Run(mode, func(t *testing.T) {
			r, a, q, m, _ := writeEvidenceFixture(t)
			defer r.Close()
			switch mode {
			case "observation":
				a.inv.Resources[0]["version"] = "other"
				if _, e := r.Observe(context.Background(), q); e == nil {
					t.Fatal("fixture expected failed observe")
				}
			case "bind":
				p := *r.boundPlan
				p.Target.RouterID = "different"
				if r.BindPlan(p, nil) == nil {
					t.Fatal("fixture expected failed bind")
				}
			case "write":
				a.writeError = &api.Error{Code: api.CodeTransport, Uncertain: true}
				if _, e := r.Apply(context.Background(), m); e == nil {
					t.Fatal("fixture expected uncertain write")
				}
				a.writeError = nil
				if _, e := r.Apply(context.Background(), m); e == nil || a.writes != 1 {
					t.Fatal("uncertain attempt retried")
				}
			case "close":
				if e := r.CloseOwner(); e != nil {
					t.Fatal(e)
				}
				if _, e := r.Apply(context.Background(), m); e == nil || a.writes != 0 {
					t.Fatal("closed owner retained authority")
				}
			case "late-observation":
				at := a.calls + 2
				a.change = func(n int, _ *api.Inventory) {
					if n == at {
						_ = r.CloseOwner()
					}
				}
				_, _ = r.Observe(context.Background(), q)
			}
			if r.writeEvidence.Load() != nil {
				t.Fatal("failure/close left evidence available")
			}
		})
	}
}

func TestWriteEvidenceDoesNotPublishAcrossInvalidationEpoch(t *testing.T) {
	r, _, _, _, _ := writeEvidenceFixture(t)
	defer r.Close()
	old := r.writeEvidence.Load()
	r.invalidateWriteEvidence()
	r.rememberWriteEvidence(old.raw, old.evidence, old.epoch)
	if r.writeEvidence.Load() != nil {
		t.Fatal("late observation crossed binding invalidation")
	}
}

func TestWaitingFailedBindInvalidatesEvidencePublishedBeforeItAcquiresGate(t *testing.T) {
	r, _, _, _, _ := writeEvidenceFixture(t)
	defer r.Close()
	old, epoch := r.writeEvidence.Load(), r.writeEvidenceEpoch.Load()
	r.gate <- struct{}{}
	p := *r.boundPlan
	p.Target.RouterID = "different"
	done := make(chan error, 1)
	go func() { done <- r.BindPlan(p, nil) }()
	until := time.Now().Add(time.Second)
	for r.writeEvidenceEpoch.Load() == epoch && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if r.writeEvidenceEpoch.Load() == epoch {
		<-r.gate
		<-done
		t.Fatal("bind did not invalidate before waiting")
	}
	// Model a full Observe completing while the binding contender waits. Its
	// late private publication cannot survive that contender's eventual failure.
	r.rememberWriteEvidence(old.raw, old.evidence, r.writeEvidenceEpoch.Load())
	if r.writeEvidence.Load() == nil {
		<-r.gate
		<-done
		t.Fatal("fixture publication failed")
	}
	<-r.gate
	if e := <-done; e == nil || r.writeEvidence.Load() != nil {
		t.Fatal("failed binding retained an intervening observation")
	}
}

type evidenceOwnedTerminal struct {
	*fakeTerminal
	writes int
}

type evidenceSlowSnapshot struct {
	*fakeTerminal
	calls int
}

func (t *evidenceSlowSnapshot) Snapshot() terminal.Evidence {
	t.calls++
	if t.calls == 2 {
		time.Sleep(1100 * time.Millisecond)
	}
	return t.fakeTerminal.Snapshot()
}
func TestWriteEvidenceExpiringDuringReconstructionFallsBackOnce(t *testing.T) {
	r, a, _, m, _ := writeEvidenceFixture(t)
	defer r.Close()
	r.terminal = &evidenceSlowSnapshot{fakeTerminal: r.terminal.(*fakeTerminal)}
	before := a.calls
	result, err := r.Apply(context.Background(), m)
	if err != nil || !result.Acknowledged || a.calls != before+1 || a.writes != 1 || r.writeEvidence.Load() != nil {
		t.Fatal("expiry did not take one full fallback read", err)
	}
}

func (t *evidenceOwnedTerminal) ExecuteOwned(_ context.Context, _ api.Mutation) (terminal.OwnedReceipt, error) {
	t.writes++
	return terminal.OwnedReceipt{Completed: true, Evidence: t.Snapshot()}, nil
}
func TestWriteEvidenceActivationKeepsFreshOwnerBudgetAndOwnedStream(t *testing.T) {
	for _, mode := range []string{"success", "lost-owner", "unknown-history", "exhausted-history"} {
		t.Run(mode, func(t *testing.T) {
			r, a, _, _, _ := writeEvidenceFixture(t)
			defer r.Close()
			cleanupModelApplyPlan(t, *r.boundPlan, nil, &a.inv)
			for i, op := range r.operations {
				if op.Phase == routeros.Activate {
					r.operationIndex = i
					break
				}
			}
			base := r.terminal.(*fakeTerminal)
			term := &evidenceOwnedTerminal{fakeTerminal: base}
			r.terminal = term
			r.profile.ActivationVerified = true
			r.profile.MaxHistoryActions = 80
			r.taken = true
			r.deadline = time.Now().Add(30 * time.Second)
			base.evidence.Mode = terminal.SafeOwned
			base.evidence.Owner = terminal.OwnerThisSession
			a.inv.SafeMode = &api.SafeModeState{Enabled: true}
			q := control.ReadRequest{KnownState: nativeExtensionDesired(*r.boundPlan)}
			o, err := r.Observe(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			m, err := r.bridge.Mutation(r.operations[r.operationIndex].ID, o.Resolver)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "lost-owner":
				base.evidence.Owner = terminal.OwnerNone
				base.evidence.Mode = terminal.Released
			case "unknown-history":
				base.historyKnown = false
			case "exhausted-history":
				base.history = 80
			}
			before, queries := a.calls, base.queries
			result, err := r.Apply(context.Background(), m)
			if mode == "success" {
				if err != nil || !result.Acknowledged || term.writes != 1 || a.writes != 0 || a.calls != before || base.queries <= queries {
					t.Fatal("reuse bypassed owned stream/owner check or repeated inventory", err)
				}
			} else if err == nil || term.writes != 0 || a.writes != 0 {
				t.Fatal("stale owner/budget was accepted")
			}
			if r.writeEvidence.Load() != nil {
				t.Fatal("activation attempt retained authority")
			}
		})
	}
}
