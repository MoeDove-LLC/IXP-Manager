"""Explicit, separate native experiments; no prefix-based test selection."""
import re
import live_schema
import device_reset
import large_cleanup

CASES = {
    **device_reset.CASES,
    large_cleanup.CASE: large_cleanup.TEST,
    "owned": "TestNativeCHRSSHOwnedMutations",
    "owned-delay": "TestNativeCHROwnedDelayedStreamOrder",
    "batch1000": "TestNativeCHRLiveRuleBatch1000Cancel",
    "discard1000": "TestNativeCHRLiveRuleRemoval1000Discard",
    "discard1000-global-tail": "TestNativeCHRLiveRuleRemoval1000GlobalTail",
    "read-scale": "TestNativeCHRLiveLargeReadPerformance",
    "read-scale-from": "TestNativeCHRLiveLargeReadPerformanceFrom",
    "presence-id-scale": "TestNativeCHRLiveRuleIDPresencePerformance",
    "selection-scale": "TestNativeCHRLiveDisabledSelectionScaleCompare",
    "cleanup": "TestNativeCHRLiveAutomaticCleanup",
    "flag-query": "TestNativeCHRLiveBooleanQuery",
    "flag-query-presence": "TestNativeCHRLiveBooleanPresenceQuery",
    "discard": "TestNativeCHRLivePreparedDiscardCycle",
    "discard-global-tail": "TestNativeCHRLivePreparedDiscardGlobalTailCycle",
    "presence": "TestNativeCHRLiveRuleObjectPresence",
    "review-reuse": "TestNativeCHRLiveRollbackReviewReuse",
    "transition-budget": "TestNativeCHRLiveIdentityAuthTransitionCancel",
    "discard-bounded": "TestNativeCHRLivePreparedDiscardBoundedContinue",
}
TRANSITION_REQUIRED = {
    **{"transition_"+name: "true" for name in (
        "baseline_confirmed", "baseline_strict", "baseline_credential_verified", "compiled_order_verified",
        "prepared_checkpoint_verified", "pending_strict_verified", "pending_credential_verified", "cancel_verified",
        "rollback_strict_verified", "prepared_checkpoint_restored", "old_native_ids_preserved", "old_attributes_preserved",
        "old_credential_restored", "old_auth_reference_restored", "prepared_generation_retained", "production_gates_unchanged",
    )},
    **{"transition_"+name: "false" for name in ("all_parameters_verified", "history_capacity_verified", "manual_cleanup")},
    "transition_forbidden_calls": "0", "transition_activate_operations": "9", "transition_prepared_additions": "6",
}
TRANSITION_FAILURES = frozenset((
    "transition_open transition_initial_empty transition_baseline_plan transition_private_journal transition_supervisor "
    "transition_baseline_stage transition_update_plan transition_update_stage transition_guards_verified"
).split())
TRANSITION_CODES = frozenset((
    "native-transition-plan-invalid native-transition-history-unverified native-transition-operation-order "
    "native-transition-history-before-unverified native-transition-prepare-id-invalid native-transition-history-after-unverified "
    "native-transition-checkpoint-unverified native-transition-checkpoint-credential-unverified native-transition-unclassified "
    "native-review-reuse-forbidden-call native-review-reuse-owner-reader-unavailable native-review-reuse-property-batch-unavailable"
).split())
TRANSITION_OPERATIONS = {
    "baseline": (("set-entry", "filter-rule", "set"),)*3 + (("enable-connection", "bgp-connection", "enable"),),
    "transition": (
        ("set-entry", "filter-rule", "set"), ("disable-connection", "bgp-connection", "disable"),
        ("set-connection", "bgp-connection", "set"), ("unset-unused-family-limit", "bgp-connection", "unset"),
        ("set-authentication", "bgp-connection", "set"), ("set-instance", "bgp-instance", "set"),
        ("set-entry", "filter-rule", "set"), ("set-entry", "filter-rule", "set"), ("enable-connection", "bgp-connection", "enable"),
    ),
}
TRANSITION_STEP_FIELDS = frozenset((
    "transition_stage transition_operation transition_kind transition_resource transition_action transition_history_before "
    "transition_history_after transition_history_delta transition_owner_preserved"
).split())
TRANSITION_BASELINE_FIELDS = frozenset(("transition_baseline_milliseconds", "transition_baseline_confirmed", "transition_baseline_strict", "transition_baseline_credential_verified"))
TRANSITION_UPDATE_FIELDS = frozenset((
    "transition_update_milliseconds transition_activate_operations transition_maximum_history transition_history_total_delta transition_prepared_additions "
    "transition_compiled_order_verified transition_prepared_checkpoint_verified transition_pending_strict_verified transition_pending_credential_verified"
).split())
TRANSITION_CANCEL_FIELDS = frozenset((
    "transition_cancel_milliseconds transition_cancel_verified transition_rollback_strict_verified transition_prepared_checkpoint_restored "
    "transition_old_native_ids_preserved transition_old_attributes_preserved transition_old_credential_restored transition_old_auth_reference_restored "
    "transition_prepared_generation_retained transition_forbidden_calls transition_production_gates_unchanged "
    "transition_all_parameters_verified transition_history_capacity_verified transition_manual_cleanup"
).split())
TRANSITION_FIXTURE_FIELDS = frozenset((
    "native_chr_fixture_enabled fixture_caps_readable fixture_capabilities_zero fixture_loopback_only fixture_context_readable "
    "fixture_context_valid fixture_fixed_target_valid fixture_ca_readable fixture_ca_pem fixture_ca_certificate fixture_host_pin_readable "
    "fixture_host_pin_exact fixture_host_key_valid fixture_password_readable fixture_api_verified fixture_stable_identity"
).split())
TRANSITION_FIELDS = TRANSITION_STEP_FIELDS | TRANSITION_BASELINE_FIELDS | TRANSITION_UPDATE_FIELDS | TRANSITION_CANCEL_FIELDS | TRANSITION_FAILURES | TRANSITION_FIXTURE_FIELDS | {"code"}
TRANSITION_BOOLEANS = {key for key, value in TRANSITION_REQUIRED.items() if value in ("true", "false")} | TRANSITION_FAILURES | TRANSITION_FIXTURE_FIELDS | {"transition_owner_preserved"}
TRANSITION_NUMBERS = {
    **{name: (0, 80) for name in ("transition_history_before", "transition_history_after", "transition_history_delta", "transition_maximum_history", "transition_history_total_delta")},
    **{name: (0, 300000) for name in ("transition_baseline_milliseconds", "transition_update_milliseconds", "transition_cancel_milliseconds")},
    "transition_operation": (1, 9), "transition_activate_operations": (0, 9), "transition_prepared_additions": (0, 6), "transition_forbidden_calls": (0, 1000000),
}

