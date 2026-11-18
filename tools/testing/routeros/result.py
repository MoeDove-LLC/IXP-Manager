"""Allow only the known native test's booleans/version/fixed error categories."""
import re
import live_schema
import experiments
import device_reset

LIVE_TESTS = live_schema.TESTS
GENERATED_BGP_TEST = live_schema.GENERATED_BGP_TEST
GENERATED_BGP6_TEST = live_schema.GENERATED_BGP6_TEST

TEST = "TestNativeCHRSafeModeRollback"
PROPERTY_TEST = "TestNativeCHRAPIProperties"
SSH_CANCEL_TEST = "TestNativeCHRSSHSafeModeRollback"
SSH_CONFIRM_TEST = "TestNativeCHRSSHConfirmRetains"
SSH_DISCONNECT_TEST = "TestNativeCHRSSHDisconnectRollback"
FIELDS = frozenset((
    "native_chr_fixture_enabled fixture_capabilities_readable fixture_capabilities_zero fixture_loopback_only "
    "fixture_scratch_valid fixture_file_valid fixture_file_readable fixture_file_stable fixture_file_bounded "
    "fixture_context_valid fixture_fixed_target_valid fixture_console_socket_valid fixture_ca_parsed "
    "api_tls_authenticated api_version_matches api_version baseline_tables_readable fixture_table_initially_absent "
    "fixture_initial_safe_mode_idle console_connected console_opened console_synchronized console_version_matches "
    "console_version console_initial_owner_clear console_safe_mode_taken console_current_owner_verified "
    "api_observes_other_channel_owner typed_table_add_acknowledged added_table_readback_available "
    "added_table_native_id_readback safe_mode_preserved_after_api_write console_owner_preserved_after_api_write "
    "console_cancel_session_ended_without_rollback_claim console_cancel_session_ended terminal_claimed_rollback "
    "rollback_readback_available rollback_readback_verified safe_mode_released_after_cancel"
).split())
ERROR_CODES = frozenset((
    "none unclassified invalid-input transport-failure deadline cancelled bounds-exceeded protocol-unknown "
    "remote-rejected authentication-required owner-conflict ownership-lost state-unknown session-closed"
).split())
REQUIRED = {
    "api_tls_authenticated": "true", "api_version": "7.24.6", "console_version": "7.24.6",
    "console_current_owner_verified": "true", "api_observes_other_channel_owner": "true",
    "typed_table_add_acknowledged": "true", "added_table_native_id_readback": "true",
    "console_cancel_session_ended": "true", "terminal_claimed_rollback": "false",
    "rollback_readback_verified": "true", "safe_mode_released_after_cancel": "true",
}

