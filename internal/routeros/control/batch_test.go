package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type batchRuntime struct {
	*memoryRuntime
	batchCalls        int
	batchIDs          []string
	failAt            int
	corrupt           bool
	failBatchReadback bool
	batchFinished     bool
}

func (r *batchRuntime) Observe(ctx context.Context, request ReadRequest) (Observation, error) {
	if r.failBatchReadback && r.batchFinished {
		return Observation{}, errors.New("fixture read outage")
	}
	return r.memoryRuntime.Observe(ctx, request)
}

func (r *batchRuntime) Binding() Binding {
	b := r.memoryRuntime.Binding()
	b.RuleBatchVerified = true
	return b
}
func (r *batchRuntime) ApplyRuleBatch(ctx context.Context, b RuleBatch, recorder RuleBatchRecorder) error {
	r.batchCalls++
	for i, m := range b.Operations {
		r.batchIDs = append(r.batchIDs, m.OperationID)
		if m.Phase != routeros.Prepare || m.Request.Resource != api.FilterRule || m.Request.Action != api.Add {
			return errors.New("not rule add")
		}
		if err := recorder.Before(m.OperationID); err != nil {
			return err
		}
		if r.failAt == i+1 {
			r.failOperation = m.OperationID
		}
		result, writeErr := r.memoryRuntime.Apply(ctx, m)
		if err := recorder.After(m.OperationID, result, writeErr); err != nil {
			return err
		}
		if writeErr != nil {
			return writeErr
		}
	}
	if r.corrupt {
		r.mu.Lock()
		r.inv.FilterRules[0]["rule"] = "accept;"
		r.mu.Unlock()
	}
	r.batchFinished = true
	return nil
}
func TestRuleBatchExcludesRootsAndPublishesOnlyExactGroup(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &batchRuntime{memoryRuntime: model}
	s, err := Open(testConfig(t, model), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	status, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != AwaitingConfirmation || r.batchCalls != 1 || len(r.batchIDs) != 3 || s.batch != nil {
		t.Fatal("generation batch not fully proved")
	}
	for _, id := range r.batchIDs {
		for _, op := range request.Plan.Operations {
			if op.ID == id && (op.Rule == nil || op.Rule.Chain == request.Plan.Owner+"_root_in" || op.Rule.Chain == request.Plan.Owner+"_root_out") {
				t.Fatal("entry root batched")
			}
		}
	}
}
func TestRuleBatchLostACKStopsWithoutRetryOrActivation(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &batchRuntime{memoryRuntime: model, failAt: 2}
	config := testConfig(t, model)
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("unknown batch outcome accepted")
	}
	r.mu.Lock()
	if r.takeCount != 0 || len(r.batchIDs) != 2 || r.byOperation[r.batchIDs[1]] != 1 {
		t.Fatal("unknown write repeated or activated")
	}
	r.mu.Unlock()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.Recover(context.Background())
	if err != nil || status.Phase != PreparationFailed {
		t.Fatalf("readonly partial group proof: %+v %v", status, err)
	}
	if r.batchCalls != 1 {
		t.Fatal("batch retried after restart")
	}
}
func TestRuleBatchWALFailurePreventsNativeWrite(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &batchRuntime{memoryRuntime: model}
	s, err := Open(testConfig(t, model), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.log.fail = func(kind string) error {
		if kind == "rule-batch-intent" {
			return errors.New("disk fixture")
		}
		return nil
	}
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("batch intent not durable")
	}
	for _, id := range r.batchIDs {
		if r.byOperation[id] != 0 {
			t.Fatal("native rule add preceded durable intent")
		}
	}
}
func TestRuleBatchRejectsUnexpectedExistingRuleChange(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &batchRuntime{memoryRuntime: model, corrupt: true}
	s, err := Open(testConfig(t, model), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("batch accepted unrelated rule change")
	}
	if r.takeCount != 0 {
		t.Fatal("unproven group activated")
	}
}

func TestRuleBatchCrashBeforeGroupReadbackRecoversOnlyByObservation(t *testing.T) {
	model, request := fixtureRuntime(t)
	r := &batchRuntime{memoryRuntime: model, failBatchReadback: true}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("missing whole group proof accepted")
	}
	if s.batch == nil || len(s.batch.Steps) != 3 {
		t.Fatal("per-operation receipts lost before group checkpoint")
	}
	s.Close()
	r.failBatchReadback = false
	reopened, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.Recover(context.Background())
	if err != nil || status.Phase != PreparationFailed || reopened.batch != nil {
		t.Fatalf("group recovery failed: %+v %v", status, err)
	}
	if r.batchCalls != 1 || r.takeCount != 0 {
		t.Fatal("read-only recovery repeated writes or activated")
	}
}