TESTS = frozenset(CASES.values())
DISCARD_REQUIRED = {
    **{name: "true" for name in (
        "discard_bootstrap_cancel_verified", "discard_prepared_checkpoint_equal", "discard_prepared_credentials_equal",
        "discard_prepared_retained", "discard_old_runtime_closed", "discard_fresh_runtime_verified",
        "discard_bootstrap_review_rejected", "discard_exact_removal_ids", "discard_complete",
        "discard_rollback_fact_preserved", "discard_original_baseline_restored", "discard_original_credentials_restored",
        "discard_fresh_review_empty", "discard_new_bootstrap_confirmed", "discard_new_bootstrap_strict",
        "discard_no_manual_cleanup",
    )},
    "discard_large_scale_verified": "false", "discard_packet_forwarding_verified": "false",
    "discard_native_forbidden_calls": "0",
}
LIVE_CASES = frozenset((*device_reset.CASES, large_cleanup.CASE, "batch1000", "discard1000", "discard1000-global-tail", "read-scale", "read-scale-from", "presence-id-scale", "selection-scale", "cleanup", "flag-query", "flag-query-presence", "discard", "discard-global-tail", "presence", "review-reuse", "transition-budget", "discard-bounded"))
LONG_CASES = frozenset(("batch1000", "discard1000", "discard1000-global-tail", "cleanup", "discard", "discard-global-tail", "review-reuse"))
LIVE_TESTS = frozenset(CASES[name] for name in LIVE_CASES)
TERMINAL_TESTS = frozenset((CASES["owned-delay"],))
OWNED_CASES = frozenset(("filter-dollar", "instance-set", "connection-set", "authentication-set", "authentication-empty", "limit-unset", "connection-enable", "connection-disable"))
FLAG_PRESENCE_REQUIRED = {"flag_presence_variant": "true", "flag_presence_gates_unchanged": "true", "flag_presence_observer_shortcut_verified": "false", "flag_query_cleanup_after_proof": "true"}
FLAG_PRESENCE_GETTERS = frozenset(("flag_presence_getter_completed", "flag_presence_getter_true", "flag_presence_getter_false", "flag_presence_getter_unknown"))
FLAG_PRESENCE_QUERY_COUNTS = frozenset(("flag_presence_expected_count", "flag_presence_query_count", "flag_presence_matched_count", "flag_presence_unexpected_count", "flag_presence_missing_count"))
FLAG_PRESENCE_QUERY_FIELDS = FLAG_PRESENCE_QUERY_COUNTS | {"flag_presence_mode", "flag_presence_predicate_disabled", "flag_presence_query_completed", "api_code"}
FLAG_PRESENCE_FIELDS = (FLAG_PRESENCE_REQUIRED.keys() | FLAG_PRESENCE_GETTERS | FLAG_PRESENCE_QUERY_FIELDS | {
    "flag_presence_query_bounded", "flag_query_mode", "flag_query_open", "flag_query_reader", "flag_query_empty_baseline",
    "flag_query_fixture_add", "flag_query_getter_exact", "flag_query_predicate_completed", "flag_query_membership_exact",
    "flag_query_fixture_toggle", "flag_query_cleanup_after_proof", "code",
    "native_chr_fixture_enabled", "fixture_caps_readable", "fixture_capabilities_zero", "fixture_loopback_only",
    "fixture_context_readable", "fixture_context_valid", "fixture_fixed_target_valid", "fixture_ca_readable", "fixture_ca_pem",
    "fixture_ca_certificate", "fixture_host_pin_readable", "fixture_host_pin_exact", "fixture_host_key_valid",
    "fixture_password_readable", "fixture_api_verified", "fixture_stable_identity",
})
BOOLEANS = frozenset((
    "owned_ca_valid owned_api_open owned_exact_version owned_stable_identity owned_nonce_available owned_fixture_add "
    "owned_fixture_idle owned_fixture_empty_key owned_ssh_open owned_ssh_sync owned_same_device owned_take "
    "completed owner_current api_mutation_used exact owned_secret_readback owned_empty_readback owned_unset_readback "
    "owned_cancel_requested owned_checkpoint_restored owned_cleanup_after_proof manual_cleanup_before_proof "
    "extension_open extension_private_journal extension_supervisor_open extension_baseline extension_plan "
    "extension_cleanup_owner_clear extension_cleanup_exact_ack_id extension_cleanup_after_proof extension_batch_stage "
    "extension_batch_complete_and_bounded_reads extension_batch_cancel extension_batch_after "
    "extension_batch_checkpoint_and_credentials_equal checkpoint_equal credentials_equal cleanup_before_proof "
    "extension_cleanup_initial_stage extension_cleanup_initial_confirm extension_cleanup_authorized_suffix_empty "
    "extension_cleanup_update_stage extension_cleanup_update_confirm extension_cleanup_confirmed_fact "
    "extension_cleanup_completed extension_cleanup_desired_retained extension_cleanup_old_target_absent "
    "extension_cleanup_final_roundtrip extension_cleanup_exactly_once cleanup_complete confirmed_preserved "
    "strict_desired old_targets_absent new_review_requested extension_batch_runtime "
    "owned_order_proxy_listen owned_order_fixture_file owned_order_fixture_owner owned_order_fixture_read "
    "owned_order_fixture_changed owned_order_fixture_bound owned_order_private_scratch owned_order_private_scratch_owner "
    "owned_order_capabilities owned_order_zero_caps owned_order_loopback_only owned_order_fixed_fixture owned_order_ca "
    "owned_order_api_verified owned_order_version owned_order_identity owned_order_initial_idle owned_order_baseline_read "
    "owned_order_fixture_absent owned_order_fixture_create owned_order_public_pin_exact owned_order_proxy_pin "
    "owned_order_ssh_verified owned_order_ssh_sync owned_order_same_device owned_order_take owned_order_submission_and_deadline "
    "owned_order_client_stream_ended owned_order_ciphertext_held owned_order_before_release_still_owned "
    "baseline_observed rollback_claimed owned_order_data_before_eof owned_order_after_release_rollback "
    "owned_order_no_late_write owner_released checkpoint_restored late_write_observed api_activation_write "
    "owned_order_cleanup_after_proof deadline getter_context_done getter_context_deadline "
    "flag_query_open flag_query_reader flag_query_empty_baseline flag_query_fixture_add flag_query_getter_exact "
    "flag_query_predicate_completed flag_query_membership_exact flag_query_fixture_toggle flag_query_cleanup_after_proof "
    "extension_apply_completed evidence_reused object_proof_present"
).split())
BOOLEANS = BOOLEANS | (DISCARD_REQUIRED.keys() - {"discard_native_forbidden_calls"})
BOOLEANS = BOOLEANS | FLAG_PRESENCE_REQUIRED.keys() | {"flag_presence_query_bounded", "flag_presence_predicate_disabled", "flag_presence_query_completed"}
PRESENCE_REQUIRED = {
    **{name: "true" for name in (
        "presence_rule_primitive_verified", "presence_baseline_verified", "presence_object_exact",
        "presence_remove_acknowledged", "presence_absence_twice_verified", "presence_full_menu_absent",
        "presence_final_identity_owner_verified", "presence_native_gates_unchanged",
    )},
    **{name: "false" for name in ("presence_batch_gc_verified", "presence_publication_verified", "presence_rollback_verified")},
    "presence_add_attempts": "1", "presence_remove_attempts": "1", "presence_absent_queries": "2",
}
BOOLEANS = BOOLEANS | {key for key, value in PRESENCE_REQUIRED.items() if value in ("true", "false")} | {
    "presence_open", "presence_reader_available", "presence_nonce_available", "presence_fixture_add", "presence_owner_before_remove",
}
REVIEW_REUSE_REQUIRED = {
    **{"review_reuse_"+name: "true" for name in (
        "baseline_confirmed", "update_cancel_verified", "checkpoint_equal", "credentials_equal",
        "old_confirm_rejected", "old_runtime_closed", "fresh_session_verified", "review_readonly",
        "retained_generation_verified", "native_ids_preserved", "authentication_refs_preserved",
        "zero_prepare_rules", "new_transaction", "confirmed", "strict", "no_manual_cleanup",
    )},
    **{"review_reuse_"+name: "false" for name in ("batch_removal_used", "large_scale_verified", "packet_forwarding_verified")},
    "review_reuse_second_add_rules": "0", "review_reuse_review_forbidden_calls": "0",
}
DISCARD1000_REQUIRED = {
    **{"removal_scale_" + name: "true" for name in (
        "baseline_verified", "stage_verified", "cancel_verified", "checkpoint_verified",
        "prepared_credentials_verified", "fresh_verified", "early_review_rejected", "discard_verified",
        "rollback_fact_verified", "strict_baseline_verified", "final_credentials_verified", "review_verified",
        "exact_once_verified", "no_forbidden_verified", "gates_unchanged_verified", "selection_verified",
    )},
    **{"removal_scale_" + name: "false" for name in (
        "publication_verified", "cleanup_verified", "manual_cleanup",
    )},
}
DISCARD1000_FAILURE_FIELDS = frozenset((
    "removal_scale_counter_bounds removal_scale_first_open removal_scale_plan removal_scale_private_journal "
    "removal_scale_supervisor removal_scale_prepare_ids_verified removal_scale_old_closed removal_scale_fresh_supervisor"
).split())
DISCARD1000_PHASES = ("baseline", "stage", "cancel", "checkpoint", "fresh", "discard", "final")
DISCARD1000_TIMINGS = ("first-open", "journal-open", "fresh-open", "journal-replay", "rejected-review", "total")
DISCARD1000_TIMING_FIELDS = frozenset((
    "removal_scale_timing removal_scale_timing_milliseconds removal_scale_elapsed_milliseconds "
    "removal_scale_remaining_milliseconds removal_scale_context_done removal_scale_context_deadline"
).split())
DISCARD1000_TOTAL_FIELDS = frozenset(("removal_scale_accounted_milliseconds", "removal_scale_unaccounted_milliseconds"))
DISCARD1000_BATCH_FIELDS = frozenset((
    "removal_scale_batch_event removal_scale_batch_sequence removal_scale_batch_requested removal_scale_batch_completed "
    "removal_scale_batch_total_completed removal_scale_batch_milliseconds removal_scale_elapsed_milliseconds "
    "removal_scale_remaining_milliseconds removal_scale_batch_error"
).split())
DISCARD1000_GETTER_FIELDS = frozenset((
    "extension_getter_resource extension_getter_field api_code getter_count getter_milliseconds after_batch_milliseconds "
    "getter_context_done getter_context_deadline reply_bytes reply_records reply_sentences limit_kind deadline deadline_kind"
).split())
DISCARD1000_METRICS = frozenset((
    "removal_scale_inventory_calls removal_scale_object_reads removal_scale_identity_reads removal_scale_presence_reads "
    "removal_scale_property_items removal_scale_property_batches removal_scale_owner_reads removal_scale_credential_reads "
    "removal_scale_api_writes removal_scale_api_adds removal_scale_api_removes removal_scale_ssh_reads "
    "removal_scale_ssh_queries removal_scale_ssh_owned_writes removal_scale_ssh_takes removal_scale_ssh_releases "
    "removal_scale_disabled_getters removal_scale_false_queries removal_scale_true_queries "
    "removal_scale_false_ids_total removal_scale_true_ids_total removal_scale_selection_observations"
).split())
DISCARD1000_COUNTS = frozenset((
    "removal_scale_generation_rules removal_scale_prepare_rule_acks removal_scale_add_total removal_scale_add_unique_ids "
    "removal_scale_batches removal_scale_batch_rules removal_scale_single_removals removal_scale_single_rule_removals "
    "removal_scale_remove_attempts removal_scale_remove_acks removal_scale_remove_unique_ids removal_scale_forbidden_calls"
).split())
DISCARD1000_GLOBAL_COUNTS = frozenset((
    "removal_scale_batch_rule_acks removal_scale_absent_callbacks removal_scale_full_tail_reads removal_scale_pending_guards "
    "removal_scale_formal_completed removal_scale_formal_total"
).split())
DISCARD1000_GLOBAL_BATCH_FIELDS = DISCARD1000_BATCH_FIELDS | {
    "removal_scale_batch_acknowledged", "removal_scale_batch_total_acknowledged",
    "removal_scale_batch_verified_tail", "removal_scale_batch_pending",
    "removal_scale_lifecycle_remaining_milliseconds", "removal_scale_discard_budget_remaining_milliseconds",
}
DISCARD1000_GLOBAL_BUDGET_FIELDS = frozenset((
    "removal_scale_budget_event removal_scale_discard_budget_milliseconds removal_scale_discard_elapsed_milliseconds "
    "removal_scale_discard_budget_remaining_milliseconds removal_scale_lifecycle_remaining_milliseconds"
).split())
DISCARD1000_GLOBAL_PHASE_BUDGET_FIELDS = frozenset((
    "removal_scale_phase_budget removal_scale_phase_remaining_milliseconds removal_scale_phase_context_done removal_scale_phase_context_deadline"
).split())
DISCARD1000_GLOBAL_REQUIRED = {
    **DISCARD1000_REQUIRED, "removal_scale_proof_mode": "global-tail", "removal_scale_global_tail_verified": "true",
    "removal_scale_absent_callbacks": "0", "removal_scale_formal_completed": "2009", "removal_scale_formal_total": "2009",
}
READ_SCALE_PHASES = ("setup", "scan-one", "scan-two", "observe", "exact-rules", "protected")
READ_SCALE_FROM_PHASES = READ_SCALE_PHASES + ("absent",)
READ_SCALE_REQUIRED = {
    **{"read_scale_"+field: "true" for field in (
        "setup_only", "setup_readback", "protected_inert", "inventory_consistent", "complete_observation", "exact_rules",
        "protected_exact", "profile_unchanged", "no_remove", "no_owner_mutations",
    )},
    **{"read_scale_"+field: "false" for field in ("strict_authorization", "publication_verified", "rollback_verified")},
    "read_scale_generation_rules": "2004", "read_scale_setup_adds": "2008", "read_scale_unique_ids": "2008",
    "read_scale_protected_objects": "4", "read_scale_forbidden_calls": "0",
}
READ_SCALE_FROM_REQUIRED = {
    **READ_SCALE_REQUIRED,
    "read_scale_from_variant": "true", "read_scale_missing_id_excluded": "true",
    "read_scale_absence_verified": "true", "read_scale_absent_queries": "2",
}
READ_SCALE_NUMBERS = frozenset((
    "read_scale_inventory_calls read_scale_property_items read_scale_property_batches read_scale_presence_reads "
    "read_scale_owner_reads read_scale_credential_reads read_scale_api_writes"
).split())
READ_SCALE_TIMES = frozenset((
    "read_scale_milliseconds read_scale_inventory_milliseconds read_scale_getter_milliseconds "
    "read_scale_presence_milliseconds read_scale_write_milliseconds"
).split())
READ_SCALE_HELPER_FIELDS = frozenset((
    "native_chr_fixture_enabled fixture_caps_readable fixture_capabilities_zero fixture_loopback_only "
    "fixture_context_readable fixture_context_valid fixture_fixed_target_valid fixture_ca_readable fixture_ca_pem "
    "fixture_ca_certificate fixture_host_pin_readable fixture_host_pin_exact fixture_host_key_valid "
    "fixture_password_readable fixture_api_verified fixture_stable_identity "
    "extension_api_resource extension_getter_resource extension_getter_field api_code reply_bytes reply_records reply_sentences "
    "limit_kind deadline deadline_kind elapsed_milliseconds extension_stage getter_count getter_milliseconds "
    "after_batch_milliseconds getter_context_done getter_context_deadline code"
).split())
READ_SCALE_FAILURE_FIELDS = frozenset((
    "read_scale_open read_scale_initial_empty read_scale_plan read_scale_setup read_scale_exact_object read_scale_final_guards"
).split())
READ_SCALE_FIELDS = (READ_SCALE_REQUIRED.keys() | READ_SCALE_NUMBERS | READ_SCALE_TIMES | READ_SCALE_HELPER_FIELDS |
                    READ_SCALE_FAILURE_FIELDS | {"read_scale_phase", "read_scale_total_milliseconds", "read_scale_api_resource", "read_scale_context_done", "read_scale_context_deadline"})