# Do not accept a generic probe_* prefix: both labels and each non-boolean
# value are a closed vocabulary reviewed against native_api_test.go.
PROPERTY_BOOLEANS = frozenset((
    "add_acknowledged api_version_has_longterm_suffix api_version_has_stable_suffix "
    "compiler_authentication_verification_available compiler_fib_switch_on_readback "
    "compiler_fib_unset_readback_matches_omission compiler_invalid_filter_inactive_semantics_verified "
    "compiler_reject_rule_exact_roundtrip compiler_required_add_accepted "
    "compiler_required_connection_table_setter_accepted compiler_required_expected_value_verified "
    "compiler_required_explicit_connection_table_verified compiler_required_fib_unset_accepted "
    "compiler_required_getter_accepted compiler_required_limit_setter_accepted "
    "compiler_required_limit_unset_accepted compiler_required_md5_setter_accepted "
    "compiler_valid_filter_inactive_semantics_verified creation_acknowledged empty expected_is_empty false "
    "full_getter_accepted full_known full_matches_empty full_matches_expected getter_accepted getter_is_main "
    "getter_known getter_matches_expected getter_matches_reference_id inactive_known inactive_true "
    "limited_getter_accepted limited_known limited_matches_empty limited_matches_fixture nonempty_set_acknowledged "
    "other present print_is_main print_matches_expected print_matches_reference_id "
    "probe_acknowledged_negative_rule_identity_found probe_ca_parsed probe_cleanup_identity_verified "
    "probe_connection_table_setter_after_acknowledged probe_connection_verified_disabled "
    "probe_default_connection_still_disabled_after_set probe_default_connection_table_print_present "
    "probe_default_connection_table_readable probe_exact_native_identity_unique probe_explicit_cleanup "
    "probe_explicit_connection_verified_disabled probe_fib_after_unset_readable probe_fib_false_form_rejected_locally "
    "probe_fib_getter_accepted probe_fib_switch_on_verified probe_fib_unset_acknowledged "
    "probe_fib_unset_matches_omitted_representation probe_filter_invalid_getter_unsupported probe_final_safe_mode_idle "
    "probe_initial_safe_mode_idle probe_instance_verified_disabled probe_invalid_filter_storage_observed "
    "probe_isolated_bgp_table_available probe_limit_after_unset_readable probe_limit_after_unset_still_has_explicit_value "
    "probe_limit_nonempty_exact probe_limited_authenticated probe_limited_basic_read probe_negative_rule_explicit_cleanup "
    "probe_negative_rule_reconciled probe_negative_rule_unique_identity probe_readback_available probe_reject_rule_exact "
    "probe_tables_available probe_tls_authenticated probe_unique_name_available probe_unique_table_initially_absent "
    "probe_valid_unreferenced_filter_inactive_false probe_version_matches "
    "probe_without_sensitive_policy_returned_known_secret_result rejected_before_storage setter_acknowledged true "
    "uncertain unsafe_fib_presence_form_blocked unset_acknowledged unsupported_empty_limit_setter_blocked"
).split())
PROPERTY_FIELDS = frozenset((
    ".id name fib disabled dynamic invalid used comment as router-id routing-table vrf inactive chain rule "
    "instance templates afi local.address local.role remote.address remote.as connect listen multihop nexthop-choice "
    "input.filter input.filter-nlri input.limit-process-routes-ipv4 input.limit-process-routes-ipv6 "
    "output.filter-chain output.filter-select output.network output.redistribute output.default-originate "
    "output.remove-private-as output.as-override"
).split())
PROPERTY_ENUMS = {
    "probe_kind": frozenset(("routing-table", "bgp-instance", "bgp-connection", "filter-rule")),
    "probe_field": PROPERTY_FIELDS,
    "probe_source": frozenset(("print", "get", "after-unset-print", "after-unset-get", "get-after-unset")),
    "probe_fib_case": frozenset(("omit", "on")),
    "probe_limit_field": frozenset(("input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6")),
    "probe_md5_stage": frozenset(("unset", "canonical-empty", "set", "clear")),
    "probe_negative_rule": frozenset(("syntax", "unknown_property")),
    "probe_explicit_connection_table_stage": frozenset(("after-set", "add-explicit")),
    "probe_stage": frozenset(("after-set", "add-explicit")),
    "api_code": frozenset((
        "none", "unclassified", "invalid-config", "authentication-failed", "tls-verification-or-handshake-failed",
        "transport-failed", "invalid-protocol", "limit-exceeded", "command-failed", "unsupported",
        "connection-terminated", "client-closed", "cancelled-or-deadline",
    )),
}
# Require observations from the end of the distinct property experiment, not
# the prior rollback result. Cleanup is enforced by the Go test's PASS status;
# it is explicit ID-checked removal and is never a cancellation proof.
PROPERTY_REQUIRED_OBSERVATIONS = (
    {"probe_fib_case": "omit", "add_acknowledged": "true"},
    {"probe_fib_case": "on", "add_acknowledged": "true"},
    {"probe_fib_unset_matches_omitted_representation": "true"},
    {"probe_explicit_connection_table_stage": "after-set", "print_matches_expected": "true", "getter_matches_expected": "true", "getter_known": "true"},
    {"probe_explicit_connection_table_stage": "add-explicit", "print_matches_expected": "true", "getter_matches_expected": "true", "getter_known": "true"},
    {"probe_md5_stage": "clear", "setter_acknowledged": "true"},
    {"probe_md5_stage": "clear", "full_getter_accepted": "true", "full_known": "true", "full_matches_empty": "true"},
    {"probe_md5_stage": "clear", "limited_known": "false", "limited_matches_empty": "false", "limited_matches_fixture": "false"},
)

