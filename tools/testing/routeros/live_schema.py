"""Closed native-live test authorization and redacted-output schema.

New TestNativeCHRLive* names are not executable merely by sharing the prefix.
Each name needs explicit review, required evidence and bounded log fields here.
"""
import re

GENERATED_BGP_TEST = "TestNativeCHRLiveGeneratedTwoPeerIPv4Policy"
GENERATED_BGP6_TEST = "TestNativeCHRLiveGeneratedIPv6TwoPeerPolicy"

TESTS = (
    "TestNativeCHRLiveObserve",
    "TestNativeCHRLiveGeneratedPrepare",
    "TestNativeCHRLiveResetIdle",
    "TestNativeCHRLiveSupervisorCancel",
    "TestNativeCHRLiveSupervisorConfirm",
    "TestNativeCHRLiveSupervisorRestartReadOnly",
)
REQUIRED = {
    TESTS[0]: {
        "live_complete_empty_namespace": "true", "cross_channel_identity": "true", "pending_count_known": "true",
        "native_activation_permitted": "false", "live_readback_after_owner_close": "true",
    },
    TESTS[1]: {"live_generated_prepare_native_readback": "true", "activation_exercised": "false", "undo_budget_verified": "false"},
    TESTS[2]: {"idle_reset_new_unowned_session": "true", "cross_channel_identity": "true", "old_owner_resumed": "false"},
    TESTS[3]: {"native_supervisor_cancel_checkpoint_verified": "true", "prepared_retained": "true", "manual_cleanup_before_proof": "false"},
    TESTS[4]: {"native_supervisor_confirm_strict_snapshot_verified": "true", "post_owner_close_retained": "true", "manual_cleanup_before_proof": "false"},
    TESTS[5]: {
        "native_supervisor_restart_readonly_verified": "true", "exact_checkpoint_verified": "true",
        "fresh_runtime_identity_verified": "true", "old_confirmation_rejected": "true",
        "forbidden_native_calls": "0", "manual_cleanup_before_proof": "false",
    },
    GENERATED_BGP_TEST: {
        **{ "generated_bgp_" + field: "true" for field in (
            "baseline_exact", "source_binding", "as_path_transparent", "next_hop_transparent", "self_echo_absent",
            "policy_only", "cached_withdrawal", "switch_path_transparent", "switch_next_hop_transparent",
            "ipv4_fixture_verified", "cached_cancel_restored", "sender_announced_once", "sessions_single_open", "no_refresh",
            "rib_baseline", "rib_switch", "rib_cancel",
        )},
        **{ "generated_bgp_" + field: "false" for field in (
            "manual_routing_changes", "ipv6_verified", "packet_forwarding_verified",
        )},
    },
    GENERATED_BGP6_TEST: {
        **{"generated6_bgp_" + field: "true" for field in (
            "native_sources_verified", "ipv6_socket_verified", "mp_ipv6_negotiated", "open_sent_once",
            "baseline_exact", "source_binding", "as_path_transparent", "next_hop_transparent", "self_echo_absent",
            "policy_only", "cached_withdrawal", "switch_path_transparent", "switch_next_hop_transparent",
            "ipv6_fixture_verified", "cached_cancel_restored", "sender_announced_once", "sessions_single_open",
            "no_refresh", "ipv6_verified", "rib_baseline", "rib_switch", "rib_cancel",
        )},
        "generated6_bgp_manual_policy_changes": "false",
        "generated6_bgp_packet_forwarding_verified": "false",
    },
}
SUPERVISOR_TESTS = frozenset(TESTS[3:])
SUPERVISOR_MODES = {TESTS[3]: "cancel", TESTS[4]: "confirm", TESTS[5]: "restart"}
BOOLEANS = frozenset((
    "native_chr_fixture_enabled fixture_caps_readable fixture_capabilities_zero fixture_loopback_only fixture_context_readable "
    "fixture_context_valid fixture_fixed_target_valid fixture_ca_readable fixture_ca_pem fixture_ca_certificate "
    "fixture_host_pin_readable fixture_host_pin_exact fixture_host_key_valid fixture_password_readable fixture_api_verified "
    "fixture_stable_identity live_open live_observation live_complete_empty_namespace_and_owner live_complete_empty_namespace "
    "cross_channel_identity pending_count_known native_activation_permitted unverified_native_gate_closed live_close_owner "
    "live_readback_after_owner_close live_prepare_open live_prepare_initial_namespace_empty live_prepare_plan_valid "
    "live_prepare_bridge_valid live_prepare_cleanup live_prepare_readback_before live_prepare_reference live_prepare_inert_command "
    "live_prepare_native_accept live_prepare_native_id live_prepare_readback_after live_prepare_exact_id_readback "
    "live_prepare_credential_readback live_prepare_exact_attribute live_prepare_unset_readback live_generated_rule_usable "
    "live_generated_connection_inert live_generated_table_nonfib live_generated_coverage live_generated_prepare_native_readback "
    "activation_exercised undo_budget_verified live_connection_boolean_inventory live_connection_boolean_exact_id "
    "live_connection_as instance_bound_unique print_equals_instance getter_no_ret_empty getter_equals_instance "
    "live_connection_as_inventory live_connection_as_exact_id live_connection_as_evidence_available "
    "print_zero print_zero_pair print_equals_instance_pair "
    "explicit_yes print_present print_empty print_true print_false getter_known getter_empty getter_true getter_false "
    "live_connection_explicit_connect_listen_verified idle_reset_open idle_reset_baseline idle_reset_close_old "
    "idle_reset_new_channel idle_reset_new_authority idle_reset_new_unowned_session old_owner_resumed "
    "native_supervisor_open native_supervisor_initial_namespace native_supervisor_plan_valid native_supervisor_open_journal "
    "native_supervisor_private_journal_create native_supervisor_private_journal_mode native_supervisor_private_journal_owner "
    "native_supervisor_cleanup_owner_clear native_supervisor_cleanup_received_id native_supervisor_stage_pending "
    "native_supervisor_pending_owner native_supervisor_cancel_verified native_supervisor_cancel_observation "
    "native_supervisor_checkpoint_digest native_supervisor_prepared_checkpoint_equal native_supervisor_actual_history_bound "
    "native_supervisor_cancel_checkpoint_verified prepared_retained manual_cleanup_before_proof "
    "native_supervisor_confirm_verified native_supervisor_confirm_strict_snapshot native_supervisor_confirm_connections_enabled "
    "native_supervisor_confirm_desired_roundtrip native_supervisor_confirm_digest native_supervisor_confirm_owner_close "
    "native_supervisor_confirm_postclose_readback native_supervisor_confirm_postclose_retained "
    "native_supervisor_confirm_strict_snapshot_verified post_owner_close_retained "
    "native_restart_checkpoint_available native_restart_checkpoint_digest native_restart_old_supervisor_closed "
    "native_restart_old_runtime_closed native_restart_fresh_runtime native_restart_journal_authenticated "
    "native_restart_rollback_readback native_restart_exact_checkpoint_without_owner_reuse "
    "native_restart_old_confirmation_rejected native_restart_recovery_read_only "
    "native_supervisor_restart_readonly_verified exact_checkpoint_verified fresh_runtime_identity_verified old_confirmation_rejected "
    "generated_bgp_fixture_enabled generated_bgp_context_valid generated_bgp_runtime generated_bgp_initial_wire "
    "generated_bgp_initial_namespace generated_bgp_first_plan generated_bgp_journal generated_bgp_supervisor "
    "generated_bgp_bootstrap_stage generated_bgp_bootstrap_confirm generated_bgp_bootstrap_snapshot generated_bgp_bootstrap_roundtrip "
    "generated_bgp_sessions generated_bgp_announce generated_bgp_baseline generated_bgp_update_base generated_bgp_update_plan "
    "generated_bgp_update_stage generated_bgp_cached_switch generated_bgp_checkpoint generated_bgp_cancel "
    "generated_bgp_cancel_snapshot generated_bgp_cancel_checkpoint generated_bgp_cached_cancel "
    "generated_bgp_baseline_exact generated_bgp_source_binding generated_bgp_as_path_transparent generated_bgp_next_hop_transparent "
    "generated_bgp_self_echo_absent generated_bgp_policy_only generated_bgp_cached_withdrawal generated_bgp_switch_path_transparent "
    "generated_bgp_switch_next_hop_transparent generated_bgp_ipv4_fixture_verified generated_bgp_cached_cancel_restored "
    "generated_bgp_sender_announced_once generated_bgp_sessions_single_open generated_bgp_no_refresh "
    "generated_bgp_manual_routing_changes generated_bgp_ipv6_verified generated_bgp_packet_forwarding_verified "
    "generated_bgp_attribute_diagnostic generated_bgp_origin_match generated_bgp_path_match generated_bgp_next_hop_match "
    "generated_bgp_path_empty generated_bgp_path_one_sequence generated_bgp_path_only_source generated_bgp_path_server_then_source "
    "generated_bgp_path_contains_server generated_bgp_path_contains_recipient generated_bgp_next_hop_server "
    "generated_bgp_next_hop_recipient generated_bgp_next_hop_gateway generated_bgp_origin_igp generated_bgp_origin_egp "
    "generated_bgp_origin_incomplete"
).split())
RESOURCES = frozenset(("routing-table", "bgp-instance", "bgp-connection", "filter-rule"))
FIELDS = frozenset((
    ".id name fib disabled dynamic invalid used comment as router-id routing-table vrf inactive chain rule "
    "instance templates afi local.address local.role remote.address remote.as connect listen multihop nexthop-choice "
    "input.filter input.filter-nlri input.limit-process-routes-ipv4 input.limit-process-routes-ipv6 "
    "output.filter-chain output.filter-select output.network output.redistribute output.default-originate output.default-prepend "
    "output.remove-private-as output.as-override tcp-md5-key"
).split())
API_CODES = frozenset((
    "none", "unclassified", "invalid-config", "authentication-failed", "tls-verification-or-handshake-failed",
    "transport-failed", "invalid-protocol", "limit-exceeded", "command-failed", "unsupported",
    "connection-terminated", "client-closed", "cancelled-or-deadline",
))
ENUMS = {
    "resource": RESOURCES,
    "action": frozenset(("add", "set", "unset", "enable", "disable", "remove")),
    "field": FIELDS,
    "live_connection_switch_field": frozenset(("connect", "listen")),
    "api_code": API_CODES,
    "native_supervisor_mode": frozenset(("cancel", "confirm", "restart")),
    "phase": frozenset(("prepare", "activate")),
}
FAILURES = frozenset((
    "none unclassified api-device-or-version-mismatch ssh-identity-unavailable ssh-version-unverified ssh-identity-unverified "
    "cross-channel-device-mismatch api-inventory-failed inventory-object-bound property-getter-bound inventory-inspection-failed "
    "inventory-digest-failed manifest-peer-bound invalid-known-peer credential-observation-failed manifest-target-mismatch "
    "configuration-changed-during-observation credentials-changed-during-observation private-runtime-directory private-runtime-file "
    "invalid-api-password-file api-authentication-or-transport ssh-authentication-or-transport ssh-synchronization api-inventory "
    "runtime-pin-file-changed operation-deadline runtime-closed runtime-not-bindable plan-target-mismatch invalid-canonical-plan "
    "native-mutation-profile-unverified mutation-not-bound operation-order-mismatch prepare-owner-conflict "
    "activation-owner-or-budget-unknown unsupported-operation-phase mutation-reference-unverified mutation-does-not-match-compiled-plan "
    "owner-channel-ended api-mutation-failed mutation-receipt-incomplete native-activation-profile-unverified owner-take-not-permitted "
    "owner-conflict-or-unknown owner-take-uncertain owner-take-not-verified owner-query-failed owner-not-taken-by-runtime "
    "confirmation-not-permitted confirmation-owner-unknown confirmation-release-uncertain original-owner-unavailable "
    "cancellation-unverified private-runtime-cleanup runtime-close-failed invalid-private-file-path private-file-permissions-or-size "
    "private-file-unreadable private-file-parent-invalid private-file-invalid private-file-read-not-stable invalid-private-config "
    "invalid-ssh-host-pin ambiguous-known-hosts-entry multiple-target-host-keys unsupported-ssh-host-key invalid-ssh-host-key "
    "ssh-host-key-type-mismatch ssh-host-key-pin-mismatch target-host-key-missing unsupported-or-invalid-target-config "
    "invalid-identity-source invalid-api-target invalid-ssh-target invalid-live-bounds invalid-api-ca idle-session-reset-not-permitted "
    "idle-session-api-owner-not-clear idle-session-api-identity-mismatch idle-session-old-channel-close idle-session-ssh-connect "
    "idle-session-ssh-synchronize idle-session-ssh-owner-not-clear idle-session-api-owner-changed idle-session-closed-during-reset "
    "idle-session-randomness native-history-before-unknown native-history-before-owner-mismatch native-history-after-unknown "
    "native-prepare-created-owner native-activation-lost-owner native-checkpoint-owner-unknown native-checkpoint-credential-count "
    "native-checkpoint-empty-credential-unverified native-checkpoint-connection-not-disabled native-recovery-forbidden-call "
    "generated-unclassified generated-control-action generated-control-socket generated-control-io generated-control-response "
    "generated-peer-failed generated-counter-invalid generated-event-invalid generated-attribute-changed generated-rib-invalid "
    "generated-source-reflection generated-convergence-timeout generated-checkpoint-owner generated-checkpoint-connection "
    "generated-checkpoint-credential generated-prepare-changed-wire generated-as-path-changed generated-next-hop-changed generated-origin-changed"
).split())
CONTROL_FAILURES = frozenset((
    "activation-budget-exceeded add-id-readback-mismatch add-readback-not-exact added-object-not-exact added-object-not-observed "
    "ambiguous-observed-id baseline-owner-or-credentials-unverified confirm-outcome-unknown confirm-readback-unknown "
    "confirmation-not-authorized confirmation-timeout-outside-bounds desired-changed-before-confirm desired-final-readback-mismatch "
    "exclusive-access-precondition-required external-state-changed idle-session-reset-failed incomplete-observation "
    "invalid-observed-attributes invalid-timeout-bounds invalid-write-result journal-begin-invalid journal-capacity-insufficient "
    "journal-checkpoint-invalid journal-deadline-invalid journal-encode journal-event-unknown journal-integrity-unknown "
    "journal-intent-invalid journal-invalid-json journal-key-invalid journal-key-missing journal-key-random journal-observation-invalid "
    "journal-readback-digest journal-readback-invalid journal-record-limit journal-result-invalid journal-size-limit journal-status-invalid "
    "journal-sync-failed journal-torn-tail journal-trailing-data journal-unavailable journal-unwritable journal-write-failed "
    "mutation-object-not-owned mutation-readback-mismatch native-adapter-gates-unverified no-cancellable-transaction "
    "no-recoverable-transaction observation-failed observation-row-limit observation-time-invalid operation-resolution-failed "
    "owner-query-failed owner-release-not-proven owner-session-lost owner-session-unavailable plan-credentials-invalid plan-invalid "
    "prepare-requires-inspection prepared-credentials-unverified private-directory-create private-directory-open "
    "private-directory-permissions private-directory-required private-directory-stat private-directory-sync private-file-existence "
    "private-file-open private-file-permissions private-file-stat private-file-sync recovery-state-not-proven unconfirmed-candidate-retained request-target-mismatch "
    "reviewed-baseline-changed runtime-plan-binding-failed runtime-target-mismatch safe-mode-active-during-prepare "
    "state-changed-before-write strict-baseline-snapshot-missing supervisor-already-running supervisor-unavailable take-unverified "
    "target-binding-invalid target-binding-mismatch target-binding-missing transaction-id-random transaction-pending "
    "undo-history-unknown-or-exhausted unsupported-state-action unsupported-unset-property unverified-credential-value "
    "unverified-observation write-outcome-unknown write-readback-unavailable"
).split())
INSPECTION_CODES = frozenset((
    "unsupported-filter-rule-disabled-selection incomplete-filter-rule-disabled-selection invalid-filter-rule-disabled-selection filter-rule-disabled-selection-conflict "
    "unknown-property unknown-flag unsupported-flag row-limit unrecognized-property invalid-property invalid-or-duplicate-id "
    "invalid-or-duplicate-name unverified-transport target-version-mismatch unknown-device-identity-source target-device-mismatch "
    "inconsistent-ssh-evidence cross-channel-mismatch unsupported-property-evidence unknown-or-duplicate-property-evidence "
    "incomplete-property-getter property-read-conflict unknown-property-value invalid-property-evidence incomplete-inventory "
    "inventory-row-limit inventory-byte-limit unrecognized-owner-object foreign-reference unresolved-template-reference "
    "duplicate-or-moved-rule rule-body-drift foreign-reference-or-unrecognized-rule missing-rule rule-order-drift "
    "unowned-existing-namespace missing-owned-object unrecognized-owned-property invalid-owned-property missing-owned-connection "
    "unverified-credential"
).split())
INSPECTION_RESOURCES = RESOURCES | frozenset(("system-resource", "system-license", "routerboard"))
INSPECTION_FIELDS = FIELDS | frozenset((
    "attributes identity version system-id software-id serial-number property capability rows references authentication "
    "configuration unused-family-limit numeric connection"
).split())

