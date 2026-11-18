package routing

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestJunosManagedGroupAndPolicies(t *testing.T) {
	r, p := fixture()
	config, err := renderJunos(r, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"<configuration", "operation=\"replace\"", "<apply-groups>", "<route-server-client>", "<accepted-prefix-limit>", "<maximum>100</maximum>", "<exact>", "<reject>", "64513 .{0,63}", "<inet6>"} {
		if !strings.Contains(config, required) {
			t.Errorf("missing %s", required)
		}
	}
	if strings.Contains(config, "Untrusted name") {
		t.Fatal("untrusted name in configuration")
	}
	p[0], p[1] = p[1], p[0]
	again, err := renderJunos(r, p)
	if err != nil || again != config {
		t.Fatal("configuration ordering unstable")
	}
	var tree struct {
		Groups struct {
			Policy []struct {
				Name  string `xml:"name"`
				Terms []struct {
					From struct {
						Neighbors []string `xml:"neighbor"`
					} `xml:"from"`
				} `xml:"term"`
			} `xml:"policy-statement"`
		} `xml:"groups>policy-options"`
	}
	if err = xml.Unmarshal([]byte(config), &tree); err != nil {
		t.Fatal(err)
	}
	for _, policy := range tree.Groups.Policy {
		if strings.HasSuffix(policy.Name, "_export") {
			for _, term := range policy.Terms {
				if len(term.From.Neighbors) > 0 {
					t.Fatal("Junos ignores neighbor on BGP export")
				}
			}
		}
	}
}
func TestJunosStructuralValidationIsStrict(t *testing.T) {
	for _, bad := range []string{`<!DOCTYPE config><configuration/>`, `<?x y?><configuration/>`, `<configuration/><configuration/>`, `<configuration><system/></configuration>`, `<configuration><groups><name>x</name></groups><apply-groups>x</apply-groups><routing-options/></configuration>`} {
		if err := ValidateJunosXML(bad); err == nil {
			t.Fatalf("invalid artifact accepted: %s", bad)
		}
	}
}
