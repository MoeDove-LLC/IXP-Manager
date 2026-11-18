package netconf

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type step struct {
	op, reply string
	wrongID   bool
}
type script struct {
	t       *testing.T
	steps   []step
	caps    string
	pending bytes.Buffer
	writes  []string
	closed  bool
}

func (s *script) Read(b []byte) (int, error) { return s.pending.Read(b) }
func (s *script) Close() error               { s.closed = true; return nil }
func (s *script) Write(b []byte) (int, error) {
	raw := strings.TrimSuffix(string(b), delimiter)
	s.writes = append(s.writes, raw)
	var envelope struct {
		XMLName xml.Name
		ID      string `xml:"message-id,attr"`
		Body    string `xml:",innerxml"`
	}
	if err := xml.Unmarshal([]byte(raw), &envelope); err != nil {
		s.t.Fatal(err)
	}
	if envelope.XMLName.Local == "hello" {
		if !strings.Contains(envelope.Body, "<capability>urn:ietf:params:netconf:base:1.0</capability>") {
			s.t.Fatal("hello must advertise protocol capability URI, not XML namespace")
		}
		caps := s.caps
		if caps == "" {
			caps = candidate + "|" + validate11 + "|" + confirmed
		}
		s.pending.WriteString(`<hello xmlns="` + Base + `"><capabilities><capability>` + BaseCapability + `</capability>`)
		for _, c := range strings.Split(caps, "|") {
			s.pending.WriteString(`<capability>` + c + `</capability>`)
		}
		s.pending.WriteString(`</capabilities></hello>` + delimiter)
		return len(b), nil
	}
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected RPC: %s", raw)
	}
	st := s.steps[0]
	s.steps = s.steps[1:]
	decoder := xml.NewDecoder(strings.NewReader(envelope.Body))
	first, _ := decoder.Token()
	name := first.(xml.StartElement).Name.Local
	if name != st.op {
		s.t.Fatalf("got RPC %s want %s", name, st.op)
	}
	id := envelope.ID
	if st.wrongID {
		id = "999"
	}
	reply := st.reply
	if reply == "" {
		reply = "<ok/>"
	}
	fmt.Fprintf(&s.pending, `<rpc-reply xmlns="%s" message-id="%s">%s</rpc-reply>%s`, Base, id, reply, delimiter)
	return len(b), nil
}
func fake(t *testing.T, steps []step) (*Client, *script) {
	t.Helper()
	s := &script{t: t, steps: steps}
	c, err := Open(s)
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

const clean = `<data><configuration xmlns=""><system><host-name>router</host-name></system></configuration></data>`
const artifact = `<configuration xmlns=""><groups xmlns:nc="urn:ietf:params:xml:ns:netconf:base:1.0" nc:operation="replace"><name>moedove_test</name></groups></configuration>`

var testToken = strings.Repeat("ab", 32)

func begin() []step {
	return []step{{op: "lock"}, {op: "get-config", reply: clean}, {op: "get-config", reply: clean}}
}
func TestStageConfirmedCommitWithoutImplicitConfirmation(t *testing.T) {
	steps := append(begin(), step{op: "edit-config"}, step{op: "validate"}, step{op: "commit"}, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Stage(artifact, checksum(artifact), testToken, 300); err != nil {
		t.Fatal(err)
	}
	if len(s.steps) != 0 {
		t.Fatal("incomplete transaction")
	}
	all := strings.Join(s.writes, "\n")
	if !strings.Contains(all, "<confirmed/>") || !strings.Contains(all, "<persist>"+testToken) || strings.Contains(all, "<persist-id>") {
		t.Fatal("must stage persist token without confirming")
	}
}
func TestDirtyCandidateIsNeverEditedOrDiscarded(t *testing.T) {
	steps := begin()
	steps[2].reply = `<data><configuration xmlns=""><system><host-name>someone-else</host-name></system></configuration></data>`
	steps = append(steps, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Stage(artifact, checksum(artifact), testToken, 300); err == nil {
		t.Fatal("accepted unrelated candidate changes")
	}
	all := strings.Join(s.writes, "")
	if strings.Contains(all, "discard-changes") || strings.Contains(all, "edit-config") || strings.Contains(all, "<commit>") {
		t.Fatal("modified unrelated candidate")
	}
}
func TestNativeValidationFailureDiscardsOnlyOurCandidate(t *testing.T) {
	steps := append(begin(), step{op: "edit-config"}, step{op: "validate", reply: `<rpc-error><error-tag>invalid-value</error-tag><error-message>unsupported policy</error-message></rpc-error>`}, step{op: "discard-changes"}, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Stage(artifact, checksum(artifact), testToken, 300); err == nil {
		t.Fatal("native rejection ignored")
	}
	if len(s.steps) != 0 {
		t.Fatal("cleanup not executed")
	}
	if strings.Contains(strings.Join(s.writes, ""), "<commit>") {
		t.Fatal("committed after rejected validation")
	}
}

func TestDeviceErrorsCannotEchoCredentials(t *testing.T) {
	const secret = "PrivateBGPCredential_123"
	for _, reply := range []string{
		`<rpc-error><error-tag>invalid-value</error-tag><error-message>authentication-key ` + secret + ` rejected</error-message></rpc-error>`,
		`<rpc-error><error-tag>` + secret + `</error-tag><error-message>raw config</error-message></rpc-error>`,
		`<rpc-error><` + secret + `></broken>`,
	} {
		steps := append(begin(), step{op: "edit-config"}, step{op: "validate", reply: reply}, step{op: "discard-changes"}, step{op: "unlock"})
		c, s := fake(t, steps)
		err := c.Stage(artifact, checksum(artifact), testToken, 300)
		if err == nil || strings.Contains(err.Error(), secret) || len(s.steps) != 0 {
			t.Fatal("device failure leaked credential or skipped candidate cleanup")
		}
	}
	for _, parse := range []func(string) error{
		configXML,
		func(raw string) error { _, err := canonical(raw); return err },
		func(raw string) error { _, _, err := explicitGlobals(raw); return err },
	} {
		if err := parse("<configuration><" + secret + "></broken>"); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("XML parser error exposed a credential")
		}
	}
}
func TestCommitErrorDiscardsCandidate(t *testing.T) {
	steps := append(begin(), step{op: "edit-config"}, step{op: "validate"}, step{op: "commit", reply: `<rpc-error><error-tag>operation-failed</error-tag></rpc-error>`}, step{op: "discard-changes"}, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Stage(artifact, checksum(artifact), testToken, 300); err == nil || len(s.steps) != 0 {
		t.Fatalf("commit rejection cleanup: %v", err)
	}
}
func TestConfirmLocksAndChecksCandidate(t *testing.T) {
	steps := append(begin(), step{op: "commit"}, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Confirm(testToken); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(s.writes, "")
	if !strings.Contains(all, "<persist-id>"+testToken) || strings.Contains(all, "<confirmed/>") {
		t.Fatal("incorrect confirmation")
	}
}
func TestCapabilityMessageIDAndChecksumFailClosed(t *testing.T) {
	s := &script{t: t, caps: candidate}
	c, err := Open(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Stage(artifact, checksum(artifact), testToken, 300); err == nil || len(s.writes) != 1 {
		t.Fatal("unsupported capabilities caused mutation")
	}
	c, s = fake(t, nil)
	if err = c.Stage(artifact, checksum("changed"), testToken, 300); err == nil || len(s.writes) != 1 {
		t.Fatal("unreviewed bytes sent")
	}
	c, _ = fake(t, []step{{op: "lock", wrongID: true}})
	if err = c.Stage(artifact, checksum(artifact), testToken, 300); err == nil {
		t.Fatal("reply message-id mismatch accepted")
	}
}
func TestCancelUsesPersistID(t *testing.T) {
	c, s := fake(t, []step{{op: "cancel-commit"}})
	if err := c.Cancel(testToken); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(s.writes, ""), "<persist-id>"+testToken) {
		t.Fatal("cancel token missing")
	}
}
func TestXMLAndCanonicalComparison(t *testing.T) {
	for _, s := range []string{`<!DOCTYPE a><configuration/>`, `<configuration/><rpc/>`, `<rpc/>`, artifact + delimiter} {
		if err := configXML(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	a, err := canonical(`<configuration><x a="1" b="2"/></configuration>`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := canonical("<configuration>\n <x b=\"2\" a=\"1\"></x>\n</configuration>")
	if a != b {
		t.Fatal("formatting falsely dirties candidate")
	}
}
func TestSSHArgumentsPinHostAndDisableShellHooks(t *testing.T) {
	dir := t.TempDir()
	known := filepath.Join(dir, "known_hosts")
	key := filepath.Join(dir, "identity")
	os.WriteFile(known, []byte("pinned"), 0600)
	os.WriteFile(key, []byte("private"), 0600)
	options := SSHOptions{Host: "router.example", User: "netconf", Port: 830, KnownHosts: known, Identity: key}
	args, err := SSHArgs(options)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"StrictHostKeyChecking=yes", "BatchMode=yes", "ProxyCommand=none", "-F /dev/null", "-s -- router.example netconf"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %s", required)
		}
	}
	options.Host = "-oProxyCommand=touch /tmp/pwn"
	if _, err = SSHArgs(options); err == nil {
		t.Fatal("option injection accepted")
	}
	options.Host = "router.example"
	options.KnownHosts = filepath.Join(dir, "missing")
	if _, err = SSHArgs(options); err == nil {
		t.Fatal("missing pinned keys accepted")
	}
}
func TestFramingTruncation(t *testing.T) {
	c := &Client{reader: bufio.NewReader(strings.NewReader("<hello>"))}
	if _, err := c.read(); err != io.EOF {
		t.Fatalf("truncated frame: %v", err)
	}
}

func TestConfirmRefusesNewCandidateEdits(t *testing.T) {
	steps := begin()
	steps[2].reply = `<data><configuration xmlns=""><system><host-name>unrelated-change</host-name></system></configuration></data>`
	steps = append(steps, step{op: "unlock"})
	c, s := fake(t, steps)
	if err := c.Confirm(testToken); err == nil {
		t.Fatal("confirmation committed unrelated candidate changes")
	}
	all := strings.Join(s.writes, "")
	if strings.Contains(all, "<commit>") || strings.Contains(all, "discard-changes") {
		t.Fatal("confirmation altered dirty candidate")
	}
}
func TestMissingGetConfigDataRefused(t *testing.T) {
	for _, mode := range []string{"stage", "confirm"} {
		for _, bad := range []string{"<ok/>", "<data/>", `<data xmlns="urn:wrong"><configuration/></data>`} {
			t.Run(mode+bad, func(t *testing.T) {
				steps := []step{{op: "lock"}, {op: "get-config", reply: clean}, {op: "get-config", reply: bad}, {op: "unlock"}}
				c, s := fake(t, steps)
				var err error
				if mode == "stage" {
					err = c.Stage(artifact, checksum(artifact), testToken, 300)
				} else {
					err = c.Confirm(testToken)
				}
				if err == nil {
					t.Fatal("missing, empty or foreign get-config data treated as clean candidate")
				}
				all := strings.Join(s.writes, "")
				if strings.Contains(all, "edit-config") || strings.Contains(all, "<commit>") || strings.Contains(all, "discard-changes") {
					t.Fatal("mutated device without verified baseline")
				}
				if len(s.steps) != 0 {
					t.Fatal("candidate was not unlocked")
				}
			})
		}
	}
}

func TestForeignNamespaceOKRejected(t *testing.T) {
	c, _ := fake(t, []step{{op: "cancel-commit", reply: `<ok xmlns="urn:unexpected"/>`}})
	if err := c.Cancel(testToken); err == nil {
		t.Fatal("foreign namespace acknowledgement accepted")
	}
}

func TestStageGlobalIdentityGuard(t *testing.T) {
	running := `<data><configuration xmlns=""><routing-options><router-id>192.0.2.1</router-id><autonomous-system><as-number>64512</as-number></autonomous-system></routing-options></configuration></data>`
	for _, tc := range []struct {
		name, id, asn   string
		allow, wantPass bool
	}{
		{"matching", "192.0.2.1", "64512", false, true},
		{"matching_asdot", "192.0.2.1", "0.64512", false, true},
		{"router_id_change", "192.0.2.2", "64512", false, false},
		{"asn_change", "192.0.2.1", "64513", false, false},
		{"explicit_override", "192.0.2.2", "64513", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proposal := `<configuration xmlns=""><routing-options><router-id>` + tc.id + `</router-id><autonomous-system><as-number>` + tc.asn + `</as-number></autonomous-system></routing-options></configuration>`
			steps := []step{{op: "lock"}, {op: "get-config", reply: running}, {op: "get-config", reply: running}}
			if tc.wantPass {
				steps = append(steps, step{op: "edit-config"}, step{op: "validate"}, step{op: "commit"})
			}
			steps = append(steps, step{op: "unlock"})
			c, s := fake(t, steps)
			var err error
			if tc.allow {
				err = c.StageWithOptions(proposal, checksum(proposal), testToken, 300, StageOptions{AllowGlobalRoutingChange: true})
			} else {
				err = c.Stage(proposal, checksum(proposal), testToken, 300)
			}
			if (err == nil) != tc.wantPass {
				t.Fatalf("allowed=%v expected=%v error=%v", err == nil, tc.wantPass, err)
			}
			if !tc.wantPass {
				all := strings.Join(s.writes, "")
				if strings.Contains(all, "edit-config") || strings.Contains(all, "<commit>") || strings.Contains(all, "discard-changes") {
					t.Fatal("global guard changed device")
				}
			}
			if len(s.steps) != 0 {
				t.Fatal("transaction not completed/unlocked")
			}
		})
	}
}