# Fixed fields from the separately selected IPv6 native fixture.
BOOLEANS = BOOLEANS | frozenset((
    "generated6_bgp_announce generated6_bgp_as_path_transparent generated6_bgp_attribute_diagnostic generated6_bgp_baseline "
    "generated6_bgp_baseline_exact generated6_bgp_bootstrap_confirm generated6_bgp_bootstrap_roundtrip generated6_bgp_bootstrap_snapshot "
    "generated6_bgp_bootstrap_stage generated6_bgp_cached_cancel generated6_bgp_cached_cancel_restored generated6_bgp_cached_switch "
    "generated6_bgp_cached_withdrawal generated6_bgp_cancel generated6_bgp_cancel_checkpoint generated6_bgp_cancel_snapshot "
    "generated6_bgp_checkpoint generated6_bgp_connect_once generated6_bgp_context_valid generated6_bgp_first_plan "
    "generated6_bgp_fixture_enabled generated6_bgp_initial_namespace generated6_bgp_initial_wire generated6_bgp_ipv6_fixture_verified "
    "generated6_bgp_ipv6_socket_verified generated6_bgp_ipv6_verified generated6_bgp_journal generated6_bgp_link_local_absent "
    "generated6_bgp_manual_policy_changes generated6_bgp_mp_ipv6_negotiated generated6_bgp_native_sources_verified generated6_bgp_next_hop_gateway "
    "generated6_bgp_next_hop_match generated6_bgp_next_hop_recipient generated6_bgp_next_hop_server generated6_bgp_next_hop_transparent "
    "generated6_bgp_no_refresh generated6_bgp_open_sent_once generated6_bgp_origin_egp generated6_bgp_origin_igp "
    "generated6_bgp_origin_incomplete generated6_bgp_origin_match generated6_bgp_packet_forwarding_verified generated6_bgp_path_contains_recipient "
    "generated6_bgp_path_contains_server generated6_bgp_path_empty generated6_bgp_path_match generated6_bgp_path_one_sequence "
    "generated6_bgp_path_only_source generated6_bgp_path_server_then_source generated6_bgp_policy_only generated6_bgp_runtime "
    "generated6_bgp_self_echo_absent generated6_bgp_sender_announced_once generated6_bgp_sessions generated6_bgp_sessions_single_open "
    "generated6_bgp_source_binding generated6_bgp_supervisor generated6_bgp_switch_next_hop_transparent generated6_bgp_switch_path_transparent "
    "generated6_bgp_update_base generated6_bgp_update_plan generated6_bgp_update_stage "
).split())
BOOLEANS |= frozenset(field for test in (GENERATED_BGP_TEST, GENERATED_BGP6_TEST) for field in REQUIRED[test])
FAILURES = FAILURES | frozenset((
    "generated6-as-path-changed generated6-checkpoint-connection generated6-checkpoint-credential "
    "generated6-checkpoint-owner generated6-control-action generated6-control-io "
    "generated6-control-response generated6-control-socket generated6-convergence-timeout "
    "generated6-counter-invalid generated6-event-invalid generated6-ipv6-transport-unverified "
    "generated6-link-local-next-hop-changed generated6-next-hop-changed generated6-origin-changed "
    "generated6-peer-failed generated6-prepare-changed-wire generated6-rib-invalid "
    "generated6-source-reflection generated6-unclassified "
).split())


