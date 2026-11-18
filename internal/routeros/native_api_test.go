package routeros

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	rosapi "github.com/moedove/ixp-manager/internal/routeros/api"
)

// This experiment deliberately performs explicit cleanup. It is independent of
// TestNativeCHRSafeModeRollback and cannot supply evidence that Cancel undid a
// write. It runs only in that test's fixed, disposable, loopback-only harness.
func nativeCHRAPICode(err error) string {
	if err == nil {
		return "none"
	}
	var failure *rosapi.Error
	if !errors.As(err, &failure) {
		return "unclassified"
	}
	switch failure.Code {
	case rosapi.CodeConfig, rosapi.CodeAuthentication, rosapi.CodeTLS, rosapi.CodeTransport, rosapi.CodeProtocol, rosapi.CodeLimit, rosapi.CodeTrap, rosapi.CodeUnsupported, rosapi.CodeFatal, rosapi.CodeClosed, rosapi.CodeCancelled:
		return string(failure.Code)
	}
	return "unclassified"
}
func nativeCHRProbeValue(t *testing.T, kind, field, source string, present bool, value string) {
	t.Helper()
	t.Logf("probe_kind=%s probe_field=%s probe_source=%s present=%t empty=%t true=%t false=%t other=%t", kind, field, source, present, present && value == "", present && (value == "true" || value == "yes"), present && (value == "false" || value == "no"), present && value != "" && value != "true" && value != "yes" && value != "false" && value != "no")
}
func nativeCHRProbeRow(t *testing.T, ctx context.Context, client *rosapi.Client, resource rosapi.Resource, id, identityKey, identity string) (rosapi.Record, bool) {
	t.Helper()
	rows, err := client.Read(ctx, resource)
	if err != nil {
		t.Errorf("probe_readback_available=false api_code=%s", nativeCHRAPICode(err))
		return nil, false
	}
	matches := []rosapi.Record{}
	for _, row := range rows {
		if row[".id"] == id && row[identityKey] == identity {
			matches = append(matches, row)
		}
	}
	if len(matches) != 1 {
		t.Error("probe_exact_native_identity_unique=false")
		return nil, false
	}
	return matches[0], true
}
func nativeCHRProbeProperties(t *testing.T, ctx context.Context, client *rosapi.Client, resource rosapi.Resource, id string, row rosapi.Record, fields []string) {
	t.Helper()
	for _, field := range fields {
		value, present := row[field]
		nativeCHRProbeValue(t, string(resource), field, "print", present, value)
		if field == ".id" {
			continue
		}
		if resource == rosapi.FilterRule && field == "invalid" {
			_, probeErr := client.ReadProperty(ctx, resource, id, field)
			t.Logf("probe_filter_invalid_getter_unsupported=%t api_code=%s", probeErr != nil, nativeCHRAPICode(probeErr))
			continue
		}
		property, err := client.ReadProperty(ctx, resource, id, field)
		t.Logf("probe_kind=%s probe_field=%s getter_accepted=%t api_code=%s", resource, field, err == nil, nativeCHRAPICode(err))
		if err != nil {
			if field != "used" && (field != "inactive" || resource == rosapi.FilterRule) {
				t.Errorf("compiler_required_getter_accepted=false probe_kind=%s probe_field=%s api_code=%s", resource, field, nativeCHRAPICode(err))
			}
			continue
		}
		nativeCHRProbeValue(t, string(resource), field, "get", property.Known, property.Value)
	}
}

type nativeCHRProbeObject struct {
	resource                  rosapi.Resource
	id, identityKey, identity string
}

