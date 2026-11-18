package routeros

import (
	"strings"
	"testing"
)

func TestOpaqueDeviceIDPermitsMeasuredBase64Characters(t *testing.T) {
	for _, id := range []string{"/ABCdef123+A", "+ABCdef123/A", "ABC/def+123A"} {
		target, router, peers, base := fixture()
		target.DeviceID = id
		base.Target = target
		plan, err := BuildPlan(target, router, peers, base, PlanOptions{Bootstrap: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidatePlan(plan); err != nil {
			t.Fatal(err)
		}
		compiled, err := Compile(plan, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range compiled.Commands {
			for _, a := range cmd.Attributes {
				if strings.Contains(a.Value, id) {
					t.Fatal("opaque device identity entered native command attributes")
				}
			}
		}
		if plan.Owner != OwnerName(router.ID) {
			t.Fatal("device ID altered managed namespace")
		}
	}
	for _, id := range []string{"", " leading", "trailing ", "line" + string(rune(10)) + "break", "tab" + string(rune(9)) + "value", "bad;command", "bad" + string(rune(34)) + "quote", strings.Repeat("a", 129)} {
		target, _, _, _ := fixture()
		target.DeviceID = id
		if err := validateTarget(target); err == nil {
			t.Fatal("unsafe or unbounded device identity accepted")
		}
	}
}