def valid_code(value):
    if value in FAILURES or value in CONTROL_FAILURES or value in ("rs-rib-ssh", "rs-rib-response", "rs-rib-foreign-table", "rs-rib-route-missing", "rs-rib-nonfib-table"):
        return True
    prefix = "required-property-getter-failed-"
    if value.startswith(prefix):
        tail = value[len(prefix):]
        return any(tail.startswith(resource + "-") and tail[len(resource) + 1:] in FIELDS for resource in RESOURCES)
    if value.startswith("inspection-"):
        tail = value[len("inspection-"):]
        return any(tail == code + "-" + resource + "-" + field for code in INSPECTION_CODES for resource in INSPECTION_RESOURCES for field in INSPECTION_FIELDS)
    return False


def valid_field(field, value):
    if field == "forbidden_native_calls":
        return value == "0"
    if field in BOOLEANS:
        return value in ("true", "false")
    if field == "operation_index":
        return re.fullmatch(r"(?:0|[1-9][0-9]{0,5})", value) is not None and int(value) <= 100000
    if field in ("history_before", "history_after", "history_delta"):
        return re.fullmatch(r"(?:0|[1-9][0-9]?)", value) is not None and int(value) <= 80
    if field == "code":
        return valid_code(value)
    return value in ENUMS.get(field, ())


def valid_row(test, row):
    history = ("history_before", "history_after", "history_delta")
    if any(field in row for field in (*history, "native_supervisor_mode")):
        expected = SUPERVISOR_MODES.get(test, "")
        fields = ("native_supervisor_mode", "phase", "resource", "action", *history)
        if not expected or any(field not in row for field in fields) or row["native_supervisor_mode"] != expected:
            return False
        return int(row["history_after"]) - int(row["history_before"]) == int(row["history_delta"])
    return True


def complete(test, observations):
    if test not in REQUIRED:
        return False
    required = REQUIRED[test]
    for field, value in required.items():
        values = [row[field] for row in observations if field in row]
        if not values or any(observed != value for observed in values):
            return False
    if test in SUPERVISOR_TESTS:
        mode = SUPERVISOR_MODES[test]
        return all(any(row.get("native_supervisor_mode") == mode and row.get("phase") == phase for row in observations) for phase in ("prepare", "activate"))
    return True
