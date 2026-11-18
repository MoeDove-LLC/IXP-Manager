package live

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type batchTestAPI struct {
	*fakeAPI
	ownerReads int
	ownerAt    int
	failAt     int
	events     *[]string
}

func (a *batchTestAPI) ReadSafeMode(context.Context) (api.SafeModeState, error) {
	a.ownerReads++
	return api.SafeModeState{Enabled: a.ownerAt > 0 && a.ownerReads >= a.ownerAt}, nil
}
func (a *batchTestAPI) Apply(_ context.Context, m api.Mutation) (api.Result, error) {
	a.writes++
	*a.events = append(*a.events, "write")
	if a.writes == a.failAt {
		return api.Result{}, &api.Error{Code: api.CodeTransport, Uncertain: true}
	}
	return api.Result{Acknowledged: true, ID: fmt.Sprintf("*%X", 1000+a.writes)}, nil
}

type batchTestRecorder struct {
	events    *[]string
	before    int
	after     int
	failAt    int
	writeFail bool
}

func (s *batchTestRecorder) Before(string) error {
	s.before++
	*s.events = append(*s.events, "intent")
	if s.before == s.failAt {
		return errors.New("fixture journal unavailable")
	}
	return nil
}
func (s *batchTestRecorder) After(_ string, _ api.Result, e error) error {
	s.after++
	s.writeFail = s.writeFail || e != nil
	*s.events = append(*s.events, "result")
	return nil
}

func ruleBatchFixture(t *testing.T) (*Runtime, *batchTestAPI, control.RuleBatch, *batchTestRecorder) {
	t.Helper()
	r, a, _, request := observerFixture()
	observed, e := r.Observe(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	p, e := routeros.BuildPlan(r.target, routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}, []routing.Peer{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}, *observed.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if e != nil || r.BindPlan(p, nil) != nil {
		t.Fatal("bind fixture", e)
	}
	r.profile.PreparationVerified, r.profile.RuleBatchVerified = true, true
	batch := control.RuleBatch{ID: "fixture-batch", PlanChecksum: p.Checksum, Generation: p.Generation}
	for i, op := range p.Operations {
		if generationRule(&p, op) {
			if len(batch.Operations) == 0 {
				r.operationIndex = i
			}
			command, e := r.bridge.Mutation(op.ID, observed.Resolver)
			if e != nil {
				t.Fatal(e)
			}
			batch.Operations = append(batch.Operations, command)
		}
	}
	events := []string{}
	wrapped := &batchTestAPI{fakeAPI: a, events: &events}
	r.api = wrapped
	return r, wrapped, batch, &batchTestRecorder{events: &events}
}

func TestRuleBatchUsesOneSnapshotAndDurablePerWriteOrder(t *testing.T) {
	r, a, batch, recorder := ruleBatchFixture(t)
	defer r.Close()
	before, index := a.calls, r.operationIndex
	if e := r.ApplyRuleBatch(context.Background(), batch, recorder); e != nil {
		t.Fatal(e)
	}
	if a.calls-before != 1 || a.writes != len(batch.Operations) || a.ownerReads != a.writes || recorder.before != a.writes || recorder.after != a.writes || r.operationIndex != index+a.writes {
		t.Fatal("batch repeated full snapshots or lost a bounded proof/write step")
	}
	for i, event := range *a.events {
		if event != []string{"intent", "write", "result"}[i%3] {
			t.Fatal("API write preceded its durable intent")
		}
	}
}

func TestRuleBatchRejectsPartialChangedOrReferencedGenerationBeforeWrites(t *testing.T) {
	for _, mode := range []string{"gate", "partial", "wrong-generation", "changed-body", "root", "reference"} {
		t.Run(mode, func(t *testing.T) {
			r, a, batch, recorder := ruleBatchFixture(t)
			defer r.Close()
			switch mode {
			case "gate":
				r.profile.RuleBatchVerified = false
			case "partial":
				batch.Operations = batch.Operations[:1]
			case "wrong-generation":
				batch.Generation = "0000000000000000"
			case "changed-body":
				batch.Operations[0].Request.Attributes[0].Value = "foreign"
			case "root":
				r.operationIndex = 2
			case "reference":
				a.inv.FilterRules = []api.Record{{".id": "*1", "chain": "foreign", "comment": "", "rule": "jump " + generationChains(r.boundPlan)[0] + ";", "disabled": "false", "inactive": "false"}}
			}
			if e := r.ApplyRuleBatch(context.Background(), batch, recorder); e == nil || a.writes != 0 || recorder.before != 0 {
				t.Fatal("invalid batch reached a mutation", e)
			}
		})
	}
}

func TestRuleBatchStopsForIntentOwnerOrUnknownResultWithoutRetry(t *testing.T) {
	for _, mode := range []string{"intent", "owner", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			r, a, batch, recorder := ruleBatchFixture(t)
			defer r.Close()
			want := 0
			switch mode {
			case "intent":
				recorder.failAt = 1
			case "owner":
				a.ownerAt, want = 2, 1
			case "uncertain":
				a.failAt, want = 2, 2
			}
			if e := r.ApplyRuleBatch(context.Background(), batch, recorder); e == nil || a.writes != want {
				t.Fatal("failure did not stop the batch", e)
			}
			if mode == "uncertain" {
				if !r.poisoned || !recorder.writeFail {
					t.Fatal("uncertain outcome was not journalled and fenced")
				}
				if e := r.ApplyRuleBatch(context.Background(), batch, recorder); e == nil || a.writes != want {
					t.Fatal("uncertain write was retried")
				}
			}
		})
	}
}
