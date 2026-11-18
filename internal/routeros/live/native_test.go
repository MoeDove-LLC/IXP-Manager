package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

func nativeLiveConfig(t *testing.T) Config {
	t.Helper()
	if os.Getenv("MOEDOVE_CHR_NATIVE_TEST") != "1" {
		t.Skip("native_chr_fixture_enabled=false")
	}
	status, e := os.ReadFile("/proc/self/status")
	if e != nil || len(status) > 64<<10 {
		t.Fatal("fixture_caps_readable=false")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[0] == "CapEff:" || f[0] == "CapBnd:") {
			v, e := strconv.ParseUint(f[1], 16, 64)
			if e != nil || v != 0 || seen[f[0]] {
				t.Fatal("fixture_capabilities_zero=false")
			}
			seen[f[0]] = true
		}
	}
	if !seen["CapEff:"] || !seen["CapBnd:"] {
		t.Fatal("fixture_capabilities_zero=false")
	}
	interfaces, e := net.Interfaces()
	if e != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" || interfaces[0].Flags&net.FlagLoopback == 0 || interfaces[0].Flags&net.FlagUp == 0 {
		t.Fatal("fixture_loopback_only=false")
	}
	raw, e := readFixedFile("/scratch/native-context.json", 8192, false)
	if e != nil {
		t.Fatal("fixture_context_readable=false")
	}
	var fixture struct {
		Version       string `json:"version"`
		ImageSHA256   string `json:"imageSha256"`
		APIAddress    string `json:"apiAddress"`
		ServerName    string `json:"serverName"`
		CAFile        string `json:"caFile"`
		PasswordFile  string `json:"passwordFile"`
		ConsoleSocket string `json:"consoleSocket"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&fixture) != nil || dec.Decode(new(any)) != io.EOF {
		t.Fatal("fixture_context_valid=false")
	}
	if fixture.Version != "7.24.6" || fixture.ImageSHA256 != "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878" || fixture.APIAddress != "127.0.0.1:18729" || fixture.ServerName != "chr-fixture.invalid" || fixture.CAFile != "/scratch/api.crt" || fixture.PasswordFile != "/scratch/api-password" || fixture.ConsoleSocket != "/scratch/console.sock" {
		t.Fatal("fixture_fixed_target_valid=false")
	}
	ca, e := readFixedFile(fixture.CAFile, 64<<10, false)
	if e != nil {
		t.Fatal("fixture_ca_readable=false")
	}
	block, _ := pem.Decode(ca)
	if block == nil {
		t.Fatal("fixture_ca_pem=false")
	}
	certificate, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		t.Fatal("fixture_ca_certificate=false")
	}
	spki := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	hosts, e := readFixedFile("/scratch/known_hosts", 32<<10, false)
	if e != nil {
		t.Fatal("fixture_host_pin_readable=false")
	}
	fields := strings.Fields(string(hosts))
	if len(fields) != 3 || fields[0] != "[127.0.0.1]:12222" {
		t.Fatal("fixture_host_pin_exact=false")
	}
	wire, e := base64.StdEncoding.DecodeString(fields[2])
	if e != nil {
		t.Fatal("fixture_host_key_valid=false")
	}
	hostPin := sha256.Sum256(wire)
	password, e := readFixedFile(fixture.PasswordFile, 1024, true)
	if e != nil {
		t.Fatal("fixture_password_readable=false")
	}
	defer clear(password)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, e := api.Dial(ctx, api.Config{Address: fixture.APIAddress, ServerName: fixture.ServerName, Username: "admin", Password: password, RootCAs: roots, SPKISHA256: hex.EncodeToString(spki[:])})
	if e != nil {
		t.Fatal("fixture_api_verified=false")
	}
	defer client.Close()
	licenses, e := client.Read(ctx, api.SystemLicense)
	if e != nil || len(licenses) != 1 || !validDeviceIdentifier(routeros.LicenseSystemID, licenses[0]["system-id"]) {
		t.Fatal("fixture_stable_identity=false")
	}
	return Config{SchemaVersion: 1, RouterID: "native-live-probe", Version: "7.24.6", DeviceID: licenses[0]["system-id"], IdentitySource: routeros.LicenseSystemID, API: APIConfig{Address: fixture.APIAddress, ServerName: fixture.ServerName, Username: "admin", PasswordFile: fixture.PasswordFile, CAFile: fixture.CAFile, SPKISHA256: hex.EncodeToString(spki[:])}, SSH: SSHConfig{Host: "127.0.0.1", Port: 12222, Username: "admin", IdentityFile: "/scratch/ssh_identity", KnownHostsFile: "/scratch/known_hosts", HostKeySHA256: hex.EncodeToString(hostPin[:])}, ObservationTimeoutSeconds: 120, OperationTimeoutSeconds: 10, SessionLifetimeSeconds: 300, MaxObjects: 2048, MaxGetterCalls: 8192}
}
func TestNativeCHRLiveObserve(t *testing.T) {
	cfg := nativeLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	runtime, e := Open(ctx, cfg)
	if e != nil {
		t.Fatalf("live_open=false code=%s", liveCode(e))
	}
	defer runtime.Close()
	target := runtime.Binding().Target
	known := routeros.KnownState{Target: target}
	observed, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil {
		t.Fatalf("live_observation=false code=%s", liveCode(e))
	}
	if observed.Snapshot == nil || !observed.Snapshot.NamespaceEmpty || !observed.CrossChannelIdentityVerified || observed.Resolver == nil || !observed.Owner.Known || observed.Owner.Active || !observed.Owner.HistoryKnown {
		t.Fatal("live_complete_empty_namespace_and_owner=false")
	}
	t.Log("live_complete_empty_namespace=true cross_channel_identity=true pending_count_known=true native_activation_permitted=false")
	if runtime.Binding().PrepareVerified || runtime.Binding().ActivationVerified {
		t.Fatal("unverified_native_gate_closed=false")
	}
	if e = runtime.CloseOwner(); e != nil {
		t.Fatal("live_close_owner=false")
	}
	if _, e = runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true}); e != nil {
		t.Fatalf("live_readback_after_owner_close=false code=%s", liveCode(e))
	}
	t.Log("live_readback_after_owner_close=true")
}

// This isolated probe bypasses no production capability gate. It directly
// submits only canonical Prepare-phase commands to the fixed disposable CHR,
// never takes Safe Mode, enables a peer, or asserts transaction/undo evidence.
func TestNativeCHRLiveGeneratedPrepare(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-live-prepare"
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	runtime, e := Open(ctx, cfg)
	if e != nil {
		t.Fatalf("live_prepare_open=false code=%s", liveCode(e))
	}
	defer runtime.Close()
	target := runtime.Binding().Target
	known := routeros.KnownState{Target: target}
	baseline, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || baseline.Snapshot == nil || !baseline.Snapshot.NamespaceEmpty {
		t.Fatalf("live_prepare_initial_namespace_empty=false code=%s", liveCode(e))
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "probe4", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}, {ID: "probe6", ASN: 64502, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:1::/48"}}}
	plan, e := routeros.BuildPlan(target, router, peers, *baseline.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if e != nil {
		t.Fatal("live_prepare_plan_valid=false")
	}
	bridge, e := routeros.NewAPIBridge(plan, nil)
	if e != nil {
		t.Fatal("live_prepare_bridge_valid=false")
	}
	type created struct {
		resource api.Resource
		id       string
	}
	objects := []created{}
	defer func() {
		for i := len(objects) - 1; i >= 0; i-- {
			cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := runtime.api.Apply(cleanup, api.Mutation{Resource: objects[i].resource, Action: api.Remove, ID: objects[i].id})
			c()
			if err != nil {
				t.Error("live_prepare_cleanup=false")
			}
		}
	}()
	rules := 0
	connections := 0
	for operationIndex, op := range bridge.Operations() {
		if op.Phase != routeros.Prepare {
			continue
		}
		raw, evidence, _, _, err := runtime.readState(ctx)
		if err != nil {
			t.Fatalf("live_prepare_readback_before=false code=%s", liveCode(err))
		}
		resolver, err := routeros.NewAPIResolver(raw, target, evidence)
		if err != nil {
			t.Fatal("live_prepare_reference=false")
		}
		mutation, err := bridge.Mutation(op.ID, resolver)
		if err != nil || mutation.Phase != routeros.Prepare || mutation.Request.Action == api.Enable || mutation.Request.Action == api.Remove {
			t.Fatal("live_prepare_inert_command=false")
		}
		result, err := runtime.api.Apply(ctx, mutation.Request)
		if err != nil || !result.Acknowledged {
			t.Fatalf("live_prepare_native_accept=false resource=%s action=%s operation_index=%d", mutation.Request.Resource, mutation.Request.Action, operationIndex)
		}
		if mutation.Request.Action == api.Add {
			if result.ID == "" {
				t.Fatal("live_prepare_native_id=false")
			}
			objects = append(objects, created{mutation.Request.Resource, result.ID})
		}
		if mutation.Request.Resource == api.BGPConnection && mutation.Request.Action == api.Add {
			nativeLiveConnectionBooleans(t, ctx, runtime, result.ID, mutation.Request.Attributes)
			nativeLiveConnectionASEvidence(t, ctx, runtime, result.ID)
		}
		_, _, inventory, _, err := runtime.readState(ctx)
		if err != nil {
			t.Fatalf("live_prepare_readback_after=false code=%s", liveCode(err))
		}
		id := mutation.Request.ID
		if mutation.Request.Action == api.Add {
			id = result.ID
		}
		var actual api.Record
		for _, m := range menus(inventory) {
			if m.resource == mutation.Request.Resource {
				for _, row := range m.rows {
					if strings.EqualFold(row[".id"], id) {
						actual = row
					}
				}
			}
		}
		if actual == nil {
			t.Fatal("live_prepare_exact_id_readback=false")
		}
		for _, attribute := range mutation.Request.Attributes {
			if attribute.Name == "tcp-md5-key" {
				credential, err := runtime.api.ReadCredential(ctx, id)
				if err != nil || !credential.Matches([]byte(attribute.Value)) {
					t.Fatal("live_prepare_credential_readback=false")
				}
				continue
			}
			want := attribute.Value
			if want == "yes" {
				want = "true"
			}
			if want == "no" {
				want = "false"
			}
			if actual[attribute.Name] != want {
				t.Fatalf("live_prepare_exact_attribute=false resource=%s field=%s", mutation.Request.Resource, attribute.Name)
			}
		}
		if mutation.Request.Action == api.Unset && actual[mutation.Request.Property] != "" {
			t.Fatal("live_prepare_unset_readback=false")
		}
		switch mutation.Request.Resource {
		case api.FilterRule:
			if actual["inactive"] != "false" || actual["disabled"] != "false" {
				t.Fatal("live_generated_rule_usable=false")
			}
			rules++
		case api.BGPConnection:
			if actual["disabled"] != "true" || actual["routing-table"] != plan.Owner {
				t.Fatal("live_generated_connection_inert=false")
			}
			connections++
		case api.RoutingTable:
			if actual["fib"] != "false" {
				t.Fatal("live_generated_table_nonfib=false")
			}
		}
	}
	if rules == 0 || connections == 0 {
		t.Fatal("live_generated_coverage=false")
	}
	t.Log("live_generated_prepare_native_readback=true activation_exercised=false undo_budget_verified=false")
}

// Only fixed-field boolean predicates are printed. Missing explicit getter
// values cannot be promoted from the documented effective defaults.
func nativeLiveConnectionBooleans(t *testing.T, ctx context.Context, runtime *Runtime, id string, attributes []api.Attribute) {
	t.Helper()
	inventory, err := runtime.api.Inventory(ctx)
	if err != nil {
		t.Fatal("live_connection_boolean_inventory=false")
	}
	var row api.Record
	for _, candidate := range inventory.Connections {
		if strings.EqualFold(candidate[".id"], id) {
			row = candidate
		}
	}
	if row == nil {
		t.Fatal("live_connection_boolean_exact_id=false")
	}
	valid := true
	for _, field := range []string{"connect", "listen"} {
		explicitYes := false
		for _, attribute := range attributes {
			if attribute.Name == field && (attribute.Value == "yes" || attribute.Value == "true") {
				explicitYes = true
			}
		}
		printed, present := row[field]
		property, getterErr := runtime.api.ReadProperty(ctx, api.BGPConnection, id, field)
		code := "none"
		if getterErr != nil {
			code = "unclassified"
			if failure, ok := getterErr.(*api.Error); ok {
				code = string(failure.Code)
			}
		}
		getterTrue := property.Known && (property.Value == "true" || property.Value == "yes")
		t.Logf("live_connection_switch_field=%s explicit_yes=%t print_present=%t print_empty=%t print_true=%t print_false=%t getter_known=%t getter_empty=%t getter_true=%t getter_false=%t api_code=%s", field, explicitYes, present, present && printed == "", present && (printed == "true" || printed == "yes"), present && (printed == "false" || printed == "no"), property.Known, property.Known && property.Value == "", getterTrue, property.Known && (property.Value == "false" || property.Value == "no"), code)
		if !explicitYes || getterErr != nil || !getterTrue {
			valid = false
		}
	}
	if !valid {
		t.Fatal("live_connection_explicit_connect_listen_verified=false")
	}
}
func liveCode(err error) string {
	if err == nil {
		return "none"
	}
	if e, ok := err.(*Failure); ok {
		return e.Code
	}
	return "unclassified"
}

func TestNativeCHRLiveResetIdle(t *testing.T) {
	cfg := nativeLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	runtime, e := Open(ctx, cfg)
	if e != nil {
		t.Fatalf("idle_reset_open=false code=%s", liveCode(e))
	}
	defer runtime.Close()
	request := control.ReadRequest{KnownState: routeros.KnownState{Target: runtime.Binding().Target}, Strict: true}
	before, e := runtime.Observe(ctx, request)
	if e != nil || before.Owner.Active || !before.Owner.Known {
		t.Fatalf("idle_reset_baseline=false code=%s", liveCode(e))
	}
	if e = runtime.CloseOwner(); e != nil {
		t.Fatal("idle_reset_close_old=false")
	}
	if e = runtime.ResetIdle(ctx); e != nil {
		t.Fatalf("idle_reset_new_channel=false code=%s", liveCode(e))
	}
	after, e := runtime.Observe(ctx, request)
	if e != nil || after.Owner.Active || !after.Owner.Known || !after.Owner.Alive || before.Owner.SessionID == after.Owner.SessionID || !after.CrossChannelIdentityVerified {
		t.Fatalf("idle_reset_new_authority=false code=%s", liveCode(e))
	}
	t.Log("idle_reset_new_unowned_session=true cross_channel_identity=true old_owner_resumed=false")
}

func nativeLiveConnectionASEvidence(t *testing.T, ctx context.Context, runtime *Runtime, id string) {
	t.Helper()
	inventory, err := runtime.api.Inventory(ctx)
	if err != nil {
		t.Fatal("live_connection_as_inventory=false")
	}
	var connection api.Record
	matches := 0
	for _, row := range inventory.Connections {
		if strings.EqualFold(row[".id"], id) {
			connection = row
			matches++
		}
	}
	if matches != 1 {
		t.Fatal("live_connection_as_exact_id=false")
	}
	var instance api.Record
	bindings := 0
	for _, row := range inventory.Instances {
		if connection["instance"] != "" && (connection["instance"] == row["name"] || strings.EqualFold(connection["instance"], row[".id"])) {
			instance = row
			bindings++
		}
	}
	unique := bindings == 1 && instance["name"] == routeros.OwnerName(runtime.target.RouterID) && instance["as"] != "" && connection["routing-table"] == instance["routing-table"] && connection["routing-table"] == routeros.OwnerName(runtime.target.RouterID)
	printed, present := connection["as"]
	property, getterErr := runtime.api.ReadProperty(ctx, api.BGPConnection, id, "as")
	code := "none"
	if getterErr != nil {
		code = "unclassified"
		if failure, ok := getterErr.(*api.Error); ok {
			code = string(failure.Code)
		}
	}
	t.Logf("live_connection_as=true instance_bound_unique=%t print_present=%t print_empty=%t print_zero=%t print_zero_pair=%t print_equals_instance=%t print_equals_instance_pair=%t getter_known=%t getter_empty=%t getter_no_ret_empty=%t getter_equals_instance=%t api_code=%s", unique, present, present && printed == "", present && printed == "0", present && printed == "0/0", unique && present && printed == instance["as"], unique && present && printed == instance["as"]+"/0", property.Known, property.Known && property.Value == "", !property.Known && property.Value == "", unique && property.Known && property.Value == instance["as"], code)
	if !unique || getterErr != nil {
		t.Fatal("live_connection_as_evidence_available=false")
	}
}
