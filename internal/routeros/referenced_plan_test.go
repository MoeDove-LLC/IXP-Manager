package routeros

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceOnlyPlanMatchesCredentialPlanWithoutStoringValues(t *testing.T) {
	target, router, peers, base := fixture()
	peers[0].Password = "Private_reference_fixture_123"
	peers[0].AuthenticationRevision = strings.Repeat("A", 22)
	ordinary, err := BuildPlan(target, router, peers, base, PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	referenced, err := BuildReferencedPlan(target, ordinary.Desired, base, PlanOptions{Bootstrap: true})
	if err != nil || !reflect.DeepEqual(ordinary, referenced) || ValidatePlan(referenced) != nil {
		t.Fatal("referenced plan diverged from canonical compiler input", err)
	}
	encoded, _ := json.Marshal(referenced)
	if strings.Contains(string(encoded), peers[0].Password) || strings.Contains(string(encoded), "structuralplaceholder") || referenced.NativeValidated {
		t.Fatal("planning leaked a value or granted native validation")
	}
	if _, err := Compile(referenced, nil); err == nil {
		t.Fatal("credential references became executable secret values")
	}
}
