package netconf

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func junosAS112Artifact(t *testing.T) string {
	t.Helper()
	config, err := routing.RenderFor("junos", routing.Router{ID: "as112-router", Platform: "junos", Role: routing.RoleAS112, ASN: 112, RouterID: "192.0.2.112", LocalIPv4: "192.0.2.112", LocalIPv6: "2001:db8::112", AS112Service: "both", AS112IPv4: true, AS112IPv6: true, AS112NextHop4: "192.0.2.200", AS112NextHop6: "2001:db8::200"}, []routing.Peer{{ID: "peer", ASN: 64501, Address: "192.0.2.1", Enabled: true, MaxPrefixes: 10, Password: "Synthetic-AS112-Secret", AuthenticationRevision: "abcdefghijklmnopqrstuv"}})
	if err != nil {
		t.Fatal(err)
	}
	return config
}
func TestAS112NETCONFStageConfirmCancelAndHostRouteReadback(t *testing.T) {
	config := junosAS112Artifact(t)
	running := strings.ReplaceAll(config, ` nc:operation="replace"`, "")
	reply := "<data>" + running + "</data>"
	steps := append(begin(), step{op: "edit-config"}, step{op: "validate"}, step{op: "commit"}, step{op: "unlock"}, step{op: "lock"}, step{op: "get-config", reply: reply}, step{op: "get-config", reply: reply}, step{op: "commit"}, step{op: "unlock"}, step{op: "cancel-commit"})
	c, s := fake(t, steps)
	if err := c.Stage(config, checksum(config), testToken, 120); err != nil {
		t.Fatal("AS112 managed group was not accepted at actual NETCONF entry", err)
	}
	if err := c.Confirm(testToken); err != nil {
		t.Fatal("AS112 static/host subtree canonical readback failed", err)
	}
	if err := c.Cancel(testToken); err != nil {
		t.Fatal(err)
	}
	if len(s.steps) != 0 {
		t.Fatal("NETCONF entry sequence incomplete")
	}
	joined := strings.Join(s.writes, "\n")
	configurations := 0
	for _, wire := range s.writes {
		decoder := xml.NewDecoder(strings.NewReader(wire))
		for {
			value, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			if start, ok := value.(xml.StartElement); ok && start.Name.Local == "configuration" {
				configurations++
				if start.Name.Space != "" {
					t.Fatal("AS112 Junos payload inherited the NETCONF transport namespace")
				}
			}
		}
	}
	if configurations != 1 {
		t.Fatal("expected exactly one AS112 edit-config payload")
	}
	for _, needed := range []string{"<name>192.175.48.1/32</name><next-hop>192.0.2.200</next-hop>", "<name>2001:4:112::1/128</name><next-hop>2001:db8::200</next-hop>", "<persist>" + testToken, "<persist-id>" + testToken} {
		if !strings.Contains(joined, needed) {
			t.Fatal("AS112 target or persistent transaction binding was lost")
		}
	}
	changed := strings.Replace(reply, "<next-hop>192.0.2.200</next-hop>", "<next-hop>192.0.2.201</next-hop>", 1)
	other, script := fake(t, []step{{op: "lock"}, {op: "get-config", reply: reply}, {op: "get-config", reply: changed}, {op: "unlock"}})
	if err := other.Confirm(testToken); err == nil {
		t.Fatal("changed nested AS112 host route was ignored by canonical ownership check")
	}
	if strings.Contains(strings.Join(script.writes, ""), "<commit>") {
		t.Fatal("unrelated AS112 candidate edit committed")
	}
}