func TestNativeCHRAPIProperties(t *testing.T) {
	configuration := nativeCHRRequireLab(t)
	// Read all fixed credentials before any mutation. These files belong only to
	// the disposable harness; no environment hostname or credential is accepted.
	password := nativeCHRFile(t, configuration.PasswordFile, 1024, true)
	defer clear(password)
	limitedPassword := nativeCHRFile(t, "/scratch/limited-password", 1024, true)
	defer clear(limitedPassword)
	caData := nativeCHRFile(t, configuration.CAFile, 64<<10, false)
	defer clear(caData)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("probe_ca_parsed=false")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	config := func(username string, credential []byte) rosapi.Config {
		return rosapi.Config{Address: configuration.APIAddress, ServerName: configuration.ServerName, Username: username, Password: credential, RootCAs: pool, DialTimeout: 5 * time.Second, CommandTimeout: 5 * time.Second, Limits: rosapi.Limits{MaxReplyBytes: 1 << 20, MaxRecords: 1024}}
	}
	client, err := rosapi.Dial(ctx, config("admin", password))
	clear(password)
	if err != nil {
		t.Fatalf("probe_tls_authenticated=false api_code=%s", nativeCHRAPICode(err))
	}
	defer client.Close()
	resources, err := client.Read(ctx, rosapi.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatalf("probe_version_matches=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Logf("api_version_has_longterm_suffix=%t api_version_has_stable_suffix=%t", strings.HasSuffix(resources[0]["version"], " (long-term)"), strings.HasSuffix(resources[0]["version"], " (stable)"))
	safe, err := client.ReadSafeMode(ctx)
	if err != nil || safe.Enabled || safe.Current {
		t.Fatalf("probe_initial_safe_mode_idle=false api_code=%s", nativeCHRAPICode(err))
	}
	// A successful ordinary read proves that the limited fixture exists and can
	// use API-SSL; failure to create the account is not a skipped permission test.
	limited, err := rosapi.Dial(ctx, config("mdlab-read", limitedPassword))
	if err != nil {
		t.Fatalf("probe_limited_authenticated=false api_code=%s", nativeCHRAPICode(err))
	}
	limitedResources, err := limited.Read(ctx, rosapi.SystemResource)
	limited.Close()
	if err != nil || len(limitedResources) != 1 || !nativeCHRVersionMatches(limitedResources[0]["version"]) {
		t.Fatalf("probe_limited_basic_read=false api_code=%s", nativeCHRAPICode(err))
	}
	var nonce [6]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal("probe_unique_name_available=false")
	}
	prefix := "md_native_probe_" + hex.EncodeToString(nonce[:])
	objects := []nativeCHRProbeObject{}
	// Cleanup is deliberately separate from rollback proof. IDs are re-read and
	// checked against this probe's exact unique name/comment before removal.
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for i := len(objects) - 1; i >= 0; i-- {
			object := objects[i]
			if _, ok := nativeCHRProbeRow(t, cleanupCtx, client, object.resource, object.id, object.identityKey, object.identity); !ok {
				t.Error("probe_cleanup_identity_verified=false")
				continue
			}
			result, err := client.Apply(cleanupCtx, rosapi.Mutation{Resource: object.resource, Action: rosapi.Remove, ID: object.id})
			if err != nil || !result.Acknowledged {
				t.Errorf("probe_explicit_cleanup=false api_code=%s uncertain=%t", nativeCHRAPICode(err), rosapi.IsUncertain(err))
			}
		}
	}()
	add := func(resource rosapi.Resource, identityKey, identity string, attributes []rosapi.Attribute) (string, bool) {
		t.Helper()
		result, err := client.Apply(ctx, rosapi.Mutation{Resource: resource, Action: rosapi.Add, Attributes: attributes})
		if err != nil || !result.Acknowledged || !strings.HasPrefix(result.ID, "*") {
			t.Errorf("compiler_required_add_accepted=false probe_kind=%s api_code=%s uncertain=%t", resource, nativeCHRAPICode(err), rosapi.IsUncertain(err))
			return "", false
		}
		objects = append(objects, nativeCHRProbeObject{resource, result.ID, identityKey, identity})
		return result.ID, true
	}
	for _, value := range []string{"no", "false", ""} {
		localErr := rosapi.ValidateMutation(rosapi.Mutation{Resource: rosapi.RoutingTable, Action: rosapi.Set, ID: "*1", Attributes: []rosapi.Attribute{{Name: "fib", Value: value}}})
		t.Logf("probe_fib_false_form_rejected_locally=%t", localErr != nil)
		if localErr == nil {
			t.Fatal("unsafe_fib_presence_form_blocked=false")
		}
	}
	var bgpTableName, bgpTableID string
	var omittedGet rosapi.PropertyValue
	var omittedPrint string
	var omittedPresent bool
	for _, variant := range []string{"omit", "on"} {
		name := prefix + "_fib_" + variant
		attributes := []rosapi.Attribute{{Name: "name", Value: name}}
		if variant == "on" {
			attributes = append(attributes, rosapi.Attribute{Name: "fib", Value: "yes"})
		}
		before, readErr := client.Read(ctx, rosapi.RoutingTable)
		if readErr != nil {
			t.Fatalf("probe_tables_available=false api_code=%s", nativeCHRAPICode(readErr))
		}
		if named, _ := nativeCHRTablePresence(before, name, ""); named != 0 {
			t.Fatal("probe_unique_table_initially_absent=false")
		}
		id, accepted := add(rosapi.RoutingTable, "name", name, attributes)
		t.Logf("probe_fib_case=%s add_acknowledged=%t", variant, accepted)
		if !accepted {
			continue
		}
		row, found := nativeCHRProbeRow(t, ctx, client, rosapi.RoutingTable, id, "name", name)
		if !found {
			continue
		}
		nativeCHRProbeProperties(t, ctx, client, rosapi.RoutingTable, id, row, []string{".id", "name", "fib", "disabled", "dynamic", "invalid", "used", "comment"})
		value, getErr := client.ReadProperty(ctx, rosapi.RoutingTable, id, "fib")
		if getErr != nil {
			t.Errorf("probe_fib_getter_accepted=false api_code=%s", nativeCHRAPICode(getErr))
			continue
		}
		if variant == "omit" {
			bgpTableName, bgpTableID = name, id
			omittedGet = value
			omittedPrint, omittedPresent = row["fib"]
			continue
		}
		on := value.Known && (value.Value == "true" || value.Value == "yes")
		t.Logf("probe_fib_switch_on_verified=%t", on)
		if !on {
			t.Error("compiler_fib_switch_on_readback=false")
		}
		result, unsetErr := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.RoutingTable, Action: rosapi.Unset, ID: id, Property: "fib"})
		t.Logf("probe_fib_unset_acknowledged=%t api_code=%s uncertain=%t", unsetErr == nil && result.Acknowledged, nativeCHRAPICode(unsetErr), rosapi.IsUncertain(unsetErr))
		if unsetErr != nil || !result.Acknowledged {
			t.Errorf("compiler_required_fib_unset_accepted=false api_code=%s", nativeCHRAPICode(unsetErr))
			continue
		}
		after, found := nativeCHRProbeRow(t, ctx, client, rosapi.RoutingTable, id, "name", name)
		if !found {
			continue
		}
		afterGet, getErr := client.ReadProperty(ctx, rosapi.RoutingTable, id, "fib")
		if getErr != nil {
			t.Errorf("probe_fib_after_unset_readable=false api_code=%s", nativeCHRAPICode(getErr))
			continue
		}
		afterPrint, present := after["fib"]
		nativeCHRProbeValue(t, "routing-table", "fib", "after-unset-print", present, afterPrint)
		nativeCHRProbeValue(t, "routing-table", "fib", "after-unset-get", afterGet.Known, afterGet.Value)
		equivalent := present == omittedPresent && afterPrint == omittedPrint && afterGet.Known == omittedGet.Known && afterGet.Value == omittedGet.Value
		t.Logf("probe_fib_unset_matches_omitted_representation=%t", equivalent)
		if !equivalent {
			t.Error("compiler_fib_unset_readback_matches_omission=false")
		}
	}
	if bgpTableName == "" {
		t.Fatal("probe_isolated_bgp_table_available=false")
	}
	instanceName := prefix + "_instance"
	instanceID, ok := add(rosapi.BGPInstance, "name", instanceName, []rosapi.Attribute{{Name: "name", Value: instanceName}, {Name: "as", Value: "64512"}, {Name: "router-id", Value: "192.0.2.254"}, {Name: "routing-table", Value: bgpTableName}, {Name: "disabled", Value: "yes"}})
	if !ok {
		return
	}
	instanceRow, ok := nativeCHRProbeRow(t, ctx, client, rosapi.BGPInstance, instanceID, "name", instanceName)
	if !ok {
		return
	}
	nativeCHRProbeProperties(t, ctx, client, rosapi.BGPInstance, instanceID, instanceRow, []string{".id", "name", "as", "router-id", "routing-table", "vrf", "disabled", "inactive", "comment"})
	nativeCHRProbeExpected(t, ctx, client, rosapi.BGPInstance, instanceID, instanceRow, "routing-table", bgpTableName, bgpTableID, true)
	disabled, err := client.ReadProperty(ctx, rosapi.BGPInstance, instanceID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "true" && disabled.Value != "yes" {
		t.Fatalf("probe_instance_verified_disabled=false api_code=%s", nativeCHRAPICode(err))
	}
	inputChain, outputChain := prefix+"_in", prefix+"_out"
	for _, chain := range []string{inputChain, outputChain} {
		ruleID, ok := add(rosapi.FilterRule, "comment", chain, []rosapi.Attribute{{Name: "chain", Value: chain}, {Name: "comment", Value: chain}, {Name: "rule", Value: "reject;"}})
		if !ok {
			return
		}
		row, ok := nativeCHRProbeRow(t, ctx, client, rosapi.FilterRule, ruleID, "comment", chain)
		if !ok {
			return
		}
		nativeCHRProbeProperties(t, ctx, client, rosapi.FilterRule, ruleID, row, []string{".id", "chain", "comment", "rule", "disabled", "inactive", "invalid"})
		t.Logf("probe_reject_rule_exact=%t", row["rule"] == "reject;")
		if row["rule"] != "reject;" {
			t.Error("compiler_reject_rule_exact_roundtrip=false")
		}
		validFlag, flagErr := client.ReadProperty(ctx, rosapi.FilterRule, ruleID, "inactive")
		valid := flagErr == nil && validFlag.Known && (validFlag.Value == "false" || validFlag.Value == "no")
		t.Logf("probe_valid_unreferenced_filter_inactive_false=%t api_code=%s", valid, nativeCHRAPICode(flagErr))
		if !valid {
			t.Error("compiler_valid_filter_inactive_semantics_verified=false")
		}
	}
	connectionName := prefix + "_default_connection"
	attributes := []rosapi.Attribute{{Name: "name", Value: connectionName}, {Name: "instance", Value: instanceName}, {Name: "templates", Value: ""}, {Name: "disabled", Value: "yes"}, {Name: "connect", Value: "no"}, {Name: "listen", Value: "no"}, {Name: "afi", Value: "ip"}, {Name: "local.address", Value: "192.0.2.254"}, {Name: "local.role", Value: "ebgp-rs"}, {Name: "remote.address", Value: "192.0.2.253"}, {Name: "remote.as", Value: "64513"}, {Name: "nexthop-choice", Value: "propagate"}, {Name: "input.filter", Value: inputChain}, {Name: "output.filter-chain", Value: outputChain}, {Name: "output.default-originate", Value: "never"}}
	connectionID, ok := add(rosapi.BGPConnection, "name", connectionName, attributes)
	if !ok {
		return
	}
	connectionRow, ok := nativeCHRProbeRow(t, ctx, client, rosapi.BGPConnection, connectionID, "name", connectionName)
	if !ok {
		return
	}
	disabled, err = client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "true" && disabled.Value != "yes" {
		t.Fatalf("probe_connection_verified_disabled=false api_code=%s", nativeCHRAPICode(err))
	}
	// The default-table case is an independent observation, never evidence
	// that the connection inherited its instance's isolated routing table.
	defaultTable, defaultErr := client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, "routing-table")
	defaultPrint, defaultPresent := connectionRow["routing-table"]
	t.Logf("probe_default_connection_table_print_present=%t print_is_main=%t getter_known=%t getter_is_main=%t api_code=%s", defaultPresent, defaultPresent && defaultPrint == "main", defaultTable.Known, defaultTable.Known && defaultTable.Value == "main", nativeCHRAPICode(defaultErr))
	if defaultErr != nil {
		t.Errorf("probe_default_connection_table_readable=false api_code=%s", nativeCHRAPICode(defaultErr))
		return
	}
	changed, setTableErr := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.BGPConnection, Action: rosapi.Set, ID: connectionID, Attributes: []rosapi.Attribute{{Name: "routing-table", Value: bgpTableName}}})
	t.Logf("probe_connection_table_setter_after_acknowledged=%t api_code=%s uncertain=%t", setTableErr == nil && changed.Acknowledged, nativeCHRAPICode(setTableErr), rosapi.IsUncertain(setTableErr))
	if setTableErr != nil || !changed.Acknowledged {
		t.Errorf("compiler_required_connection_table_setter_accepted=false api_code=%s", nativeCHRAPICode(setTableErr))
		return
	}
	connectionRow, ok = nativeCHRProbeRow(t, ctx, client, rosapi.BGPConnection, connectionID, "name", connectionName)
	if !ok || !nativeCHRProbeRoutingTableExact(t, ctx, client, connectionID, connectionRow, bgpTableName, "after-set") {
		return
	}
	disabled, err = client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "true" && disabled.Value != "yes" {
		t.Fatalf("probe_default_connection_still_disabled_after_set=false api_code=%s", nativeCHRAPICode(err))
	}
	// A second disabled object verifies the production add shape with an
	// explicit same-table binding, without treating the default case as valid.
	connectionName = prefix + "_connection"
	attributes = append([]rosapi.Attribute(nil), attributes...)
	for i := range attributes {
		if attributes[i].Name == "name" {
			attributes[i].Value = connectionName
		}
	}
	attributes = append(attributes, rosapi.Attribute{Name: "routing-table", Value: bgpTableName})
	connectionID, ok = add(rosapi.BGPConnection, "name", connectionName, attributes)
	if !ok {
		return
	}
	connectionRow, ok = nativeCHRProbeRow(t, ctx, client, rosapi.BGPConnection, connectionID, "name", connectionName)
	if !ok || !nativeCHRProbeRoutingTableExact(t, ctx, client, connectionID, connectionRow, bgpTableName, "add-explicit") {
		return
	}
	disabled, err = client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "true" && disabled.Value != "yes" {
		t.Fatalf("probe_explicit_connection_verified_disabled=false api_code=%s", nativeCHRAPICode(err))
	}
	nativeCHRProbeProperties(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, []string{".id", "name", "instance", "templates", "disabled", "dynamic", "inactive", "afi", "local.address", "local.role", "remote.address", "remote.as", "as", "routing-table", "vrf", "connect", "listen", "multihop", "nexthop-choice", "input.filter", "input.filter-nlri", "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6", "output.filter-chain", "output.filter-select", "output.network", "output.redistribute", "output.default-originate", "output.remove-private-as", "output.as-override", "comment"})
	nativeCHRProbeExpected(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, "instance", instanceName, instanceID, true)
	nativeCHRProbeExpected(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, "routing-table", bgpTableName, bgpTableID, true)
	for _, expected := range []struct{ field, value string }{{"nexthop-choice", "propagate"}, {"local.role", "ebgp-rs"}, {"templates", ""}, {"output.default-originate", "never"}, {"afi", "ip"}} {
		nativeCHRProbeExpected(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, expected.field, expected.value, "", true)
	}
	nativeCHRProbeExpected(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, "as", "", "", false)
	nativeCHRProbeExpected(t, ctx, client, rosapi.BGPConnection, connectionID, connectionRow, "as", "64512", "", false)

	for _, field := range []string{"input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6"} {
		if rosapi.ValidateMutation(rosapi.Mutation{Resource: rosapi.BGPConnection, Action: rosapi.Set, ID: connectionID, Attributes: []rosapi.Attribute{{Name: field, Value: ""}}}) == nil {
			t.Fatal("unsupported_empty_limit_setter_blocked=false")
		}
		result, setErr := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.BGPConnection, Action: rosapi.Set, ID: connectionID, Attributes: []rosapi.Attribute{{Name: field, Value: "10"}}})
		t.Logf("probe_limit_field=%s nonempty_set_acknowledged=%t api_code=%s uncertain=%t", field, setErr == nil && result.Acknowledged, nativeCHRAPICode(setErr), rosapi.IsUncertain(setErr))
		if setErr != nil || !result.Acknowledged {
			t.Errorf("compiler_required_limit_setter_accepted=false probe_limit_field=%s api_code=%s", field, nativeCHRAPICode(setErr))
			continue
		}
		before, getErr := client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, field)
		if getErr != nil || !before.Known || before.Value != "10" {
			t.Errorf("probe_limit_nonempty_exact=false api_code=%s", nativeCHRAPICode(getErr))
			continue
		}
		result, unsetErr := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.BGPConnection, Action: rosapi.Unset, ID: connectionID, Property: field})
		t.Logf("probe_limit_field=%s unset_acknowledged=%t api_code=%s uncertain=%t", field, unsetErr == nil && result.Acknowledged, nativeCHRAPICode(unsetErr), rosapi.IsUncertain(unsetErr))
		if unsetErr != nil || !result.Acknowledged {
			t.Errorf("compiler_required_limit_unset_accepted=false probe_limit_field=%s api_code=%s", field, nativeCHRAPICode(unsetErr))
			continue
		}
		after, getErr := client.ReadProperty(ctx, rosapi.BGPConnection, connectionID, field)
		if getErr != nil {
			t.Errorf("probe_limit_after_unset_readable=false api_code=%s", nativeCHRAPICode(getErr))
			continue
		}
		nativeCHRProbeValue(t, "bgp-connection", field, "get-after-unset", after.Known, after.Value)
		if after.Known && after.Value != "" {
			t.Error("probe_limit_after_unset_still_has_explicit_value=true")
		}
	}
	nativeCHRProbeInvalidFilters(t, ctx, client, prefix, &objects)
	// Only fixture bytes are used. The getter/matcher API is intentionally opaque
	// and neither fixture key nor any native return value is ever formatted.
	fixtureKey := []byte("fixture-key")
	defer clear(fixtureKey)
	checkCredential := func(stage string, expected []byte, wantKnown bool) {
		t.Helper()
		credential, err := client.ReadCredential(ctx, connectionID)
		t.Logf("probe_md5_stage=%s full_getter_accepted=%t full_known=%t full_matches_expected=%t full_matches_empty=%t api_code=%s", stage, err == nil, credential.Known, credential.Matches(expected), credential.Matches(nil), nativeCHRAPICode(err))
		if err != nil || credential.Known != wantKnown || wantKnown && !credential.Matches(expected) || !wantKnown && credential.Matches(nil) {
			t.Errorf("compiler_authentication_verification_available=false probe_md5_stage=%s api_code=%s", stage, nativeCHRAPICode(err))
		}
		limited, err := rosapi.Dial(ctx, config("mdlab-read", limitedPassword))
		if err != nil {
			t.Errorf("probe_limited_authenticated=false api_code=%s", nativeCHRAPICode(err))
			return
		}
		defer limited.Close()
		restricted, readErr := limited.ReadCredential(ctx, connectionID)
		t.Logf("probe_md5_stage=%s limited_getter_accepted=%t limited_known=%t limited_matches_fixture=%t limited_matches_empty=%t api_code=%s", stage, readErr == nil, restricted.Known, restricted.Matches(fixtureKey), restricted.Matches(nil), nativeCHRAPICode(readErr))
		if readErr == nil && restricted.Known {
			t.Error("probe_without_sensitive_policy_returned_known_secret_result=true")
		}
	}
	checkCredential("unset", nil, false)
	for _, step := range []struct {
		stage string
		value []byte
	}{{"canonical-empty", nil}, {"set", fixtureKey}, {"clear", nil}} {
		result, err := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.BGPConnection, Action: rosapi.Set, ID: connectionID, Attributes: []rosapi.Attribute{{Name: "tcp-md5-key", Value: string(step.value), Sensitive: true}}})
		t.Logf("probe_md5_stage=%s setter_acknowledged=%t api_code=%s uncertain=%t", step.stage, err == nil && result.Acknowledged, nativeCHRAPICode(err), rosapi.IsUncertain(err))
		if err != nil || !result.Acknowledged {
			t.Errorf("compiler_required_md5_setter_accepted=false probe_md5_stage=%s api_code=%s", step.stage, nativeCHRAPICode(err))
			continue
		}
		checkCredential(step.stage, step.value, true)
	}
	safe, err = client.ReadSafeMode(ctx)
	if err != nil || safe.Enabled || safe.Current {
		t.Errorf("probe_final_safe_mode_idle=false api_code=%s", nativeCHRAPICode(err))
	}
}

