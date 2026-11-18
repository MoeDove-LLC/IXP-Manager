package routeros

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

type nativeOwnedObject struct {
	resource api.Resource
	id       string
}

// This fixed disposable-lab selector never opens an API mutation after taking
// Safe Mode. All tested mutations use the one owned SSH stream; API is readback.
func TestNativeCHRSSHOwnedMutations(t *testing.T) {
	cfg := nativeCHRRequireLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	key := nativeCHRFile(t, "/scratch/ssh_identity", 32<<10, true)
	clear(key)
	hosts := nativeCHRFile(t, "/scratch/known_hosts", 32<<10, false)
	clear(hosts)
	ca := nativeCHRFile(t, cfg.CAFile, 64<<10, false)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("owned_ca_valid=false")
	}
	clear(ca)
	password := nativeCHRFile(t, cfg.PasswordFile, 1024, true)
	defer clear(password)
	client, err := api.Dial(ctx, api.Config{Address: cfg.APIAddress, ServerName: cfg.ServerName, Username: "admin", Password: password, RootCAs: pool, DialTimeout: 5 * time.Second, CommandTimeout: 5 * time.Second})
	clear(password)
	if err != nil {
		t.Fatalf("owned_api_open=false api_code=%s", nativeCHRAPICode(err))
	}
	defer client.Close()
	resources, err := client.Read(ctx, api.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatal("owned_exact_version=false")
	}
	licenses, err := client.Read(ctx, api.SystemLicense)
	if err != nil || len(licenses) != 1 || licenses[0]["system-id"] == "" {
		t.Fatal("owned_stable_identity=false")
	}
	expectedIdentity := sha256.Sum256([]byte(licenses[0]["system-id"]))
	var entropy [6]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		t.Fatal("owned_nonce_available=false")
	}
	name := "md_owned_" + hex.EncodeToString(entropy[:])
	created := []nativeOwnedObject{}
	add := func(resource api.Resource, attrs []api.Attribute) string {
		t.Helper()
		result, e := client.Apply(ctx, api.Mutation{Resource: resource, Action: api.Add, Attributes: attrs})
		if e != nil || !result.Acknowledged || !strings.HasPrefix(result.ID, "*") {
			t.Fatalf("owned_fixture_add=false api_code=%s", nativeCHRAPICode(e))
		}
		created = append(created, nativeOwnedObject{resource, result.ID})
		return result.ID
	}
	// These fixtures are inert before ownership: non-FIB table, disabled
	// instance/connection, connect/listen=no and unreferenced filter.
	mode, err := client.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatal("owned_fixture_idle=false")
	}
	_ = add(api.RoutingTable, []api.Attribute{{Name: "name", Value: name}})
	instance := add(api.BGPInstance, []api.Attribute{{Name: "name", Value: name}, {Name: "as", Value: "64500"}, {Name: "router-id", Value: "192.0.2.1"}, {Name: "routing-table", Value: name}, {Name: "disabled", Value: "yes"}})
	rule := add(api.FilterRule, []api.Attribute{{Name: "chain", Value: name + "_filter"}, {Name: "comment", Value: name}, {Name: "rule", Value: "reject;"}})
	connection := add(api.BGPConnection, []api.Attribute{
		{Name: "name", Value: name + "_peer"}, {Name: "instance", Value: name}, {Name: "as", Value: "64500"}, {Name: "routing-table", Value: name}, {Name: "templates", Value: ""},
		{Name: "disabled", Value: "yes"}, {Name: "connect", Value: "no"}, {Name: "listen", Value: "no"}, {Name: "afi", Value: "ip"},
		{Name: "local.address", Value: "198.51.100.1"}, {Name: "local.role", Value: "ebgp-rs"}, {Name: "remote.address", Value: "198.51.100.2"}, {Name: "remote.as", Value: "64501"},
		{Name: "nexthop-choice", Value: "propagate"}, {Name: "input.filter", Value: name + "_filter"}, {Name: "input.limit-process-routes-ipv4", Value: "10"},
		{Name: "output.filter-chain", Value: name + "_filter"}, {Name: "output.default-originate", Value: "never"}, {Name: "output.default-prepend", Value: "0"},
	})
	if result, e := client.Apply(ctx, api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: connection, Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: "", Sensitive: true}}}); e != nil || !result.Acknowledged {
		t.Fatal("owned_fixture_empty_key=false")
	}
	limits := terminal.DefaultLimits()
	limits.Lifetime = 180 * time.Second
	limits.OperationTimeout = 15 * time.Second
	term, err := terminal.DialSSH(ctx, terminal.SSHOptions{Host: "127.0.0.1", Port: 12222, User: "admin", Identity: "/scratch/ssh_identity", KnownHosts: "/scratch/known_hosts"}, limits)
	if err != nil {
		t.Fatalf("owned_ssh_open=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	defer term.Close()
	if _, err = term.Synchronize(ctx); err != nil {
		t.Fatal("owned_ssh_sync=false")
	}
	identifier, err := term.Query(ctx, terminal.LicenseSystemID)
	if err != nil || identifier.IdentifierFingerprint != hex.EncodeToString(expectedIdentity[:]) {
		t.Fatal("owned_same_device=false")
	}
	if _, err = term.EnterSafeMode(ctx); err != nil {
		t.Fatalf("owned_take=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	execute := func(label string, m api.Mutation) {
		t.Helper()
		receipt, e := term.ExecuteOwned(ctx, m)
		if e != nil || !receipt.Completed || receipt.Evidence.Owner != terminal.OwnerThisSession || receipt.Evidence.NativeValidated || receipt.Evidence.RollbackVerified {
			t.Fatalf("owned_case=%s completed=false terminal_code=%s", label, nativeCHRTerminalCode(e))
		}
		t.Logf("owned_case=%s completed=true owner_current=true api_mutation_used=false", label)
	}
	property := func(resource api.Resource, id, field, want string) {
		t.Helper()
		value, e := client.ReadProperty(ctx, resource, id, field)
		if e != nil || !value.Known || value.Value != want {
			t.Fatalf("owned_readback_field=%s exact=false api_code=%s", field, nativeCHRAPICode(e))
		}
	}
	literal := "if (dst == 203.0.113.0/24 && bgp-as-path 64501$) { accept; }"
	execute("filter-dollar", api.Mutation{Resource: api.FilterRule, Action: api.Set, ID: rule, Attributes: []api.Attribute{{Name: "rule", Value: literal}}})
	property(api.FilterRule, rule, "rule", literal)
	execute("instance-set", api.Mutation{Resource: api.BGPInstance, Action: api.Set, ID: instance, Attributes: []api.Attribute{{Name: "as", Value: "64510"}, {Name: "router-id", Value: "192.0.2.2"}}})
	property(api.BGPInstance, instance, "as", "64510")
	execute("connection-set", api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: connection, Attributes: []api.Attribute{{Name: "as", Value: "64510"}, {Name: "input.limit-process-routes-ipv4", Value: "20"}, {Name: "output.default-prepend", Value: "0"}}})
	property(api.BGPConnection, connection, "as", "64510")
	property(api.BGPConnection, connection, "input.limit-process-routes-ipv4", "20")
	secret := "OwnedFixture_MD5_123"
	execute("authentication-set", api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: connection, Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: secret, Sensitive: true}}})
	credential, e := client.ReadCredential(ctx, connection)
	if e != nil || !credential.Matches([]byte(secret)) {
		t.Fatal("owned_secret_readback=false")
	}
	execute("authentication-empty", api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: connection, Attributes: []api.Attribute{{Name: "tcp-md5-key", Value: "", Sensitive: true}}})
	credential, e = client.ReadCredential(ctx, connection)
	if e != nil || !credential.Matches(nil) {
		t.Fatal("owned_empty_readback=false")
	}
	execute("limit-unset", api.Mutation{Resource: api.BGPConnection, Action: api.Unset, ID: connection, Property: "input.limit-process-routes-ipv4"})
	limit, e := client.ReadProperty(ctx, api.BGPConnection, connection, "input.limit-process-routes-ipv4")
	if e != nil || limit.Known && limit.Value != "" {
		t.Fatal("owned_unset_readback=false")
	}
	execute("connection-enable", api.Mutation{Resource: api.BGPConnection, Action: api.Enable, ID: connection})
	property(api.BGPConnection, connection, "disabled", "false")
	execute("connection-disable", api.Mutation{Resource: api.BGPConnection, Action: api.Disable, ID: connection})
	property(api.BGPConnection, connection, "disabled", "true")
	result, err := term.CancelSafeMode(ctx)
	if err != nil || !result.Requested || !result.SessionEnded || result.RollbackVerified {
		t.Fatalf("owned_cancel_requested=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	until := time.Now().Add(15 * time.Second)
	restored := false
	for time.Now().Before(until) && ctx.Err() == nil {
		as, aerr := client.ReadProperty(ctx, api.BGPInstance, instance, "as")
		body, berr := client.ReadProperty(ctx, api.FilterRule, rule, "rule")
		prefixes, perr := client.ReadProperty(ctx, api.BGPConnection, connection, "input.limit-process-routes-ipv4")
		auth, aute := client.ReadCredential(ctx, connection)
		safe, se := client.ReadSafeMode(ctx)
		if aerr == nil && berr == nil && perr == nil && aute == nil && se == nil && as.Known && as.Value == "64500" && body.Known && body.Value == "reject;" && prefixes.Known && prefixes.Value == "10" && auth.Matches(nil) && !safe.Enabled && !safe.Current {
			restored = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !restored {
		t.Fatal("owned_checkpoint_restored=false")
	}
	t.Log("owned_checkpoint_restored=true api_activation_mutations=0 manual_cleanup_before_proof=false")
	for i := len(created) - 1; i >= 0; i-- {
		result, e := client.Apply(ctx, api.Mutation{Resource: created[i].resource, Action: api.Remove, ID: created[i].id})
		if e != nil || !result.Acknowledged {
			t.Fatal("owned_cleanup_after_proof=false")
		}
	}
}
