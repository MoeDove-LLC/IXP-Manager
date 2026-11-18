package live

import (
	"context"
	"fmt"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type observerBatchAPI struct {
	*fakeAPI
	batches int
	largest int
	items   int
}

func (a *observerBatchAPI) ReadProperties(ctx context.Context, queries []api.PropertyRequest) ([]api.PropertyValue, error) {
	a.batches++
	a.items += len(queries)
	if len(queries) > a.largest {
		a.largest = len(queries)
	}
	values := make([]api.PropertyValue, len(queries))
	for i, q := range queries {
		v, e := a.fakeAPI.ReadProperty(ctx, q.Resource, q.ID, q.Property)
		if e != nil {
			return nil, e
		}
		values[i] = v
	}
	return values, nil
}
func TestObserverBatchesRealGetterEvidenceAndKeepsBothStableReads(t *testing.T) {
	r, a, _, request := observerFixture()
	defer r.Close()
	r.config.MaxObjects = 1000
	r.config.MaxGetterCalls = 1000
	for i := 1; i <= 130; i++ {
		id := fmt.Sprintf("*%X", i)
		a.inv.Tables = append(a.inv.Tables, api.Record{".id": id, "name": fmt.Sprintf("external-%d", i), "disabled": "false", "invalid": "false", "comment": ""})
		for _, field := range []string{"fib", "dynamic"} {
			a.properties["routing-table/"+id+"/"+field] = api.PropertyValue{Known: false}
		}
	}
	batched := &observerBatchAPI{fakeAPI: a}
	r.api = batched
	o, e := r.Observe(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	if !o.Snapshot.NamespaceEmpty || a.calls != 2 || batched.items != 520 || batched.batches != 10 || batched.largest != api.MaxPropertyBatch {
		t.Fatal("two complete reads were not bounded getter batches")
	}
	if o.Inventory.Tables[0]["fib"] != "false" {
		t.Fatal("actual no-ret version evidence not normalized")
	}
	if _, changed := a.inv.Tables[0]["fib"]; changed {
		t.Fatal("raw print evidence mutated")
	}
}