// Equality observations use fixed fixture expectations; neither requested nor
// returned strings/IDs are logged. Readback by native reference ID is recorded
// separately and is never silently normalized by the transport.
func nativeCHRProbeExpected(t *testing.T, ctx context.Context, client *rosapi.Client, resource rosapi.Resource, id string, row rosapi.Record, field, expected, referenceID string, required bool) {
	t.Helper()
	printed, present := row[field]
	property, err := client.ReadProperty(ctx, resource, id, field)
	printMatch := present && printed == expected
	getMatch := err == nil && property.Known && property.Value == expected
	t.Logf("probe_kind=%s probe_field=%s expected_is_empty=%t print_matches_expected=%t getter_matches_expected=%t print_matches_reference_id=%t getter_matches_reference_id=%t getter_known=%t api_code=%s", resource, field, expected == "", printMatch, getMatch, referenceID != "" && present && printed == referenceID, referenceID != "" && property.Known && property.Value == referenceID, property.Known, nativeCHRAPICode(err))
	if err != nil || required && !printMatch && !getMatch {
		t.Errorf("compiler_required_expected_value_verified=false probe_kind=%s probe_field=%s api_code=%s", resource, field, nativeCHRAPICode(err))
	}
}

// Negative rules are isolated from all BGP connections. They must either fail
// creation, or be explicitly marked inactive. This probes filter compilation
// semantics without confusing it with an inactive BGP instance/session.
func nativeCHRProbeInvalidFilters(t *testing.T, ctx context.Context, client *rosapi.Client, prefix string, objects *[]nativeCHRProbeObject) {
	t.Helper()
	storedInvalid := false
	for _, test := range []struct{ label, rule string }{{"syntax", "if ("}, {"unknown_property", "if (md_native_probe_unknown_attribute == 1) { accept; }"}} {
		marker := prefix + "_negative_" + test.label
		result, err := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.FilterRule, Action: rosapi.Add, Attributes: []rosapi.Attribute{{Name: "chain", Value: marker}, {Name: "comment", Value: marker}, {Name: "rule", Value: test.rule}}})
		t.Logf("probe_negative_rule=%s creation_acknowledged=%t api_code=%s uncertain=%t", test.label, err == nil && result.Acknowledged, nativeCHRAPICode(err), rosapi.IsUncertain(err))
		rows, readErr := client.Read(ctx, rosapi.FilterRule)
		if readErr != nil {
			t.Errorf("probe_negative_rule_reconciled=false api_code=%s", nativeCHRAPICode(readErr))
			if result.ID != "" {
				*objects = append(*objects, nativeCHRProbeObject{rosapi.FilterRule, result.ID, "comment", marker})
			}
			continue
		}
		matched := []rosapi.Record{}
		for _, row := range rows {
			if row["chain"] == marker && row["comment"] == marker {
				matched = append(matched, row)
			}
		}
		if len(matched) == 0 {
			rejected := err != nil
			t.Logf("probe_negative_rule=%s rejected_before_storage=%t", test.label, rejected)
			if !rejected {
				t.Error("probe_acknowledged_negative_rule_identity_found=false")
			}
			continue
		}
		if len(matched) != 1 {
			t.Error("probe_negative_rule_unique_identity=false")
			continue
		}
		row := matched[0]
		storedInvalid = true
		property, getErr := client.ReadProperty(ctx, rosapi.FilterRule, row[".id"], "inactive")
		inactive := getErr == nil && property.Known && (property.Value == "true" || property.Value == "yes")
		t.Logf("probe_negative_rule=%s inactive_known=%t inactive_true=%t api_code=%s", test.label, property.Known, inactive, nativeCHRAPICode(getErr))
		if !inactive {
			t.Error("compiler_invalid_filter_inactive_semantics_verified=false")
		}
		removed, removeErr := client.Apply(ctx, rosapi.Mutation{Resource: rosapi.FilterRule, Action: rosapi.Remove, ID: row[".id"]})
		if removeErr != nil || !removed.Acknowledged {
			t.Errorf("probe_negative_rule_explicit_cleanup=false api_code=%s uncertain=%t", nativeCHRAPICode(removeErr), rosapi.IsUncertain(removeErr))
			*objects = append(*objects, nativeCHRProbeObject{rosapi.FilterRule, row[".id"], "comment", marker})
		}
	}
	t.Logf("probe_invalid_filter_storage_observed=%t", storedInvalid)
}

func nativeCHRProbeRoutingTableExact(t *testing.T, ctx context.Context, client *rosapi.Client, id string, row rosapi.Record, expected, stage string) bool {
	t.Helper()
	printed, present := row["routing-table"]
	readback, err := client.ReadProperty(ctx, rosapi.BGPConnection, id, "routing-table")
	printExact := present && printed == expected
	getExact := err == nil && readback.Known && readback.Value == expected
	t.Logf("probe_explicit_connection_table_stage=%s print_matches_expected=%t getter_matches_expected=%t getter_known=%t api_code=%s", stage, printExact, getExact, readback.Known, nativeCHRAPICode(err))
	if !printExact || !getExact {
		t.Errorf("compiler_required_explicit_connection_table_verified=false probe_stage=%s api_code=%s", stage, nativeCHRAPICode(err))
		return false
	}
	return true
}
