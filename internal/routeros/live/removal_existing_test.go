package live

import (
	"context"
	"errors"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

type removalExistingAPIModel struct {
	*removalBatchAPIModel
	objects map[string]int
	queries map[string]int
	failure string
}

func (a *removalExistingAPIModel) ReadObjectPresence(ctx context.Context, resource api.Resource, id string) (api.ObjectPresence, error) {
	a.queries[cleanupKey(resource, id)]++
	return a.removalBatchAPIModel.ReadObjectPresence(ctx, resource, id)
}
func (a *removalExistingAPIModel) ReadObject(ctx context.Context, resource api.Resource, id string) (api.Record, error) {
	if resource != api.FilterRule {
		return nil, errors.New("only directly verified rule selection is allowed")
	}
	a.objects[cleanupKey(resource, id)]++
	if a.failure == "trap" {
		return nil, &api.Error{Code: api.CodeTrap, Resource: resource}
	}
	if a.failure == "nil" {
		return nil, nil
	}
	value, err := a.removalBatchAPIModel.ReadObjectPresence(ctx, resource, id)
	if err != nil || !value.Present {
		return nil, errors.New("existing object unavailable")
	}
	if a.failure == "wrong-id" {
		value.Record[".id"] = "*BAD"
	}
	return value.Record, nil
}

func TestRemovalDirectExistingReadPreservesIndependentAbsence(t *testing.T) {
	for _, mode := range []control.RuleRemovalMode{control.RuleRemovalCleanup, control.RuleRemovalPreparedDiscard} {
		t.Run(string(mode), func(t *testing.T) {
			f := newRemovalBatchFixture(t, mode)
			adapter := &removalExistingAPIModel{removalBatchAPIModel: f.api, objects: map[string]int{}, queries: map[string]int{}}
			f.r.api = adapter
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err != nil {
				t.Fatal(err)
			}
			for _, mutation := range batch.Operations {
				key := cleanupKey(api.FilterRule, mutation.Request.ID)
				if adapter.objects[key] != 2 || adapter.queries[key] != 2 {
					t.Fatal("actual present/absent query boundaries changed", adapter.objects[key], adapter.queries[key])
				}
			}
			if f.api.base.writes-f.initialWrites != 2 || len(recorder.absent) != 2 {
				t.Fatal("direct selection replaced durable removal proof")
			}
		})
	}
}

func TestRemovalDirectExistingFailureNeverBecomesAbsenceOrFallback(t *testing.T) {
	for _, failure := range []string{"trap", "nil", "wrong-id"} {
		t.Run(failure, func(t *testing.T) {
			f := newRemovalBatchFixture(t, control.RuleRemovalPreparedDiscard)
			adapter := &removalExistingAPIModel{removalBatchAPIModel: f.api, objects: map[string]int{}, queries: map[string]int{}, failure: failure}
			f.r.api = adapter
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), removalBatchPrefix(f.batch, 2), recorder); err == nil {
				t.Fatal("invalid direct object read was accepted")
			}
			if f.api.base.writes != f.initialWrites || len(recorder.before) != 0 || len(recorder.absent) != 0 || len(adapter.queries) != 0 {
				t.Fatal("failed direct read caused a write or fabricated absence")
			}
		})
	}
}