SSH_BOOLEANS = frozenset((
    "ssh_fixture_pinned_image ssh_fixture_private_key_mode ssh_fixture_ca_parsed ssh_api_tls_authenticated "
    "ssh_api_version_matches ssh_api_device_identity_available ssh_baseline_tables_readable ssh_fixture_table_initially_absent "
    "ssh_fixture_initial_safe_mode_idle ssh_channel_opened ssh_synchronized ssh_version_matches ssh_api_same_device_verified "
    "ssh_initial_owner_clear ssh_safe_mode_taken ssh_current_owner_verified ssh_api_observes_other_channel_owner "
    "ssh_typed_table_add_acknowledged ssh_added_table_readable ssh_added_table_native_id_readback ssh_disabled_table_verified "
    "ssh_no_fib_table_verified ssh_owner_preserved_after_write ssh_safe_mode_preserved_after_write "
    "ssh_cancel_session_ended_without_rollback_claim ssh_cancel_session_ended ssh_terminal_claimed_rollback "
    "ssh_rollback_readback_available ssh_rollback_readback_verified ssh_safe_mode_released_after_cancel "
    "ssh_confirm_pinned_image ssh_confirm_private_key_mode ssh_confirm_ca_parsed ssh_confirm_api_authenticated "
    "ssh_confirm_api_version_matches ssh_confirm_api_identity_available ssh_confirm_baseline_readable "
    "ssh_confirm_table_initially_absent ssh_confirm_initial_safe_mode_idle ssh_confirm_channel_opened "
    "ssh_confirm_synchronized ssh_confirm_version_matches ssh_confirm_same_device_verified ssh_confirm_safe_mode_taken "
    "ssh_confirm_current_owner_verified ssh_confirm_api_observes_other_owner ssh_confirm_initial_undo_empty "
    "ssh_confirm_table_add_acknowledged ssh_confirm_pending_undo_known ssh_confirm_table_readback "
    "ssh_confirm_release_observed_without_commit_claim ssh_confirm_api_safe_mode_released ssh_confirm_released_undo_empty "
    "ssh_confirm_release_observed ssh_confirm_terminal_claimed_commit ssh_confirm_retained_after_release "
    "ssh_confirm_channel_closed ssh_confirm_safe_mode_idle_after_close ssh_confirm_retained_after_close "
    "ssh_confirm_independent_readback_verified ssh_confirm_explicit_cleanup ssh_confirm_cleanup_readback_available "
    "ssh_confirm_cleanup_readback_verified ssh_confirm_explicit_cleanup_after_proof ssh_confirm_retained_table_readable "
    "ssh_confirm_exact_native_identity_retained ssh_confirm_disabled_table_retained ssh_confirm_no_fib_retained"
).split())
SSH_REQUIRED = {
    SSH_CANCEL_TEST: {
        "ssh_api_tls_authenticated": "true", "ssh_api_version": "7.24.6", "ssh_version": "7.24.6",
        "ssh_api_same_device_verified": "true", "ssh_current_owner_verified": "true", "ssh_api_observes_other_channel_owner": "true",
        "ssh_added_table_native_id_readback": "true", "ssh_disabled_table_verified": "true", "ssh_no_fib_table_verified": "true",
        "ssh_cancel_session_ended": "true", "ssh_terminal_claimed_rollback": "false",
        "ssh_rollback_readback_verified": "true", "ssh_safe_mode_released_after_cancel": "true",
    },
    SSH_CONFIRM_TEST: {
        "ssh_confirm_version": "7.24.6", "ssh_confirm_same_device_verified": "true",
        "ssh_confirm_pending_undo_known": "true", "ssh_confirm_table_readback": "true",
        "ssh_confirm_release_observed": "true", "ssh_confirm_terminal_claimed_commit": "false",
        "ssh_confirm_retained_after_release": "true", "ssh_confirm_retained_after_close": "true",
        "ssh_confirm_independent_readback_verified": "true", "ssh_confirm_explicit_cleanup_after_proof": "true",
    },
}
DISCONNECT_BOOLEANS = frozenset((
    "native_chr_failure_fixture_enabled ssh_disconnect_pinned_image ssh_disconnect_private_key_mode ssh_disconnect_ca_parsed "
    "ssh_disconnect_api_authenticated ssh_disconnect_api_version_matches ssh_disconnect_api_identity_available "
    "ssh_disconnect_baseline_readable ssh_disconnect_table_initially_absent ssh_disconnect_initial_safe_mode_idle "
    "ssh_disconnect_channel_opened ssh_disconnect_synchronized ssh_disconnect_version_matches ssh_disconnect_same_device_verified "
    "ssh_disconnect_version_verified ssh_disconnect_initial_owner_clear ssh_disconnect_safe_mode_taken "
    "ssh_disconnect_initial_history_empty ssh_disconnect_table_add_acknowledged ssh_disconnect_added_table_readable "
    "ssh_disconnect_added_table_native_id_readback ssh_disconnect_disabled_table_verified ssh_disconnect_nonfib_table_verified "
    "ssh_disconnect_current_owner_verified ssh_disconnect_api_other_owner_verified ssh_disconnect_pending_history_known "
    "ssh_disconnect_exact_inert_table_readback ssh_disconnect_transport_closed ssh_disconnect_unverified_close_evidence "
    "ssh_disconnect_terminal_claimed_rollback ssh_disconnect_original_api_readback ssh_disconnect_owner_readback "
    "ssh_disconnect_rollback_readback_verified ssh_disconnect_wait_exhausted ssh_disconnect_wait_bound_verified "
    "ssh_disconnect_owner_released ssh_disconnect_manual_cleanup"
).split())
DISCONNECT_REQUIRED = {
    "ssh_disconnect_version_verified": "true", "ssh_disconnect_same_device_verified": "true",
    "ssh_disconnect_current_owner_verified": "true", "ssh_disconnect_api_other_owner_verified": "true",
    "ssh_disconnect_exact_inert_table_readback": "true", "ssh_disconnect_pending_history_known": "true",
    "ssh_disconnect_transport_closed": "true", "ssh_disconnect_terminal_claimed_rollback": "false",
    "ssh_disconnect_rollback_readback_verified": "true", "ssh_disconnect_owner_released": "true",
    "ssh_disconnect_manual_cleanup": "false",
}