READ_SCALE_FROM_FIELDS = READ_SCALE_FIELDS | READ_SCALE_FROM_REQUIRED.keys()
SELECTION_SCALE_PHASES = ("setup", "initial-getter", "initial-selection", "initial-observe", "toggle", "toggled-getter", "toggled-selection", "toggled-observe")
SELECTION_SCALE_REQUIRED = {
    "selection_scale_variant": "true", "selection_scale_generation_rules": "2004", "selection_scale_setup_adds": "2008",
    "selection_scale_toggle_writes": "2", "selection_scale_unique_ids": "2008", "selection_scale_forbidden_calls": "0",
    **{"selection_scale_"+name: "true" for name in ("toggle_verified", "gates_unchanged", "source_only", "no_connections", "no_active_roots")},
    "selection_scale_publication_verified": "false", "selection_scale_rollback_verified": "false",
}
SELECTION_SCALE_PROOFS = frozenset((
    "selection_scale_inventory_equal selection_scale_digest_equal selection_scale_observe_equal selection_scale_partition_exact selection_scale_explicit_flags selection_scale_body_verified"
).split())
SELECTION_SCALE_COUNTS = frozenset((
    "selection_scale_inventory_calls selection_scale_property_items selection_scale_property_batches selection_scale_disabled_getters "
    "selection_scale_predicate_calls selection_scale_false_queries selection_scale_true_queries selection_scale_false_ids_total "
    "selection_scale_true_ids_total selection_scale_api_writes selection_scale_credential_reads"
).split())
SELECTION_SCALE_TIMES = frozenset((
    "selection_scale_milliseconds selection_scale_inventory_milliseconds selection_scale_getter_milliseconds selection_scale_predicate_milliseconds selection_scale_write_milliseconds"
).split())
SELECTION_SCALE_FAILURES = frozenset((
    "selection_scale_open selection_scale_initial_empty selection_scale_plan selection_scale_setup selection_scale_getter_read selection_scale_scope selection_scale_final_guards"
).split())
SELECTION_SCALE_FIELDS = (SELECTION_SCALE_REQUIRED.keys() | SELECTION_SCALE_PROOFS | SELECTION_SCALE_COUNTS | SELECTION_SCALE_TIMES |
                          SELECTION_SCALE_FAILURES | READ_SCALE_HELPER_FIELDS |
                          {"selection_scale_phase", "selection_scale_mode", "selection_scale_total_milliseconds", "read_scale_api_resource", "read_scale_context_done", "read_scale_context_deadline"})
PRESENCE_ID_PHASES = ("setup", "inventory", "existing", "absent")
PRESENCE_ID_REQUIRED = {
    **{"presence_id_"+field: "true" for field in (
        "variant", "complete_inventory", "compiled_rules_verified", "missing_excluded", "existing_verified",
        "absence_verified", "boolean_only", "no_owner_mutations", "no_remove", "gates_unchanged",
    )},
    **{"presence_id_"+field: "false" for field in ("attributes_verified", "publication_verified", "rollback_verified")},
    "presence_id_generation_rules": "2004", "presence_id_setup_adds": "2008", "presence_id_unique_ids": "2008",
    "presence_id_existing_queries": "3", "presence_id_absent_queries": "2", "presence_id_forbidden_calls": "0",
}
PRESENCE_ID_FAILURE_FIELDS = frozenset((
    "presence_id_open presence_id_initial_empty presence_id_plan presence_id_reader presence_id_setup presence_id_final_guards"
).split())
PRESENCE_ID_FIELDS = (PRESENCE_ID_REQUIRED.keys() | PRESENCE_ID_FAILURE_FIELDS | READ_SCALE_NUMBERS | READ_SCALE_TIMES |
                      READ_SCALE_HELPER_FIELDS | {"read_scale_phase", "read_scale_total_milliseconds", "read_scale_api_resource", "read_scale_context_done", "read_scale_context_deadline"})
# Native test helpers used by this exact selector. These are fixed labels from
# nativeLiveConfig and nativeExtensionAPI/Runtime, not a name-prefix grant.
DISCARD1000_HELPER_FIELDS = frozenset((
    "native_chr_fixture_enabled fixture_caps_readable fixture_capabilities_zero fixture_loopback_only "
    "fixture_context_readable fixture_context_valid fixture_fixed_target_valid fixture_ca_readable fixture_ca_pem "
    "fixture_ca_certificate fixture_host_pin_readable fixture_host_pin_exact fixture_host_key_valid "
    "fixture_password_readable fixture_api_verified fixture_stable_identity "
    "extension_api_resource api_code reply_bytes reply_records reply_sentences limit_kind deadline deadline_kind "
    "elapsed_milliseconds extension_stage extension_getter_resource extension_getter_field getter_count "
    "getter_milliseconds after_batch_milliseconds getter_context_done getter_context_deadline "
    "extension_observation_code object_proof_present object_proof_age_milliseconds object_apply_milliseconds "
    "extension_observe_milliseconds extension_inventory_milliseconds extension_getter_milliseconds "
    "extension_other_milliseconds extension_inventory_calls extension_getter_items extension_getter_batches "
    "extension_apply_inventory_calls extension_apply_milliseconds extension_apply_completed evidence_reused "
    "extension_batch_runtime batch_acknowledged code"
).split())
DISCARD1000_FIELDS = (DISCARD1000_REQUIRED.keys() | DISCARD1000_FAILURE_FIELDS | DISCARD1000_METRICS |
                      DISCARD1000_COUNTS | DISCARD1000_HELPER_FIELDS | DISCARD1000_TIMING_FIELDS |
                      DISCARD1000_TOTAL_FIELDS | DISCARD1000_BATCH_FIELDS |
                      {"removal_scale_phase", "removal_scale_milliseconds", "removal_scale_adapter_code"})
DISCARD1000_GLOBAL_FIELDS = (DISCARD1000_FIELDS | DISCARD1000_GLOBAL_REQUIRED.keys() |
                            DISCARD1000_GLOBAL_COUNTS | DISCARD1000_GLOBAL_BATCH_FIELDS | DISCARD1000_GLOBAL_BUDGET_FIELDS |
                            DISCARD1000_GLOBAL_PHASE_BUDGET_FIELDS | {"removal_scale_proof_verified"})
BOOLEANS = BOOLEANS | DISCARD1000_REQUIRED.keys() | DISCARD1000_FAILURE_FIELDS
BOOLEANS = BOOLEANS | {"removal_scale_context_done", "removal_scale_context_deadline", "removal_scale_batch_error"}
BOOLEANS = BOOLEANS | {"removal_scale_global_tail_verified", "removal_scale_proof_verified", "removal_scale_batch_verified_tail", "removal_scale_batch_pending"}
BOOLEANS = BOOLEANS | {"removal_scale_phase_context_done", "removal_scale_phase_context_deadline"}
BOOLEANS = BOOLEANS | {key for key, value in READ_SCALE_REQUIRED.items() if value in ("true", "false")} | READ_SCALE_FAILURE_FIELDS | {"read_scale_context_done", "read_scale_context_deadline"}
BOOLEANS = BOOLEANS | {key for key, value in READ_SCALE_FROM_REQUIRED.items() if value in ("true", "false")}
BOOLEANS = BOOLEANS | {key for key, value in PRESENCE_ID_REQUIRED.items() if value in ("true", "false")} | PRESENCE_ID_FAILURE_FIELDS
BOOLEANS = BOOLEANS | {key for key, value in SELECTION_SCALE_REQUIRED.items() if value in ("true", "false")} | SELECTION_SCALE_PROOFS | SELECTION_SCALE_FAILURES
BOOLEANS = BOOLEANS | {key for key, value in REVIEW_REUSE_REQUIRED.items() if value in ("true", "false")}
EXTRA_CODES = frozenset((
    "another-write-unresolved cleanup-binding-not-permitted cleanup-completed-object-reappeared cleanup-connection-baseline-missing "
    "cleanup-connection-not-retired cleanup-does-not-match-plan cleanup-not-authorized cleanup-not-bound cleanup-operation-order "
    "cleanup-owner-not-clear cleanup-owner-not-idle cleanup-owner-reader-unavailable cleanup-plan-invalid cleanup-protected-configuration-mismatch "
    "cleanup-protected-connection-mismatch cleanup-protected-graph-unverified cleanup-recovery-state-unverified cleanup-reference-unverified "
    "cleanup-remaining-object-missing cleanup-removal-not-proven cleanup-retired-object-changed cleanup-rule-identity-changed cleanup-suffix-invalid "
    "cleanup-without-confirmed-receipt cleanup-would-replace-unconfirmed-plan cleanup-write-outcome-unknown journal-cleanup-complete-invalid "
    "journal-cleanup-intent-invalid journal-cleanup-readback-digest journal-cleanup-readback-invalid journal-cleanup-result-invalid "
    "journal-cleanup-scope-invalid journal-cleanup-start-invalid journal-cleanup-status-invalid journal-rule-batch-digest-invalid "
    "journal-rule-batch-intent-invalid journal-rule-batch-invalid journal-rule-batch-readback-invalid journal-rule-batch-result-invalid "
    "native-rule-batch-profile-unverified rule-batch-acked-object-missing rule-batch-baseline-changed rule-batch-channel-ended "
    "rule-batch-does-not-match-plan rule-batch-duplicate-marker rule-batch-exact-difference-unverified "
    "rule-batch-generation-already-present-or-referenced rule-batch-id-mismatch rule-batch-id-reused rule-batch-intent-not-durable "
    "rule-batch-interrupted rule-batch-invalid rule-batch-not-bound rule-batch-not-complete-generation rule-batch-not-inert "
    "rule-batch-operation-mismatch rule-batch-order-mismatch rule-batch-outcome-unknown rule-batch-owner-changed "
    "rule-batch-owner-not-idle rule-batch-owner-reader-unavailable rule-batch-readback-unavailable rule-batch-receipt-invalid "
    "rule-batch-recorder-stopped rule-batch-reference-unverified rule-batch-result-not-durable rule-batch-result-order-mismatch "
    "rule-batch-state-changed rule-batch-write-failed owned-mutation-channel-unavailable owned-mutation-outcome-unknown "
    "required-property-getter-batch-failed native-extension-property-batch-unavailable "
    "activation-global-checkpoint-mismatch object-action-not-permitted object-activation-binding-failed object-activation-not-permitted "
    "object-activation-owner-unverified object-after-not-pending object-attributes-mismatch object-changed-before-write "
    "object-changed-during-observation object-credential-authority-unexpected object-credential-authority-unverified "
    "object-credential-read-unverified object-credential-readback-mismatch object-credential-unverified object-global-checkpoint-expired "
    "object-global-checkpoint-unavailable object-global-checkpoint-unverified object-global-resolver-unverified "
    "object-identity-change-not-permitted object-identity-read-failed object-mutation-invalid object-observation-binding-invalid "
    "object-observation-failed object-observation-owner-unverified object-operation-not-activation object-operation-not-owned "
    "object-operation-not-permitted object-operation-order object-operation-reference-unverified object-operation-resolution-failed "
    "object-operation-target-missing object-owner-unverified object-properties-unverified object-property-read-bound "
    "object-property-read-failed object-read-not-permitted object-reader-unavailable object-record-read-failed object-resource-not-permitted "
    "object-session-unavailable object-state-change-not-permitted object-unset-not-permitted object-write-not-permitted "
    "object-write-owner-unverified object-write-proof-unavailable object-write-readback-mismatch object-write-readback-unavailable "
    "object-write-returned-unexpected-id unverified-object-observation object-write-owner-query-failed "
    "object-write-history-unverified object-write-history-exhausted object-write-session-invalidated object-write-proof-expired"
).split())
# Fixed discard failure vocabulary; never accept an arbitrary code prefix.
EXTRA_CODES = EXTRA_CODES | frozenset((
    "discard-awaiting-explicit-continuation discard-baseline-manifest-invalid discard-baseline-not-restored "
    "discard-checkpoint-not-exact-prepare-additions discard-checkpoint-unavailable discard-complete "
    "discard-context-ended discard-current-state-changed discard-final-credential-unverified "
    "discard-final-state-unverified discard-final-strict-unverified discard-intent "
    "discard-journal-capacity-insufficient discard-journal-unavailable discard-manifest "
    "discard-manifest-invalid discard-native-gate-unverified discard-not-authorized "
    "discard-object discard-original-snapshot-mismatch discard-outcome-unresolved "
    "discard-plan-invalid discard-prepare-provenance-unverified discard-prepared-checkpoint-changed "
    "discard-prepared-objects-invalid discard-readback discard-readback-unavailable "
    "discard-recovery-state-unverified discard-reference-unverified discard-removal-not-proven "
    "discard-result discard-runtime-binding-failed discard-runtime-reopen-required "
    "discard-start discard-state-changed-before-write discard-status "
    "discard-without-verified-rollback journal-discard-complete-invalid journal-discard-event-invalid "
    "journal-discard-intent-invalid journal-discard-intent-scope journal-discard-readback-digest "
    "journal-discard-readback-invalid journal-discard-replay-failed journal-discard-result-invalid "
    "journal-discard-scope-invalid journal-discard-start-invalid journal-discard-status-invalid "
    "journal-discard-status-scope journal-discard-target-changed native-discard-forbidden-call "
    "native-discard-owner-reader-unavailable native-discard-property-batch-unavailable native-discard-unclassified "
    "prepared-discard-baseline-graph-unverified prepared-discard-binding-invalid prepared-discard-binding-not-permitted "
    "prepared-discard-completed-object-reappeared prepared-discard-does-not-match-binding prepared-discard-foreign-reference "
    "prepared-discard-not-bound prepared-discard-object-changed prepared-discard-operation-order "
    "prepared-discard-owner-not-idle prepared-discard-owner-reader-unavailable prepared-discard-owner-unverified "
    "prepared-discard-plan-invalid prepared-discard-protected-baseline-changed prepared-discard-protected-chain-overlap "
    "prepared-discard-rebind-not-permitted prepared-discard-reference-unverified prepared-discard-remaining-object-missing "
    "prepared-discard-suffix-invalid prepared-discard-v1 prepared-discard-write-outcome-unknown "
    "review-fresh-runtime-required review-rollback-baseline-unavailable review-rollback-identity-changed "
).split())

