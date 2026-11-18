package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

func TestJournalTornTailAndPrivateFileGuards(t *testing.T) {
	r, _ := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(config.Dir, "journal.wal"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{torn")
	f.Close()
	reopened := openTest(t, config, r)
	if !reopened.Status().RecoveryOnly || reopened.Status().Phase != Uncertain {
		t.Fatal("torn journal opened for new writes")
	}
	reopened.Close()
	// Recovery never truncates or repairs the source journal silently.
	data, _ := os.ReadFile(filepath.Join(config.Dir, "journal.wal"))
	if string(data) != "{torn" {
		t.Fatal("source journal modified during failed recovery")
	}
	if err = os.Chmod(filepath.Join(config.Dir, "journal.key"), 0644); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(config, r); err == nil {
		other.Close()
		t.Fatal("public journal key accepted")
	}
}

func TestJournalHMACAndNoPlaintextCredentials(t *testing.T) {
	r, _ := fixtureRuntime(t)
	config := testConfig(t, r)
	j, _, err := openJournal(config.Dir, r.target)
	if err != nil {
		t.Fatal(err)
	}
	secret := "low-entropy-secret-for-test"
	spec := specFor(j.key, api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: secret, Sensitive: true}}})
	if spec.Attributes["tcp-md5-key"] != "" || spec.CredentialDigest == "" || spec.CredentialDigest == routing.Checksum(secret) {
		t.Fatal("secret must use keyed fingerprint")
	}
	if err = j.append(event{Kind: "intent", OperationID: "op-000001", Spec: &spec}); err != nil {
		t.Fatal(err)
	}
	keyBefore := append([]byte(nil), j.key...)
	j.close()
	data, err := os.ReadFile(filepath.Join(config.Dir, "journal.wal"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("ordinary journal leaked plaintext password")
	}
	again, events, err := openJournal(config.Dir, r.target)
	if err != nil {
		t.Fatal(err)
	}
	if again.poisoned || len(events) != 1 || credentialMAC(again.key, "*A", []byte(secret)) != spec.CredentialDigest {
		t.Fatal("persistent key cannot verify old observation")
	}
	again.close()
	clear(keyBefore)
	// Flipping a syntactically valid frame value without its MAC is detected.
	var f frame
	if json.Unmarshal(data, &f) != nil {
		t.Fatal("fixture frame")
	}
	f.Sequence = 2
	changed, _ := json.Marshal(f)
	changed = append(changed, 10)
	if err = os.WriteFile(filepath.Join(config.Dir, "journal.wal"), changed, 0600); err != nil {
		t.Fatal(err)
	}
	bad, _, err := openJournal(config.Dir, r.target)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.close()
	if !bad.poisoned {
		t.Fatal("changed journal authenticated")
	}
}

func TestCompletedServiceSupportsNewReviewedTransaction(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	config.PollInterval = 10 * time.Second
	s := openTest(t, config, r)
	first, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(context.Background(), first.TransactionID, first.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	known := routeros.KnownState{Target: r.target, ActiveGeneration: request.Plan.Generation, Generations: []routeros.KnownGeneration{{Target: r.target, Desired: request.Plan.Desired, PolicyChecksum: request.Plan.PolicyChecksum}}}
	r.mu.Lock()
	baseline, err := routeros.BuildSnapshot(copyInventory(r.inv), known, routeros.CredentialEvidence{}, r.evidence())
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64520, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	next, err := routeros.BuildPlan(r.target, router, nil, baseline, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Stage(context.Background(), Request{Plan: next, KnownState: known, Timeout: 2 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.TransactionID == second.TransactionID || second.PlanChecksum == first.PlanChecksum || second.Phase != AwaitingConfirmation {
		t.Fatal("new transaction did not have independent identity")
	}
	// A prior transaction's confirm intent must not confirm the second one.
	r.mu.Lock()
	r.noUndo = true
	r.owner.Active = false
	r.owner.ThisSession = false
	r.owner.Alive = false
	r.mu.Unlock()
	if st, err := s.Recover(context.Background()); err == nil || st.Phase == Confirmed {
		t.Fatal("old confirm intent leaked into another transaction")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmCount != 1 {
		t.Fatal("second transaction was remotely confirmed")
	}
}

func TestExternalTextYesNoDoesNotCollapseInStateDigest(t *testing.T) {
	r, _ := fixtureRuntime(t)
	r.mu.Lock()
	r.inv.Tables = append(r.inv.Tables, api.Record{".id": "*F", "name": "yes", "fib": "false", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": "no"})
	r.mu.Unlock()
	one, err := r.Observe(context.Background(), ReadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := projectObservation([]byte("fixed-fixture-key"), r.target, one)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.inv.Tables[0]["name"] = "true"
	r.inv.Tables[0]["comment"] = "false"
	r.mu.Unlock()
	two, err := r.Observe(context.Background(), ReadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := projectObservation([]byte("fixed-fixture-key"), r.target, two)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExternalDigest == b.ExternalDigest {
		t.Fatal("text attributes were normalized as booleans")
	}
}

func TestCredentialDeltaAndUnexpectedSideEffectsFailClosed(t *testing.T) {
	key := []byte("fixture-only-key")
	old := "old-secret"
	next := "next-secret"
	obj := objectState{Resource: api.BGPConnection, ID: "*A", Attributes: api.Record{"name": "managed", "disabled": "true"}, CredentialKnown: true, CredentialDigest: credentialMAC(key, "*A", []byte(old))}
	before := deviceState{Objects: map[string]objectState{"bgp-connection/*A": obj}, ExternalDigest: "external"}
	m := api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: "*A", Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: next, Sensitive: true}}}
	spec := specFor(key, m)
	after := cloneState(before)
	obj.CredentialDigest = credentialMAC(key, "*A", []byte(next))
	after.Objects["bgp-connection/*A"] = obj
	delta, err := verifyMutation(key, "managed", before, after, spec, "")
	if err != nil || delta.Object == nil {
		t.Fatal(err)
	}
	obj.CredentialKnown = false
	obj.CredentialDigest = ""
	after.Objects["bgp-connection/*A"] = obj
	if _, err = verifyMutation(key, "managed", before, after, spec, ""); err == nil {
		t.Fatal("unknown key accepted as new key")
	}
	after = cloneState(before)
	after.ExternalDigest = "changed"
	if _, err = verifyMutation(key, "managed", before, after, spec, ""); err == nil {
		t.Fatal("unrelated side effect accepted")
	}
}