def valid_field(field, value, properties, ssh=False, disconnect=False):
    if disconnect:
        if field == "ssh_disconnect_wait_ms":
            return re.fullmatch(r"(?:0|[1-9][0-9]{0,5})", value) is not None and int(value) <= 600000
        return ((field in DISCONNECT_BOOLEANS or field in FIELDS) and value in ("true", "false")) or (field == "terminal_code" and value in ERROR_CODES) or (field == "api_code" and value in PROPERTY_ENUMS["api_code"])
    if ssh:
        if field in ("ssh_api_version", "ssh_version", "ssh_confirm_version"):
            return value == "7.24.6"
        return ((field in SSH_BOOLEANS or field in FIELDS) and value in ("true", "false")) or (field == "terminal_code" and value in ERROR_CODES) or (field == "api_code" and value in PROPERTY_ENUMS["api_code"])
    if properties:
        return ((field in PROPERTY_BOOLEANS or field in FIELDS) and value in ("true", "false")) or value in PROPERTY_ENUMS.get(field, ())
    if field in ("api_version", "console_version"):
        return value == "7.24.6"
    return (field in FIELDS and value in ("true", "false")) or (field == "terminal_code" and value in ERROR_CODES)


def inspect_output(raw, returncode, test=TEST):
    if len(raw) > 65536 or test not in (TEST, PROPERTY_TEST, SSH_CANCEL_TEST, SSH_CONFIRM_TEST, SSH_DISCONNECT_TEST, GENERATED_BGP_TEST, GENERATED_BGP6_TEST, *LIVE_TESTS, *experiments.TESTS):
        return False, []
    output, values = [], {}
    observations = []
    properties = test == PROPERTY_TEST
    ssh = test in (SSH_CANCEL_TEST, SSH_CONFIRM_TEST)
    live = test in LIVE_TESTS or test in (GENERATED_BGP_TEST, GENERATED_BGP6_TEST)
    disconnect = test == SSH_DISCONNECT_TEST
    experiment = test in experiments.TESTS
    ran = passed = rejected = False
    for line in raw.decode("utf-8", "replace").splitlines():
        if line == "=== RUN   " + test:
            if ran:
                rejected = True
            ran = True
            output.append(line)
            continue
        if re.fullmatch(r"--- PASS: " + test + r" \([0-9.]+s\)", line):
            if not ran or passed:
                rejected = True
            passed = True
            output.append(line)
            continue
        if line == "PASS":
            output.append(line)
            continue
        files = r"(?:native_integration_test|native_api_test)" if properties else "native_integration_test"
        if ssh:
            files = r"(?:native_integration_test|native_ssh_test)"
        if live:
            files = r"(?:native_test|native_supervisor_test|native_restart_test)"
            if test == GENERATED_BGP_TEST:
                files = r"(?:native_test|native_generated_bgp_test)"
            if test == GENERATED_BGP6_TEST:
                files = r"(?:native_test|native_generated_bgp6_test)"
        if disconnect:
            files = r"(?:native_integration_test|native_failures_test)"
        if experiment:
            files = r"(?:native_integration_test|native_owned_test)" if test == experiments.CASES["owned"] else r"(?:native_test|native_extensions_test)"
            if test in device_reset.CASES.values():
                files = "native_device_reset_test"
            if test in experiments.TERMINAL_TESTS:
                files = "native_owned_order_test"
            if test in (experiments.CASES["flag-query"], experiments.CASES["flag-query-presence"]):
                files = r"(?:native_test|native_flag_query_test)"
            if test == experiments.CASES["discard"]:
                files = r"(?:native_test|native_discard_test)"
            if test == experiments.CASES["discard-global-tail"]:
                files = r"(?:native_test|native_discard_test|native_global_tail_test)"
            if test in (experiments.CASES["discard1000"], experiments.CASES["discard1000-global-tail"]):
                files = r"(?:native_test|native_extensions_test|native_removal_scale_test)"
            if test == experiments.large_cleanup.TEST:
                files = r"(?:native_test|native_extensions_test|native_cleanup_scale_test)"
            if test in (experiments.CASES["read-scale"], experiments.CASES["read-scale-from"]):
                files = r"(?:native_test|native_extensions_test|native_read_scale_test)"
            if test == experiments.CASES["presence-id-scale"]:
                files = r"(?:native_test|native_extensions_test|native_read_scale_test|native_presence_id_scale_test)"
            if test == experiments.CASES["selection-scale"]:
                files = r"(?:native_test|native_extensions_test|native_read_scale_test|native_selection_scale_test)"
            if test == experiments.CASES["presence"]:
                files = r"(?:native_test|native_presence_test)"
            if test == experiments.CASES["discard-bounded"]:
                files = r"(?:native_test|native_discard_test|native_global_tail_test|native_bounded_discard_test)"
            if test == experiments.CASES["transition-budget"]:
                files = r"(?:native_test|native_transition_budget_test)"
            if test == experiments.CASES["review-reuse"]:
                files = r"(?:native_test|native_rollback_review_test)"
        match = re.fullmatch(r"\s+" + files + r"\.go:[0-9]+: (.+)", line)
        if not match:
            # No-test, skip, panic, arbitrary prints and remote traces are not
            # accepted as native evidence and are never forwarded verbatim.
            rejected = True
            continue
        parsed = {}
        for word in match[1].split():
            field, separator, value = word.partition("=")
            if experiment and test in (*device_reset.CASES.values(), experiments.large_cleanup.TEST, experiments.CASES["discard-bounded"], experiments.CASES["transition-budget"], experiments.CASES["selection-scale"], experiments.CASES["flag-query-presence"], experiments.CASES["discard-global-tail"], experiments.CASES["discard1000"], experiments.CASES["discard1000-global-tail"], experiments.CASES["read-scale"], experiments.CASES["read-scale-from"], experiments.CASES["presence-id-scale"]):
                valid = experiments.valid_case_field(test, field, value)
            else:
                valid = (experiments.valid_field(field, value) or valid_field(field, value, False)) if experiment else live_schema.valid_field(field, value) if live else valid_field(field, value, properties, ssh=ssh, disconnect=disconnect)
            if not separator or not valid or field in parsed:
                rejected = True
                parsed = {}
                break
            parsed[field] = value
        if parsed:
            if experiment and not experiments.valid_case_row(test, parsed):
                rejected = True
                continue
            if live and not live_schema.valid_row(test, parsed):
                rejected = True
                continue
            if not ran or passed:
                rejected = True
            prefix = "native_experiment " if experiment else "native_disconnect " if disconnect else "native_live " if live else "native_properties " if properties else "native_ssh " if ssh else "native_test "
            output.append(prefix + " ".join(field + "=" + value for field, value in parsed.items()))
            observations.append(parsed)
            for field, value in parsed.items():
                if not properties and not live and not experiment and field in values and values[field] != value:
                    rejected = True
                values[field] = value
    if experiment:
        complete = experiments.complete(test, observations)
    elif live:
        complete = live_schema.complete(test, observations)
    elif properties:
        complete = all(any(all(row.get(key) == value for key, value in required.items()) for row in observations) for required in PROPERTY_REQUIRED_OBSERVATIONS)
    else:
        required = DISCONNECT_REQUIRED if disconnect else SSH_REQUIRED[test] if ssh else REQUIRED
        complete = all(values.get(key) == value for key, value in required.items())
        if disconnect:
            complete = complete and "ssh_disconnect_wait_ms" in values
    verified = returncode == 0 and ran and passed and not rejected and complete
    return verified, output