# Closed stage labels from native_discard_test.go.
DISCARD_STAGE_FIELDS = frozenset((
    "discard_bootstrap_cancel_verified discard_bootstrap_review_rejected discard_bootstrap_stage discard_cancel_observation "
    "discard_complete discard_exact_removal_ids discard_final_baseline discard_first_open "
    "discard_first_supervisor discard_fresh_open discard_fresh_review_empty discard_fresh_runtime_verified "
    "discard_fresh_supervisor discard_initial_baseline discard_initial_digest discard_initial_plan "
    "discard_large_scale_verified discard_new_bootstrap_confirmed discard_new_bootstrap_strict discard_next_open "
    "discard_next_plan discard_next_roundtrip discard_next_stage discard_next_supervisor "
    "discard_no_manual_cleanup discard_old_runtime_closed discard_original_baseline_restored discard_original_credentials_restored "
    "discard_packet_forwarding_verified discard_prepared_checkpoint_equal discard_prepared_credentials_equal discard_prepared_retained "
    "discard_private_journal discard_rollback_fact_preserved discard_service_closed "
).split())
BOOLEANS = BOOLEANS | DISCARD_STAGE_FIELDS
GLOBAL_TAIL_REQUIRED = {
    **DISCARD_REQUIRED,
    "global_tail_complete": "true", "global_tail_gate_unchanged": "true",
    "global_tail_large_scale_verified": "false", "global_tail_rule_acks": "6", "global_tail_absent_callbacks": "0",
}
GLOBAL_TAIL_COUNTS = frozenset((
    "global_tail_batches global_tail_rule_acks global_tail_verified_full_reads global_tail_pending_guards global_tail_absent_callbacks"
).split())
GLOBAL_TAIL_FIELDS = (DISCARD_STAGE_FIELDS | GLOBAL_TAIL_REQUIRED.keys() | GLOBAL_TAIL_COUNTS | {
    "discard_original_additions", "discard_remove_attempts", "discard_remove_acknowledged", "discard_adapter_code", "code",
    "native_chr_fixture_enabled", "fixture_caps_readable", "fixture_capabilities_zero", "fixture_loopback_only",
    "fixture_context_readable", "fixture_context_valid", "fixture_fixed_target_valid", "fixture_ca_readable", "fixture_ca_pem",
    "fixture_ca_certificate", "fixture_host_pin_readable", "fixture_host_pin_exact", "fixture_host_key_valid",
    "fixture_password_readable", "fixture_api_verified", "fixture_stable_identity",
})
BOUNDED_DISCARD_REQUIRED = {
    "bounded_discard_fresh_recovery": "true", "bounded_discard_recovery_completed": "7", "bounded_discard_recovery_mutations": "0",
    "bounded_discard_partial_review_rejected": "true", "bounded_discard_automatic_continuation": "false",
    "bounded_discard_requests": "5", "bounded_discard_final_verified": "true", "bounded_discard_exact_once": "true", "bounded_discard_large_scale_verified": "false",
}
BOUNDED_DISCARD_FAILURES = frozenset(("bounded_discard_first_boundary bounded_discard_old_closed bounded_discard_fresh_open bounded_discard_fresh_supervisor bounded_discard_recovery_readonly bounded_discard_continuation_verified bounded_discard_partial_review_rejected").split())
BOUNDED_DISCARD_STEP = frozenset(("bounded_discard_request bounded_discard_limit bounded_discard_completed bounded_discard_total bounded_discard_pending bounded_discard_complete bounded_discard_return_verified").split())
BOUNDED_DISCARD_RECOVERY = frozenset(("bounded_discard_fresh_recovery bounded_discard_recovery_completed bounded_discard_recovery_mutations bounded_discard_partial_review_rejected bounded_discard_automatic_continuation").split())
BOUNDED_DISCARD_FINAL = frozenset(("bounded_discard_requests bounded_discard_final_verified bounded_discard_exact_once bounded_discard_large_scale_verified").split())
BOUNDED_DISCARD_ONLY = BOUNDED_DISCARD_STEP | BOUNDED_DISCARD_RECOVERY | BOUNDED_DISCARD_FINAL | BOUNDED_DISCARD_FAILURES
BOUNDED_DISCARD_FIELDS = GLOBAL_TAIL_FIELDS | BOUNDED_DISCARD_ONLY
BOOLEANS = BOOLEANS | {key for key, value in BOUNDED_DISCARD_REQUIRED.items() if value in ("true", "false")} | BOUNDED_DISCARD_FAILURES | {"bounded_discard_pending", "bounded_discard_complete", "bounded_discard_return_verified"}
EXTRA_CODES = EXTRA_CODES | {"discard-invalid-batch-limit", "discard-boundary-unverified"}

BOOLEANS = BOOLEANS | {key for key, value in GLOBAL_TAIL_REQUIRED.items() if value in ("true", "false")}
EXTRA_CODES = EXTRA_CODES | frozenset(("native-discard-checkpoint-unverified",))
EXTRA_CODES = EXTRA_CODES | frozenset((
    "native-global-tail-object-reader-unavailable native-global-tail-presence-reader-unavailable "
    "rule-removal-proof-mode-invalid rule-removal-absence-not-permitted rule-removal-acknowledged-target-present "
    "rule-removal-tail-invalid rule-removal-tail-unverified"
).split())

# Fixed stage labels for the managed rollback-review/reuse fixture.
BOOLEANS = BOOLEANS | frozenset((
    "review_reuse_authentication_refs_preserved review_reuse_baseline_confirmed review_reuse_baseline_stage review_reuse_baseline_strict "
    "review_reuse_batch_removal_used review_reuse_checkpoint_equal review_reuse_confirmed review_reuse_credentials_equal "
    "review_reuse_empty_baseline review_reuse_first_open review_reuse_first_plan review_reuse_first_supervisor "
    "review_reuse_fresh_open review_reuse_fresh_session_verified review_reuse_fresh_supervisor review_reuse_journal "
    "review_reuse_large_scale_verified review_reuse_native_ids_preserved review_reuse_new_transaction review_reuse_no_manual_cleanup "
    "review_reuse_old_confirm_rejected review_reuse_old_runtime_closed review_reuse_packet_forwarding_verified review_reuse_retained_generation_verified "
    "review_reuse_retry_plan review_reuse_retry_stage review_reuse_review_readonly review_reuse_strict "
    "review_reuse_strict_review review_reuse_update_cancel_verified review_reuse_update_plan review_reuse_update_stage "
    "review_reuse_zero_prepare_rules "
).split())
EXTRA_CODES = EXTRA_CODES | frozenset((
    "native-review-reuse-checkpoint-unverified native-review-reuse-forbidden-call native-review-reuse-owner-reader-unavailable "
    "native-review-reuse-property-batch-unavailable native-review-reuse-unclassified "
).split())

