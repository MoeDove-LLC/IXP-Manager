package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routing"
)

type resetRuntime struct {
	*memoryRuntime
	resets int
	reject bool
}

func (r *resetRuntime) ResetIdle(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets++
	if r.reject || r.owner.Active {
		return errors.New("reset not permitted")
	}
	r.owner = Owner{Known: true, Alive: true, HistoryKnown: true}
	return nil
}
func TestResetIdleOnlyAtNewStageBoundary(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &resetRuntime{memoryRuntime: model}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if r.resets != 1 {
		t.Fatal("new explicit stage did not reset idle channel")
	}
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("pending stage accepted")
	}
	if r.resets != 1 {
		t.Fatal("pending transaction reset its owner")
	}
	_, _ = s.Recover(context.Background())
	if r.resets != 1 {
		t.Fatal("read-only recover reset its owner")
	}
	if _, err = s.Confirm(context.Background(), status.TransactionID, status.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	if r.resets != 1 {
		t.Fatal("confirm changed sessions")
	}
}
func TestResetIdleFailureAndUnverifiedGateHaveNoWrites(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &resetRuntime{memoryRuntime: model, reject: true}
	s, err := Open(testConfig(t, model), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("rejected reset ignored")
	}
	if model.applyCount != 0 || model.takeCount != 0 || s.Status().Phase != Idle {
		t.Fatal("failed reset started native transaction")
	}
	r.reject = false
	r.gates = false
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("unverified profile accepted")
	}
	if r.resets != 1 {
		t.Fatal("unverified adapter reached reset")
	}
}

func TestRejectedBaselineDoesNotLeaveIdleDriverBound(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &resetRuntime{memoryRuntime: model}
	s, err := Open(testConfig(t, model), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The separately reviewed baseline differs from the actual strict read.
	original := request.Plan.Base.CrossChannelIdentityVerified
	target, router := request.Plan.Target, request.Plan.Desired
	base := request.Plan.Base
	base.CrossChannelIdentityVerified = !original
	changed, err := routeros.BuildPlan(target, routing.Router{ID: target.RouterID, ASN: router.ASN, RouterID: router.RouterID, LocalIPv4: router.LocalIPv4, LocalIPv6: router.LocalIPv6}, nil, base, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	bad := request
	bad.Plan = changed
	if _, err = s.Stage(context.Background(), bad); err == nil {
		t.Fatal("changed reviewed baseline accepted")
	}
	if model.bindCount != 0 || s.Status().Phase != Idle {
		t.Fatal("failed read-only preflight left a bound transaction")
	}
	if _, err = s.Stage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if model.bindCount != 1 {
		t.Fatal("successful stage was not bound exactly once")
	}
}
