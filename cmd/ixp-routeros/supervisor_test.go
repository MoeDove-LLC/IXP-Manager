package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/live"
	"github.com/moedove/ixp-manager/internal/routing"
)

type budgetReviewRuntime struct {
	*readonlyReviewRuntime
	remaining time.Duration
}

func (r *budgetReviewRuntime) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return control.Observation{}, context.DeadlineExceeded
	}
	r.remaining = time.Until(deadline)
	return r.readonlyReviewRuntime.Observe(ctx, request)
}

func TestPrivateSupervisorPolicyReachesControllerObservationAndAdmission(t *testing.T) {
	for _, test := range []struct {
		name                    string
		budgets                 live.SupervisorConfig
		operation, confirmation int
	}{
		{"defaults", live.SupervisorConfig{}, 10, 300},
		{"tested-large-fixture", live.SupervisorConfig{OperationTimeoutSeconds: 30, PrepareTimeoutSeconds: 420, MaxConfirmationTimeoutSeconds: 120}, 30, 120},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &budgetReviewRuntime{readonlyReviewRuntime: &readonlyReviewRuntime{blockedRuntime: &blockedRuntime{target: cliTarget()}}}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			supervisor, err := openSupervisor(live.Config{Supervisor: test.budgets}, dir, runtime)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			if _, err := supervisor.Review(context.Background()); err != nil {
				t.Fatal(err)
			}
			limit := time.Duration(test.operation) * time.Second
			if runtime.remaining <= limit-2*time.Second || runtime.remaining > limit {
				t.Fatal("service ignored configured controller observation budget", runtime.remaining)
			}
			router := routing.Router{ID: runtime.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "192.0.2.1"}
			plan, err := routeros.BuildPlan(runtime.target, router, nil, routeros.Snapshot{Target: runtime.target, Owner: routeros.OwnerName(runtime.target.RouterID), NamespaceEmpty: true}, routeros.PlanOptions{Bootstrap: true})
			if err != nil {
				t.Fatal(err)
			}
			request := control.Request{Plan: plan, KnownState: routeros.KnownState{Target: runtime.target}, Timeout: time.Duration(test.confirmation+1) * time.Second, ExclusiveAccessAcknowledged: true}
			_, err = supervisor.Stage(context.Background(), request)
			var denied *control.Failure
			if !errors.As(err, &denied) || denied.Code != "confirmation-timeout-outside-bounds" {
				t.Fatal("request could exceed private confirmation limit", err)
			}
			request.Timeout = 90 * time.Second
			_, err = supervisor.Stage(context.Background(), request)
			var failure *control.Failure
			if !errors.As(err, &failure) || failure.Code != "native-adapter-gates-unverified" || runtime.writes != 0 {
				t.Fatal("private service budgets opened native gates or changed approved confirmation window", err)
			}
		})
	}
}

func TestSocketStageCannotOverridePrivateSupervisorPolicy(t *testing.T) {
	for _, extra := range []string{
		`"supervisor":{"operationTimeoutSeconds":30}`,
		`"supervisor":{"manualCleanup":true}`, `"manualCleanup":true`,
		`"operationTimeoutSeconds":30`, `"prepareTimeoutSeconds":900`,
		`"maxConfirmationTimeoutSeconds":300`,
	} {
		var input request
		raw := []byte(`{"version":1,"action":"stage","stage":{"timeoutSeconds":90,"exclusiveAccessAcknowledged":true,` + extra + `}}`)
		if strictDecode(raw, &input) == nil {
			t.Fatal("stage payload accepted a service-budget override", extra)
		}
	}
	var stage stageWire
	if err := json.Unmarshal([]byte(`{"timeoutSeconds":90,"exclusiveAccessAcknowledged":true}`), &stage); err != nil || stage.TimeoutSeconds != 90 {
		t.Fatal("ordinary requested confirmation window changed", err)
	}
}