# Actual failure sites in the batch-removal adapter/controller and this fixture.
# HMAC domain strings and journal event names are not diagnostic codes.
EXTRA_CODES = EXTRA_CODES | frozenset((
    "native-removal-owned-writer-unavailable native-removal-presence-reader-unavailable "
    "rule-removal-absence-invalid rule-removal-absence-not-durable rule-removal-absence-not-empty rule-removal-absence-order "
    "rule-removal-batch-not-permitted rule-removal-batch-unavailable rule-removal-cleanup-not-authorized "
    "rule-removal-cleanup-not-bound rule-removal-context-ended rule-removal-discard-not-authorized rule-removal-discard-not-bound "
    "rule-removal-failed rule-removal-generation-invalid rule-removal-getter-bound rule-removal-getter-failed "
    "rule-removal-global-baseline-changed rule-removal-global-delta-unverified rule-removal-global-readback-unavailable "
    "rule-removal-global-state-changed rule-removal-intent-not-durable rule-removal-mode-invalid rule-removal-no-progress "
    "rule-removal-object-changed rule-removal-object-identity rule-removal-object-invalid rule-removal-object-unstable "
    "rule-removal-object-unverified rule-removal-observation-unverified rule-removal-observed-target-reappeared "
    "rule-removal-operation-invalid rule-removal-operation-order rule-removal-outcome-unknown rule-removal-outcome-unresolved "
    "rule-removal-owner-not-idle rule-removal-owner-reader-unavailable rule-removal-owner-unverified "
    "rule-removal-presence-reader-unavailable rule-removal-presence-unverified rule-removal-properties-incomplete "
    "rule-removal-properties-unverified rule-removal-protected-object-changed rule-removal-protected-object-missing "
    "rule-removal-protection-incomplete rule-removal-protection-invalid rule-removal-protection-unverified "
    "rule-removal-recorder-closed rule-removal-recovery-required rule-removal-recovery-unavailable "
    "rule-removal-reference-unverified rule-removal-result-not-durable rule-removal-result-order "
    "rule-removal-runtime-incomplete rule-removal-runtime-reopen-required rule-removal-scope-changed rule-removal-session-lost "
    "rule-removal-suffix-invalid rule-removal-unattempted-change rule-removal-unbound rule-removal-unresolved-source "
    "rule-removal-write-outcome-unknown journal-rule-removal-unbound journal-rule-removal-start-invalid "
    "journal-rule-removal-scope-invalid journal-rule-removal-prefix-invalid journal-rule-removal-cursor-changed "
    "journal-rule-removal-intent-invalid journal-rule-removal-target-invalid journal-rule-removal-result-invalid "
    "journal-rule-removal-absence-invalid journal-rule-removal-readback-prefix journal-rule-removal-readback-invalid "
    "journal-rule-removal-readback-digest journal-rule-removal-pending-invalid journal-rule-removal-event-invalid "
    "native-extension-owner-reader-unavailable extension-prepared-owner-unverified extension-prepared-credential-count "
    "extension-prepared-key-unverified"
).split())
EXTRA_CODES = EXTRA_CODES | {"native-read-scale-presence-unavailable", "native-read-scale-object-mismatch", "native-presence-id-result-mismatch"}
EXTRA_CODES = EXTRA_CODES | {"native-removal-global-tail-guard-unavailable", "native-removal-global-tail-observation-unverified", "native-removal-selection-path-unverified", "native-extension-selection-reader-unavailable"}
EXTRA_CODES = EXTRA_CODES | {"native-selection-reader-unavailable", "filter-rule-disabled-selection-failed", "filter-rule-disabled-selection-bound"}

NUMBERS = {
    "generation_rules": (2000, 60016), "acknowledged_rules": (0, 60016), "batches": (0, 1),
    "stage_inventory_calls": (1, 100000), "stage_milliseconds": (1, 540000),
    "approved_removals": (1, 60016), "acknowledged_removals": (0, 60016),
    "batch_acknowledged": (0, 60016),
    "reply_bytes": (0, (16<<20)+16), "reply_records": (0, 20000), "reply_sentences": (0, 40065),
    "elapsed_milliseconds": (0, 900000), "batch_milliseconds": (0, 900000),
    "getter_count": (0, 1000000), "getter_milliseconds": (0, 900000), "after_batch_milliseconds": (0, 900000),
    "extension_observe_milliseconds": (0, 900000), "extension_inventory_milliseconds": (0, 900000),
    "extension_getter_milliseconds": (0, 900000), "extension_other_milliseconds": (0, 900000),
    "extension_inventory_calls": (0, 20000), "extension_getter_items": (0, 100000), "extension_getter_batches": (0, 20000),
    "extension_detail_records": (0, 20000), "extension_detail_present": (0, 20000), "extension_detail_booleans": (0, 20000), "extension_detail_milliseconds": (0, 900000),
    "extension_apply_inventory_calls": (0, 20000), "extension_apply_milliseconds": (0, 900000),
    "stage_object_reads": (0, 100000), "stage_identity_reads": (0, 100000),
    "object_proof_age_milliseconds": (0, 900000), "object_apply_milliseconds": (0, 900000),
    "discard_original_additions": (1, 64), "discard_remove_attempts": (1, 64), "discard_remove_acknowledged": (1, 64),
}
NUMBERS.update({field: (0, 1000000) for field in DISCARD1000_METRICS | DISCARD1000_COUNTS})
NUMBERS["removal_scale_milliseconds"] = (0, 540000)
NUMBERS.update({field: (0, 600000) for field in (
    "removal_scale_timing_milliseconds", "removal_scale_elapsed_milliseconds", "removal_scale_batch_milliseconds",
    "removal_scale_accounted_milliseconds", "removal_scale_unaccounted_milliseconds",
)})
NUMBERS.update({
    "removal_scale_remaining_milliseconds": (0, 540000),
    "removal_scale_batch_sequence": (1, 2004), "removal_scale_batch_requested": (2, 256),
    "removal_scale_batch_completed": (0, 256), "removal_scale_batch_total_completed": (0, 2004),
})
NUMBERS.update({field: (0, 1000000) for field in READ_SCALE_NUMBERS})
NUMBERS.update({field: (0, 300000) for field in READ_SCALE_TIMES | {"read_scale_total_milliseconds"}})
NUMBERS.update({field: (0, 6) for field in GLOBAL_TAIL_COUNTS})
NUMBERS.update({"bounded_discard_request": (1,5), "bounded_discard_limit": (1,16), "bounded_discard_completed": (0,11), "bounded_discard_total": (11,11), "bounded_discard_recovery_completed": (0,11), "bounded_discard_recovery_mutations": (0,1000000), "bounded_discard_requests": (0,5)})
NUMBERS.update({field: (0, 2009) for field in DISCARD1000_GLOBAL_COUNTS})
NUMBERS["removal_scale_batch_acknowledged"] = (0, 256)
NUMBERS["removal_scale_batch_total_acknowledged"] = (0, 2004)
NUMBERS["removal_scale_discard_budget_milliseconds"] = (420000, 420000)
NUMBERS["removal_scale_discard_elapsed_milliseconds"] = (0, 600000)
NUMBERS["removal_scale_discard_budget_remaining_milliseconds"] = (0, 420000)
NUMBERS["removal_scale_lifecycle_remaining_milliseconds"] = (0, 540000)
NUMBERS["removal_scale_phase_remaining_milliseconds"] = (0, 540000)
NUMBERS.update({field: (0, 3) for field in FLAG_PRESENCE_GETTERS | FLAG_PRESENCE_QUERY_COUNTS})
NUMBERS.update({field: (0, 1000000) for field in SELECTION_SCALE_COUNTS})
NUMBERS.update({field: (0, 300000) for field in SELECTION_SCALE_TIMES | {"selection_scale_total_milliseconds"}})


def valid_field(field, value):
    if field in SELECTION_SCALE_REQUIRED and SELECTION_SCALE_REQUIRED[field] not in ("true", "false"):
        return value == SELECTION_SCALE_REQUIRED[field]
    if field == "selection_scale_phase":
        return value in SELECTION_SCALE_PHASES
    if field == "selection_scale_mode":
        return value in ("initial", "toggled")
    if field == "flag_presence_mode":
        return value in ("initial", "toggled")
    if field == "removal_scale_phase_budget":
        return value in DISCARD1000_PHASES
    if field == "removal_scale_budget_event":
        return value in ("start", "end")
    if field == "removal_scale_proof_mode":
        return value == "global-tail"
    if field in PRESENCE_ID_REQUIRED and PRESENCE_ID_REQUIRED[field] not in ("true", "false"):
        return value == PRESENCE_ID_REQUIRED[field]
    if field == "read_scale_absent_queries":
        return value == "2"
    if field in READ_SCALE_REQUIRED and READ_SCALE_REQUIRED[field] not in ("true", "false"):
        return value == READ_SCALE_REQUIRED[field]
    if field == "read_scale_phase":
        return value in READ_SCALE_FROM_PHASES or value in PRESENCE_ID_PHASES
    if field == "read_scale_api_resource":
        return value in live_schema.RESOURCES
    if field == "removal_scale_timing":
        return value in DISCARD1000_TIMINGS
    if field == "removal_scale_batch_event":
        return value in ("enter", "exit", "tail")
    if field == "removal_scale_phase":
        return value in DISCARD1000_PHASES
    if field == "removal_scale_adapter_code":
        return value in EXTRA_CODES or live_schema.valid_code(value)
    if field in ("review_reuse_second_add_rules", "review_reuse_review_forbidden_calls"):
        return value == "0"
    if field in ("presence_add_attempts", "presence_remove_attempts", "presence_absent_queries"):
        return value == PRESENCE_REQUIRED[field]
    if field == "discard_native_forbidden_calls":
        return value == "0"
    if field == "discard_adapter_code":
        return value in EXTRA_CODES or live_schema.valid_code(value)
    if field == "flag_query_mode":
        return value in ("initial", "toggled")
    if field == "extension_detail_field":
        return value in ("disabled", "inactive")
    if field in ("extension_api_resource", "extension_getter_resource"):
        return value in ("none", "unknown", "system-resource", "system-license", "routerboard", "identity", "routing-table", "bgp-instance", "bgp-connection", "filter-rule", "safe-mode")
    if field == "extension_getter_field":
        return value == "unknown" or value in live_schema.FIELDS and value != "tcp-md5-key"
    if field == "limit_kind":
        return value in ("none", "reply-bytes", "sentences", "word-bytes", "sentence-bytes", "words", "records")
    if field == "extension_stage":
        return value in ("baseline", "batch", "after-batch")
    if field == "deadline_kind":
        return value in ("none", "context", "command")
    if field in ("code", "extension_observation_code") and (value in EXTRA_CODES or live_schema.valid_code(value)):
        return True
    if field == "code" and value.startswith("rule-batch-"):
        tail = value[len("rule-batch-"):]
        if tail in ("resource", "credential-state", "count-mismatch") or any(tail == kind + "-" + name for kind in ("missing", "value", "unexpected") for name in live_schema.FIELDS):
            return True
    if field in BOOLEANS:
        return value in ("true", "false")
    if field == "api_activation_mutations":
        return value == "0"
    if field == "owned_case":
        return value in OWNED_CASES
    if field == "owned_readback_field":
        return value in live_schema.FIELDS
    if field == "extension_mode":
        return value in ("batch1000", "cleanup")
    if field in NUMBERS:
        bounds = NUMBERS[field]
        return len(value) <= len(str(bounds[1])) and bool(re.fullmatch(r"0|[1-9][0-9]*", value)) and bounds[0] <= int(value) <= bounds[1]
    return live_schema.valid_field(field, value)


def valid_case_field(test, field, value):
    if test == large_cleanup.TEST and field.startswith("large_cleanup_"):
        return large_cleanup.valid_field(field, value)
    if test in device_reset.CASES.values(): return device_reset.valid_field(field, value, test == device_reset.RESTORE_TEST)
    if test == CASES["discard-bounded"] and field not in BOUNDED_DISCARD_FIELDS: return False
    if test == CASES["transition-budget"]:
        if field not in TRANSITION_FIELDS: return False
        if field in TRANSITION_BOOLEANS: return value in ("true", "false")
        if field in TRANSITION_NUMBERS:
            low, high = TRANSITION_NUMBERS[field]
            return len(value) <= len(str(high)) and bool(re.fullmatch(r"0|[1-9][0-9]*", value)) and low <= int(value) <= high
        if field == "code": return value in TRANSITION_CODES or live_schema.valid_code(value)
        if field == "transition_stage": return value in TRANSITION_OPERATIONS
        enums = {"transition_kind": 0, "transition_resource": 1, "transition_action": 2}
        return field in enums and value in {op[enums[field]] for operations in TRANSITION_OPERATIONS.values() for op in operations}
    if test == CASES["selection-scale"] and field not in SELECTION_SCALE_FIELDS:
        return False
    if test == CASES["flag-query-presence"] and field not in FLAG_PRESENCE_FIELDS:
        return False
    if test == CASES["discard1000-global-tail"] and field not in DISCARD1000_GLOBAL_FIELDS:
        return False
    if test == CASES["discard-global-tail"] and field not in GLOBAL_TAIL_FIELDS:
        return False
    if test == CASES["presence-id-scale"]:
        if field not in PRESENCE_ID_FIELDS or field == "read_scale_phase" and value not in PRESENCE_ID_PHASES:
            return False
    if test == CASES["read-scale"] and field not in READ_SCALE_FIELDS:
        return False
    if test == CASES["read-scale"] and field == "read_scale_phase" and value not in READ_SCALE_PHASES:
        return False
    if test == CASES["read-scale-from"] and field not in READ_SCALE_FROM_FIELDS:
        return False
    if test == CASES["read-scale-from"] and field == "read_scale_phase" and value not in READ_SCALE_FROM_PHASES:
        return False
    if test == CASES["discard1000"] and field not in DISCARD1000_FIELDS:
        return False
    if test == CASES["discard1000"] and field == "removal_scale_batch_event" and value not in ("enter", "exit"):
        return False
    return valid_field(field, value)


def valid_case_row(test, row):
    if test in device_reset.CASES.values(): return bool(row) and all(device_reset.valid_field(field, value, test == device_reset.RESTORE_TEST) for field, value in row.items())
    if test == CASES["discard-bounded"] and BOUNDED_DISCARD_ONLY & row.keys():
        if "bounded_discard_request" in row:
            if row.keys() != BOUNDED_DISCARD_STEP: return False
            number = int(row["bounded_discard_request"])
            return row["bounded_discard_limit"] == ("2" if number == 1 else "1") and int(row["bounded_discard_completed"]) == 6+number and row["bounded_discard_pending"] == ("true" if number < 5 else "false") and row["bounded_discard_complete"] == ("true" if number == 5 else "false") and row["bounded_discard_return_verified"] == "true"
        if row.keys() in (BOUNDED_DISCARD_RECOVERY, BOUNDED_DISCARD_FINAL): return True
        return row.keys() <= BOUNDED_DISCARD_FAILURES | {"code", "discard_adapter_code"} and any(key in BOUNDED_DISCARD_FAILURES and value == "false" for key,value in row.items())
    if test == CASES["transition-budget"]:
        if "transition_stage" in row:
            if row.keys() != TRANSITION_STEP_FIELDS: return False
            number = int(row["transition_operation"])
            operations = TRANSITION_OPERATIONS[row["transition_stage"]]
            if number > len(operations) or tuple(row[field] for field in ("transition_kind", "transition_resource", "transition_action")) != operations[number-1]: return False
            before, after, delta = (int(row[field]) for field in ("transition_history_before", "transition_history_after", "transition_history_delta"))
            return after >= before and after-before == delta
        if row.keys() in (TRANSITION_BASELINE_FIELDS, TRANSITION_UPDATE_FIELDS, TRANSITION_CANCEL_FIELDS): return True
        failures = TRANSITION_BOOLEANS - {"transition_owner_preserved", "transition_all_parameters_verified", "transition_history_capacity_verified", "transition_manual_cleanup"}
        return len(row) in (1, 2) and len(row.keys()-{"code"}) == 1 and any(key in failures and value == "false" for key, value in row.items())
    if test == CASES["selection-scale"]:
        if SELECTION_SCALE_COUNTS & row.keys() or SELECTION_SCALE_TIMES & row.keys() or "selection_scale_phase" in row:
            return row.keys() == SELECTION_SCALE_COUNTS | SELECTION_SCALE_TIMES | {"selection_scale_phase"}
        if "selection_scale_mode" in row:
            return row.keys() == SELECTION_SCALE_PROOFS | {"selection_scale_mode"}
        if "extension_getter_resource" in row:
            return valid_case_row(CASES["discard1000"], row)
        return True
    if test == CASES["flag-query-presence"]:
        if FLAG_PRESENCE_GETTERS & row.keys():
            if row.keys() != FLAG_PRESENCE_GETTERS | {"flag_presence_mode"}:
                return False
            complete = int(row["flag_presence_getter_completed"])
            return complete == int(row["flag_presence_getter_true"]) + int(row["flag_presence_getter_false"]) and complete + int(row["flag_presence_getter_unknown"]) <= 3
        if (FLAG_PRESENCE_QUERY_FIELDS - {"api_code"}) & row.keys():
            if row.keys() != FLAG_PRESENCE_QUERY_FIELDS:
                return False
            expected, actual, matched, unexpected, missing = (int(row[name]) for name in ("flag_presence_expected_count", "flag_presence_query_count", "flag_presence_matched_count", "flag_presence_unexpected_count", "flag_presence_missing_count"))
            return (expected == (1 if row["flag_presence_predicate_disabled"] == "true" else 2) and
                    matched + unexpected == actual and matched + missing == expected and
                    (row["flag_presence_query_completed"] == "true") == (row["api_code"] == "none"))
        return True
    if test in (CASES["read-scale"], CASES["read-scale-from"], CASES["presence-id-scale"]):
        if READ_SCALE_NUMBERS & row.keys() or READ_SCALE_TIMES & row.keys() or "read_scale_phase" in row:
            return row.keys() == READ_SCALE_NUMBERS | READ_SCALE_TIMES | {"read_scale_phase"}
        if "extension_getter_resource" in row:
            return valid_case_row(CASES["discard1000"], row)
        if "read_scale_context_deadline" in row and row["read_scale_context_deadline"] == "true" and row.get("read_scale_context_done") != "true":
            return False
        return True
    if test not in (CASES["discard1000"], CASES["discard1000-global-tail"]):
        return True
    if {"extension_getter_resource", "extension_getter_field", "getter_count", "getter_milliseconds", "getter_context_done", "getter_context_deadline"} & row.keys():
        if row.keys() != DISCARD1000_GETTER_FIELDS:
            return False
        if row["deadline"] == "true" and row["deadline_kind"] not in ("command", "context"):
            return False
        if row["deadline"] == "false" and row["deadline_kind"] != "none" and row["api_code"] != "cancelled-or-deadline":
            return False
        if row["getter_context_deadline"] == "true" and row["getter_context_done"] != "true":
            return False
        records, sentences, size = (int(row[field]) for field in ("reply_records", "reply_sentences", "reply_bytes"))
        # ReplySentences includes the failed read attempt; EOF before a first
        # byte can legitimately be one attempted sentence and zero bytes.
        return records <= sentences and (records == 0 or size > 0)
    global_tail = test == CASES["discard1000-global-tail"]
    if global_tail and "removal_scale_phase_budget" in row:
        if row.keys() != DISCARD1000_GLOBAL_PHASE_BUDGET_FIELDS:
            return False
        return row["removal_scale_phase_context_deadline"] != "true" or row["removal_scale_phase_context_done"] == "true"
    if global_tail and "removal_scale_budget_event" in row:
        if row.keys() != DISCARD1000_GLOBAL_BUDGET_FIELDS:
            return False
        return int(row["removal_scale_discard_budget_remaining_milliseconds"]) == max(0, 420000-int(row["removal_scale_discard_elapsed_milliseconds"]))
    batch_fields = DISCARD1000_GLOBAL_BATCH_FIELDS if global_tail else DISCARD1000_BATCH_FIELDS
    diagnostic = DISCARD1000_TIMING_FIELDS | DISCARD1000_TOTAL_FIELDS | batch_fields
    if global_tail:
        diagnostic = diagnostic | DISCARD1000_GLOBAL_BUDGET_FIELDS | DISCARD1000_GLOBAL_PHASE_BUDGET_FIELDS
    if not diagnostic & row.keys():
        return True
    if "removal_scale_timing" in row:
        expected = DISCARD1000_TIMING_FIELDS
        total = row["removal_scale_timing"] == "total"
        if total:
            expected = expected | DISCARD1000_TOTAL_FIELDS
        if row.keys() != expected:
            return False
        elapsed = int(row["removal_scale_elapsed_milliseconds"])
        if int(row["removal_scale_timing_milliseconds"]) > elapsed:
            return False
        if row["removal_scale_context_deadline"] == "true" and row["removal_scale_context_done"] != "true":
            return False
        if total and (int(row["removal_scale_timing_milliseconds"]) != elapsed or
                      int(row["removal_scale_accounted_milliseconds"]) + int(row["removal_scale_unaccounted_milliseconds"]) != elapsed):
            return False
        return True
    if row.keys() != batch_fields:
        return False
    completed = int(row["removal_scale_batch_completed"])
    if completed > int(row["removal_scale_batch_requested"]) or completed > int(row["removal_scale_batch_total_completed"]):
        return False
    if int(row["removal_scale_batch_milliseconds"]) > int(row["removal_scale_elapsed_milliseconds"]):
        return False
    if row["removal_scale_batch_event"] == "enter" and (completed != 0 or row["removal_scale_batch_error"] != "false"):
        return False
    if global_tail:
        if int(row["removal_scale_remaining_milliseconds"]) > int(row["removal_scale_lifecycle_remaining_milliseconds"]):
            return False
        if int(row["removal_scale_remaining_milliseconds"]) > int(row["removal_scale_discard_budget_remaining_milliseconds"])+1:
            return False
        acknowledged = int(row["removal_scale_batch_acknowledged"])
        if acknowledged > int(row["removal_scale_batch_requested"]) or acknowledged > int(row["removal_scale_batch_total_acknowledged"]):
            return False
        if row["removal_scale_batch_event"] == "tail":
            if row["removal_scale_batch_verified_tail"] != "true" or row["removal_scale_batch_pending"] != "false" or completed != acknowledged:
                return False
        elif completed != 0 or row["removal_scale_batch_verified_tail"] != "false":
            return False
    return True


def complete_discard1000_diagnostics(rows, phases, counts, global_tail=False):
    test = CASES["discard1000-global-tail"] if global_tail else CASES["discard1000"]
    timings = [row for row in rows if "removal_scale_timing" in row]
    if tuple(row["removal_scale_timing"] for row in timings) != DISCARD1000_TIMINGS:
        return False
    if any(not valid_case_row(test, row) for row in timings):
        return False
    duration = {row["removal_scale_timing"]: int(row["removal_scale_timing_milliseconds"]) for row in timings}
    if duration["total"] > 540000 or any(row["removal_scale_context_done"] != "false" for row in timings):
        return False
    # fresh-open is nested inside the existing fresh phase; counting it twice
    # would conceal the replay/closing gaps this diagnostic is meant to expose.
    accounted = sum(int(row["removal_scale_milliseconds"]) for row in phases)
    accounted += sum(duration[name] for name in ("first-open", "journal-open", "journal-replay", "rejected-review"))
    if int(timings[-1]["removal_scale_accounted_milliseconds"]) != accounted:
        return False
    fresh = next(row for row in phases if row["removal_scale_phase"] == "fresh")
    if duration["fresh-open"] > int(fresh["removal_scale_milliseconds"]):
        return False
    if global_tail:
        phase_budgets = [row for row in rows if "removal_scale_phase_budget" in row]
        if tuple(row["removal_scale_phase_budget"] for row in phase_budgets) != DISCARD1000_PHASES or any(not valid_case_row(test, row) or row["removal_scale_phase_context_done"] != "false" for row in phase_budgets):
            return False
        budgets = [row for row in rows if "removal_scale_budget_event" in row]
        if tuple(row["removal_scale_budget_event"] for row in budgets) != ("start", "end") or any(not valid_case_row(test, row) for row in budgets):
            return False
        if int(budgets[0]["removal_scale_discard_elapsed_milliseconds"]) > int(budgets[1]["removal_scale_discard_elapsed_milliseconds"]):
            return False
        if int(budgets[0]["removal_scale_lifecycle_remaining_milliseconds"]) < int(budgets[1]["removal_scale_lifecycle_remaining_milliseconds"]):
            return False
    batch_rows = [row for row in rows if "removal_scale_batch_event" in row]
    group_size = 3 if global_tail else 2
    if len(batch_rows) != group_size * counts["removal_scale_batches"]:
        return False
    completed = 0
    for sequence, offset in enumerate(range(0, len(batch_rows), group_size), 1):
        if global_tail:
            enter, exit, tail = batch_rows[offset:offset+3]
            if any(not valid_case_row(test, row) for row in (enter, exit, tail)):
                return False
            if tuple(row["removal_scale_batch_event"] for row in (enter, exit, tail)) != ("enter", "exit", "tail"):
                return False
            if any(int(row["removal_scale_batch_sequence"]) != sequence or row["removal_scale_batch_error"] != "false" for row in (enter, exit, tail)):
                return False
            if any(row["removal_scale_batch_requested"] != enter["removal_scale_batch_requested"] for row in (exit, tail)):
                return False
            prefix = int(exit["removal_scale_batch_acknowledged"])
            if prefix < 1 or int(enter["removal_scale_batch_acknowledged"]) != 0 or int(tail["removal_scale_batch_acknowledged"]) != prefix:
                return False
            if any(int(row["removal_scale_batch_total_completed"]) != completed for row in (enter, exit)) or int(enter["removal_scale_batch_total_acknowledged"]) != completed:
                return False
            if enter["removal_scale_batch_pending"] != "false" or exit["removal_scale_batch_pending"] != "true":
                return False
            completed += prefix
            if int(tail["removal_scale_batch_total_completed"]) != completed or any(int(row["removal_scale_batch_total_acknowledged"]) != completed for row in (exit, tail)):
                return False
            if not int(enter["removal_scale_batch_milliseconds"]) <= int(exit["removal_scale_batch_milliseconds"]) <= int(tail["removal_scale_batch_milliseconds"]):
                return False
            if not int(enter["removal_scale_remaining_milliseconds"]) >= int(exit["removal_scale_remaining_milliseconds"]) >= int(tail["removal_scale_remaining_milliseconds"]):
                return False
            continue
        enter, exit = batch_rows[offset:offset+2]
        if any(not valid_case_row(CASES["discard1000"], row) for row in (enter, exit)):
            return False
        if enter["removal_scale_batch_event"] != "enter" or exit["removal_scale_batch_event"] != "exit":
            return False
        if any(int(row["removal_scale_batch_sequence"]) != sequence or row["removal_scale_batch_error"] != "false" for row in (enter, exit)):
            return False
        if enter["removal_scale_batch_requested"] != exit["removal_scale_batch_requested"] or int(enter["removal_scale_batch_total_completed"]) != completed:
            return False
        prefix = int(exit["removal_scale_batch_completed"])
        completed += prefix
        if prefix == 0 or int(exit["removal_scale_batch_total_completed"]) != completed:
            return False
        if int(exit["removal_scale_remaining_milliseconds"]) > int(enter["removal_scale_remaining_milliseconds"]):
            return False
        if int(exit["removal_scale_batch_milliseconds"]) < int(enter["removal_scale_batch_milliseconds"]):
            return False
    if completed != counts["removal_scale_batch_rules"]:
        return False
    timeline = [int(row["removal_scale_elapsed_milliseconds"]) for row in rows if "removal_scale_elapsed_milliseconds" in row]
    return timeline == sorted(timeline)


def complete_discard1000(rows, global_tail=False):
    required = DISCARD1000_GLOBAL_REQUIRED if global_tail else DISCARD1000_REQUIRED
    if not all(any(row.get(key) == value for row in rows) and
               all(row[key] == value for row in rows if key in row) for key, value in required.items()):
        return False
    if any(row.get(field) == "false" for row in rows for field in DISCARD1000_FAILURE_FIELDS):
        return False
    if global_tail and any(row.get("removal_scale_proof_verified") == "false" for row in rows):
        return False
    if any(row.get(field, "none") != "none" for row in rows for field in (
            "code", "api_code", "extension_observation_code", "removal_scale_adapter_code")):
        return False
    prepare_batches = [row for row in rows if "extension_batch_runtime" in row]
    if len(prepare_batches) != 1 or any(prepare_batches[0].get(key) != value for key, value in {
            "extension_batch_runtime": "true", "code": "none", "batch_acknowledged": "2004"}.items()):
        return False
    counts = {}
    for field in DISCARD1000_COUNTS | (DISCARD1000_GLOBAL_COUNTS if global_tail else set()):
        values = [row[field] for row in rows if field in row]
        if len(values) != 1:
            return False
        counts[field] = int(values[0])
    def count(name):
        return counts["removal_scale_" + name]
    if count("generation_rules") != 2004 or count("prepare_rule_acks") != 2004:
        return False
    if any(count(name) != 2009 for name in ("add_total", "add_unique_ids", "remove_attempts", "remove_acks", "remove_unique_ids")):
        return False
    if count("forbidden_calls") != 0 or count("batches") < 1 or count("single_rule_removals") not in (0, 1):
        return False
    if count("batch_rules") + count("single_rule_removals") != 2004 or count("single_removals") - count("single_rule_removals") != 5:
        return False
    if not count("batches") <= count("batch_rules") <= 256 * count("batches"):
        return False
    if global_tail and (count("batch_rule_acks") != count("batch_rules") or count("full_tail_reads") != count("batches") or count("pending_guards") != count("batches")):
        return False
    phases = [row for row in rows if "removal_scale_phase" in row]
    if tuple(row["removal_scale_phase"] for row in phases) != DISCARD1000_PHASES:
        return False
    metric_fields = DISCARD1000_METRICS | {"removal_scale_milliseconds"}
    if any(not metric_fields <= row.keys() for row in phases):
        return False
    if any(metric_fields & row.keys() and "removal_scale_phase" not in row for row in rows):
        return False
    if sum(int(row["removal_scale_milliseconds"]) for row in phases) > 540000:
        return False
    for row in phases:
        phase = row["removal_scale_phase"]
        def metric(name):
            return int(row["removal_scale_" + name])
        if metric("api_writes") < metric("api_adds") + metric("api_removes"):
            return False
        if metric("property_batches") > metric("property_items") or metric("inventory_calls") == 0:
            return False
        # Only successful, full two-round observations count as selection
        # proofs. Object reads and the final <128-rule tail still use getters.
        queries = metric("false_queries")
        if queries != metric("true_queries") or queries > metric("inventory_calls") or metric("true_ids_total") != 0:
            return False
        if not 128*queries <= metric("false_ids_total") <= 2006*queries or 2*metric("selection_observations") > queries:
            return False
        if metric("disabled_getters") > metric("property_items"):
            return False
        if phase in ("baseline", "final") and any(metric(name) for name in ("false_queries", "true_queries", "false_ids_total", "true_ids_total", "selection_observations", "disabled_getters")):
            return False
        if phase in ("checkpoint", "fresh") and (queries != 2 or metric("false_ids_total") != 4012 or metric("selection_observations") != 1 or metric("disabled_getters") != 0):
            return False
        if phase in ("stage", "discard") and metric("selection_observations") < 1:
            return False
        if phase == "stage":
            # Fixed bootstrap Compile: 2009 Add, then the new connection's
            # unused AF limit Unset and explicit empty authentication Set.
            if metric("api_writes") != 2011 or metric("api_adds") != 2009 or metric("api_removes") != 0 or metric("ssh_owned_writes") == 0 or metric("ssh_takes") != 1:
                return False
        elif phase == "discard":
            if metric("api_writes") != 2009 or metric("api_removes") != 2009 or metric("api_adds") != 0:
                return False
            # Interface call counts include protected-object reads; these
            # lower bounds do not claim per-target wire RTT measurements.
            if global_tail:
                if metric("object_reads") == 0 and metric("presence_reads") < 2 * count("batch_rules") or metric("object_reads") != 0 and metric("object_reads") < 2 * count("batch_rules"):
                    return False
            elif metric("object_reads") == 0:
                if metric("presence_reads") < 4 * count("batch_rules"):
                    return False
            elif metric("object_reads") < 2 * count("batch_rules") or metric("presence_reads") < 2 * count("batch_rules"):
                return False
        elif any(metric(name) != 0 for name in ("api_writes", "api_adds", "api_removes", "ssh_owned_writes", "ssh_takes")):
            return False
        if phase in ("fresh", "discard", "final") and any(metric(name) != 0 for name in ("ssh_owned_writes", "ssh_takes", "ssh_releases")):
            return False
    return complete_discard1000_diagnostics(rows, phases, counts, global_tail)


def complete(test, rows):
    if test == large_cleanup.TEST:
        return large_cleanup.complete(rows)
    if test in device_reset.CASES.values(): return device_reset.complete(rows, test == device_reset.RESTORE_TEST)
    if test == CASES["discard-bounded"]:
        if not all([row[key] for row in rows if key in row] == [value] for key,value in BOUNDED_DISCARD_REQUIRED.items()): return False
        if any(row.get(key) == "false" for row in rows for key in BOUNDED_DISCARD_FAILURES): return False
        selected = [row for row in rows if BOUNDED_DISCARD_ONLY & row.keys()]
        if len(selected) != 7 or any(not valid_case_row(test,row) for row in selected): return False
        if selected[1].keys() != BOUNDED_DISCARD_RECOVERY or selected[-1].keys() != BOUNDED_DISCARD_FINAL: return False
        steps = [row for row in selected if "bounded_discard_request" in row]
        if [int(row["bounded_discard_request"]) for row in steps] != list(range(1,6)): return False
        return complete(CASES["discard-global-tail"], [row for row in rows if not BOUNDED_DISCARD_ONLY & row.keys()])
    if test == CASES["transition-budget"]:
        # Successful native output has four baseline steps, its summary, nine
        # transition steps, and the exact update/cancel summaries in that order.
        if len(rows) != 16 or any(not valid_case_row(test, row) for row in rows): return False
        if rows[4].keys() != TRANSITION_BASELINE_FIELDS or rows[14].keys() != TRANSITION_UPDATE_FIELDS or rows[15].keys() != TRANSITION_CANCEL_FIELDS: return False
        for field, value in TRANSITION_REQUIRED.items():
            actual = [row[field] for row in rows if field in row]
            if actual != [value]: return False
        totals = {}
        for stage, group in (("baseline", rows[:4]), ("transition", rows[5:14])):
            previous, total = 0, 0
            for number, row in enumerate(group, 1):
                if row.get("transition_stage") != stage or int(row.get("transition_operation", "0")) != number or row.get("transition_owner_preserved") != "true": return False
                if int(row["transition_history_before"]) != previous: return False
                previous = int(row["transition_history_after"])
                total += int(row["transition_history_delta"])
            totals[stage] = total
        if int(rows[14]["transition_maximum_history"]) != totals["transition"] or int(rows[14]["transition_history_total_delta"]) != totals["transition"]: return False
        return sum(int(rows[index][field]) for index, field in ((4, "transition_baseline_milliseconds"), (14, "transition_update_milliseconds"), (15, "transition_cancel_milliseconds"))) <= 240000
    if test == CASES["selection-scale"]:
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in SELECTION_SCALE_REQUIRED.items()):
            return False
        if any(row.get(key) == "false" for row in rows for key in SELECTION_SCALE_FAILURES) or any(row.get(field, "none") != "none" for row in rows for field in ("code", "api_code")):
            return False
        proofs = [row for row in rows if "selection_scale_mode" in row]
        if tuple(row["selection_scale_mode"] for row in proofs) != ("initial", "toggled") or any(not valid_case_row(test, row) or any(row[key] != "true" for key in SELECTION_SCALE_PROOFS) for row in proofs):
            return False
        phases = [row for row in rows if "selection_scale_phase" in row]
        totals = [int(row["selection_scale_total_milliseconds"]) for row in rows if "selection_scale_total_milliseconds" in row]
        if tuple(row["selection_scale_phase"] for row in phases) != SELECTION_SCALE_PHASES or len(totals) != 1 or totals[0] > 240000 or sum(int(row["selection_scale_milliseconds"]) for row in phases) > totals[0]:
            return False
        for row in phases:
            if not valid_case_row(test, row): return False
            phase = row["selection_scale_phase"]
            def count(name): return int(row["selection_scale_"+name])
            writes = 2008 if phase == "setup" else 2 if phase == "toggle" else 0
            inventories = 2 if phase.endswith("-observe") else 1 if phase.endswith(("-getter", "-selection")) else 0
            partitions = 2 if phase.endswith("-observe") else 1 if phase.endswith("-selection") else 0
            if count("api_writes") != writes or count("credential_reads") != 0 or count("inventory_calls") != inventories:
                return False
            if count("predicate_calls") != 2*partitions or count("false_queries") != partitions or count("true_queries") != partitions or count("false_ids_total") != 2005*partitions or count("true_ids_total") != partitions:
                return False
            if phase.endswith("-getter"):
                if not 1 <= count("disabled_getters") <= 2006: return False
            elif count("disabled_getters") != 0: return False
            if count("property_items") < count("disabled_getters") or count("property_batches") > count("property_items"):
                return False
            if sum(count(field) for field in ("inventory_milliseconds", "getter_milliseconds", "predicate_milliseconds", "write_milliseconds")) > count("milliseconds"):
                return False
        return True
    if test == CASES["flag-query-presence"]:
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in FLAG_PRESENCE_REQUIRED.items()):
            return False
        if any(row.get("flag_presence_query_bounded") == "false" or row.get("code", "none") != "none" or row.get("api_code", "none") != "none" for row in rows):
            return False
        getters = [row for row in rows if FLAG_PRESENCE_GETTERS & row.keys()]
        if tuple(row.get("flag_presence_mode") for row in getters) != ("initial", "toggled"):
            return False
        for row in getters:
            if not valid_case_row(test, row) or any(row[field] != value for field, value in {"flag_presence_getter_completed": "3", "flag_presence_getter_true": "1", "flag_presence_getter_false": "2", "flag_presence_getter_unknown": "0"}.items()):
                return False
        queries = [row for row in rows if "flag_presence_predicate_disabled" in row]
        if tuple((row.get("flag_presence_mode"), row["flag_presence_predicate_disabled"]) for row in queries) != (("initial", "false"), ("initial", "true"), ("toggled", "false"), ("toggled", "true")):
            return False
        for row in queries:
            if not valid_case_row(test, row) or row["flag_presence_query_completed"] != "true" or row["flag_presence_unexpected_count"] != "0" or row["flag_presence_missing_count"] != "0":
                return False
        proofs = [row for row in rows if "flag_query_mode" in row]
        return (tuple(row["flag_query_mode"] for row in proofs) == ("initial", "toggled") and
                all(row.get("flag_query_getter_exact") == "true" and row.get("flag_query_membership_exact") == "true" for row in proofs))
    if test == CASES["discard1000-global-tail"]:
        return complete_discard1000(rows, global_tail=True)
    if test == CASES["discard-global-tail"]:
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in GLOBAL_TAIL_REQUIRED.items()):
            return False
        if not complete(CASES["discard"], rows):
            return False
        counts = {}
        for field in GLOBAL_TAIL_COUNTS | {"discard_original_additions", "discard_remove_attempts", "discard_remove_acknowledged"}:
            values = [row[field] for row in rows if field in row]
            if len(values) != 1:
                return False
            counts[field] = int(values[0])
        if any(counts[field] != 11 for field in ("discard_original_additions", "discard_remove_attempts", "discard_remove_acknowledged")):
            return False
        if not 1 <= counts["global_tail_batches"] <= counts["global_tail_rule_acks"] == 6:
            return False
        if counts["global_tail_verified_full_reads"] != counts["global_tail_batches"] or counts["global_tail_pending_guards"] != counts["global_tail_batches"] or counts["global_tail_absent_callbacks"] != 0:
            return False
        return not any(row.get(field, "none") != "none" for row in rows for field in ("code", "discard_adapter_code"))
    if test == CASES["presence-id-scale"]:
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in PRESENCE_ID_REQUIRED.items()):
            return False
        if any(row.get(key) == "false" for row in rows for key in PRESENCE_ID_FAILURE_FIELDS):
            return False
        if any(row.get(field, "none") != "none" for row in rows for field in ("code", "api_code")):
            return False
        phases = [row for row in rows if "read_scale_phase" in row]
        totals = [int(row["read_scale_total_milliseconds"]) for row in rows if "read_scale_total_milliseconds" in row]
        if tuple(row["read_scale_phase"] for row in phases) != PRESENCE_ID_PHASES or len(totals) != 1 or totals[0] > 240000:
            return False
        if sum(int(row["read_scale_milliseconds"]) for row in phases) > totals[0]:
            return False
        for row in phases:
            if not valid_case_row(test, row):
                return False
            phase = row["read_scale_phase"]
            def metric(name):
                return int(row["read_scale_"+name])
            if any(metric(field) != 0 for field in ("credential_reads", "property_items", "property_batches", "getter_milliseconds")):
                return False
            if metric("api_writes") != (2008 if phase == "setup" else 0) or metric("inventory_calls") != (1 if phase == "inventory" else 0):
                return False
            if metric("presence_reads") != {"existing": 3, "absent": 2}.get(phase, 0):
                return False
            if sum(metric(name) for name in ("inventory_milliseconds", "getter_milliseconds", "presence_milliseconds", "write_milliseconds")) > metric("milliseconds"):
                return False
        return True
    if test in (CASES["read-scale"], CASES["read-scale-from"]):
        variant = test == CASES["read-scale-from"]
        required = READ_SCALE_FROM_REQUIRED if variant else READ_SCALE_REQUIRED
        expected_phases = READ_SCALE_FROM_PHASES if variant else READ_SCALE_PHASES
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in required.items()):
            return False
        if any(row.get(key) == "false" for row in rows for key in READ_SCALE_FAILURE_FIELDS):
            return False
        if any(row.get(field, "none") != "none" for row in rows for field in ("code", "api_code")):
            return False
        phases = [row for row in rows if "read_scale_phase" in row]
        totals = [int(row["read_scale_total_milliseconds"]) for row in rows if "read_scale_total_milliseconds" in row]
        if tuple(row["read_scale_phase"] for row in phases) != expected_phases or len(totals) != 1 or totals[0] > 240000:
            return False
        if sum(int(row["read_scale_milliseconds"]) for row in phases) > totals[0]:
            return False
        for row in phases:
            if not valid_case_row(test, row):
                return False
            phase = row["read_scale_phase"]
            def metric(name):
                return int(row["read_scale_"+name])
            if metric("credential_reads") != 0 or metric("api_writes") != (2008 if phase == "setup" else 0):
                return False
            if metric("inventory_calls") != {"scan-one": 1, "scan-two": 1, "observe": 2}.get(phase, 0):
                return False
            if metric("presence_reads") != {"exact-rules": 6, "protected": 4, "absent": 2}.get(phase, 0):
                return False
            if phase == "absent" and (metric("property_items") != 0 or metric("property_batches") != 0):
                return False
            if metric("property_batches") > metric("property_items"):
                return False
            if sum(metric(name) for name in ("inventory_milliseconds", "getter_milliseconds", "presence_milliseconds", "write_milliseconds")) > metric("milliseconds"):
                return False
        return True
    if test == CASES["discard1000"]:
        return complete_discard1000(rows)
    if test == CASES["review-reuse"]:
        return all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in REVIEW_REUSE_REQUIRED.items())
    if test == CASES["presence"]:
        return all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in PRESENCE_REQUIRED.items())
    if test == CASES["discard"]:
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in DISCARD_REQUIRED.items()):
            return False
        return any(all(key in row for key in ("discard_original_additions", "discard_remove_attempts", "discard_remove_acknowledged")) and row["discard_original_additions"] == row["discard_remove_attempts"] == row["discard_remove_acknowledged"] for row in rows)
    if test == CASES["flag-query"]:
        if not any(row.get("flag_query_cleanup_after_proof") == "true" for row in rows):
            return False
        return all(any(row.get("flag_query_mode") == mode and row.get("flag_query_getter_exact") == "true" and row.get("flag_query_membership_exact") == "true" for row in rows) for mode in ("initial", "toggled"))
    if test == CASES["owned-delay"]:
        required = {"owned_order_before_release_still_owned": "true", "baseline_observed": "true", "rollback_claimed": "false", "owned_order_data_before_eof": "true", "owner_released": "true", "checkpoint_restored": "true", "late_write_observed": "false", "api_activation_write": "false"}
        return all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in required.items())
    if test == CASES["owned"]:
        required = {"owned_checkpoint_restored": "true", "api_activation_mutations": "0", "manual_cleanup_before_proof": "false"}
        if not all(any(row.get(key) == value for row in rows) and all(row[key] == value for row in rows if key in row) for key, value in required.items()):
            return False
        cases = [row for row in rows if "owned_case" in row]
        return len(cases) == len(OWNED_CASES) and all(sum(row.get("owned_case") == case and row.get("completed") == "true" and row.get("owner_current") == "true" and row.get("api_mutation_used") == "false" for row in cases) == 1 for case in OWNED_CASES)
    if test == CASES["batch1000"]:
        if any(row.get("extension_mode", "batch1000") != "batch1000" for row in rows):
            return False
        for row in rows:
            if row.get("extension_mode") == "batch1000" and all(row.get(key) == value for key, value in {"batches": "1", "checkpoint_equal": "true", "credentials_equal": "true", "cleanup_before_proof": "false"}.items()):
                if not all(key in row for key in ("generation_rules", "acknowledged_rules", "stage_inventory_calls", "stage_milliseconds", "stage_object_reads", "stage_identity_reads")):
                    continue
                count = int(row["generation_rules"])
                if count == int(row["acknowledged_rules"]) and int(row["stage_inventory_calls"]) < count//4 and int(row["stage_object_reads"]) > 0 and row["stage_object_reads"] == row["stage_identity_reads"]:
                    return True
        return False
    if test == CASES["cleanup"]:
        if any(row.get("extension_mode", "cleanup") != "cleanup" for row in rows):
            return False
        required = {"cleanup_complete": "true", "confirmed_preserved": "true", "strict_desired": "true", "old_targets_absent": "true", "new_review_requested": "false", "cleanup_before_proof": "false"}
        return any(row.get("extension_mode") == "cleanup" and all(row.get(key) == value for key, value in required.items()) and "approved_removals" in row and row["approved_removals"] == row.get("acknowledged_removals") for row in rows)
    return False
