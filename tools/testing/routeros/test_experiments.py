"""Reject incomplete or overbroad claims from explicitly selected experiments."""
from pathlib import Path
import re
import tempfile
import unittest

import test_harness as base


class ExperimentContract(unittest.TestCase):
    def transcript(self, case, rows):
        name = base.experiments.CASES[case]
        filename = "native_owned_test.go" if case == "owned" else "native_extensions_test.go"
        if case == "discard":
            filename = "native_discard_test.go"
        if case == "discard-global-tail":
            filename = "native_global_tail_test.go"
        if case in ("discard1000", "discard1000-global-tail"):
            filename = "native_removal_scale_test.go"
        if case in ("read-scale", "read-scale-from"):
            filename = "native_read_scale_test.go"
        if case == "presence-id-scale":
            filename = "native_presence_id_scale_test.go"
        if case == "selection-scale":
            filename = "native_selection_scale_test.go"
        if case == "flag-query-presence":
            filename = "native_flag_query_test.go"
        if case == "presence":
            filename = "native_presence_test.go"
        if case == "discard-bounded":
            filename = "native_bounded_discard_test.go"
        if case == "transition-budget":
            filename = "native_transition_budget_test.go"
        if case == "review-reuse":
            filename = "native_rollback_review_test.go"
        return ("\n".join(["=== RUN   "+name] + [f"    {filename}:1: "+row for row in rows] + ["--- PASS: "+name+" (5.00s)", "PASS"])).encode()

    def test_owner_requires_each_exact_operation_and_independent_checkpoint(self):
        rows = ["owned_case="+case+" completed=true owner_current=true api_mutation_used=false" for case in sorted(base.experiments.OWNED_CASES)]
        rows += ["owned_checkpoint_restored=true api_activation_mutations=0 manual_cleanup_before_proof=false"]
        name = base.experiments.CASES["owned"]
        self.assertTrue(base.evidence.inspect_output(self.transcript("owned", rows), 0, name)[0])
        for bad in (rows[1:], rows+[rows[0]], [row.replace("api_mutation_used=false", "api_mutation_used=true") for row in rows], rows+["owned_checkpoint_restored=false"]):
            self.assertFalse(base.evidence.inspect_output(self.transcript("owned", bad), 0, name)[0])

    def flag_presence_rows(self):
        rows = [{"flag_presence_variant": "true"}]
        for mode in ("initial", "toggled"):
            rows.append({"flag_presence_mode": mode, "flag_presence_getter_completed": "3", "flag_presence_getter_true": "1", "flag_presence_getter_false": "2", "flag_presence_getter_unknown": "0"})
            for disabled, count in (("false", "2"), ("true", "1")):
                rows.append({"flag_presence_mode": mode, "flag_presence_predicate_disabled": disabled, "flag_presence_expected_count": count,
                             "flag_presence_query_count": count, "flag_presence_matched_count": count, "flag_presence_unexpected_count": "0",
                             "flag_presence_missing_count": "0", "flag_presence_query_completed": "true", "api_code": "none"})
            rows.append({"flag_query_mode": mode, "flag_query_getter_exact": "true", "flag_query_membership_exact": "true"})
        rows.append({"flag_query_cleanup_after_proof": "true", "flag_presence_gates_unchanged": "true", "flag_presence_observer_shortcut_verified": "false"})
        return rows

    def flag_presence_transcript(self, rows):
        return self.transcript("flag-query-presence", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def test_flag_presence_requires_getters_both_partitions_and_toggle(self):
        name = base.experiments.CASES["flag-query-presence"]
        rows = self.flag_presence_rows()
        self.assertEqual(name, "TestNativeCHRLiveBooleanPresenceQuery")
        good = self.flag_presence_transcript(rows)
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for i, row in enumerate(rows):
            for field in row:
                changed = [dict(row) for row in rows]
                del changed[i][field]
                self.assertFalse(base.evidence.inspect_output(self.flag_presence_transcript(changed), 0, name)[0], field)
        for field, value in (("flag_presence_query_count", "0"), ("flag_presence_matched_count", "1"), ("flag_presence_unexpected_count", "1"), ("flag_presence_missing_count", "1"), ("flag_presence_query_completed", "false"), ("api_code", "command-failed")):
            changed = [dict(row) for row in rows]
            changed[2][field] = value
            self.assertFalse(base.evidence.inspect_output(self.flag_presence_transcript(changed), 0, name)[0], field)
        for changed in (rows+[rows[2]], rows[:2]+rows[3:], rows[:-1], rows[:5]+rows[1:5]+rows[-1:]):
            self.assertFalse(base.evidence.inspect_output(self.flag_presence_transcript(changed), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"flag_presence_observer_shortcut_verified=false", b"flag_presence_observer_shortcut_verified=true"), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])

    def test_flag_presence_counts_are_bounded_and_failures_remain_diagnostic(self):
        name = base.experiments.CASES["flag-query-presence"]
        missing = {"flag_presence_mode": "initial", "flag_presence_predicate_disabled": "false", "flag_presence_expected_count": "2",
                   "flag_presence_query_count": "0", "flag_presence_matched_count": "0", "flag_presence_unexpected_count": "0",
                   "flag_presence_missing_count": "2", "flag_presence_query_completed": "true", "api_code": "none"}
        verified, safe = base.evidence.inspect_output(self.flag_presence_transcript([missing, {"flag_query_membership_exact": "false"}]), 1, name)
        self.assertFalse(verified)
        self.assertTrue(any("flag_presence_query_count=0" in row and "flag_presence_missing_count=2" in row for row in safe))
        for field, value in (("flag_presence_query_count", "4"), ("flag_presence_getter_completed", "-1"), ("flag_presence_mode", "private-data"), ("flag_presence_unknown", "private-data"), ("code", "private-data")):
            _, safe = base.evidence.inspect_output(self.flag_presence_transcript([{field: value}]), 1, name)
            self.assertFalse(any(field+"="+value in row for row in safe))
        good = self.flag_presence_transcript(self.flag_presence_rows())
        self.assertFalse(base.evidence.inspect_output(good.replace(b"native_flag_query_test.go", b"native_flag_presence_unknown.go"), 0, name)[0])

    def test_large_batch_and_cleanup_counts_must_match_verified_effects(self):
        good = {
            "batch1000": "extension_mode=batch1000 generation_rules=2004 acknowledged_rules=2004 batches=1 stage_inventory_calls=60 stage_milliseconds=90000 stage_object_reads=16 stage_identity_reads=16 checkpoint_equal=true credentials_equal=true cleanup_before_proof=false",
            "cleanup": "extension_mode=cleanup approved_removals=14 acknowledged_removals=14 cleanup_complete=true confirmed_preserved=true strict_desired=true old_targets_absent=true new_review_requested=false cleanup_before_proof=false",
        }
        for case, row in good.items():
            name = base.experiments.CASES[case]
            body = self.transcript(case, [row])
            self.assertTrue(base.evidence.inspect_output(body, 0, name)[0])
            self.assertFalse(base.evidence.inspect_output(body, 1, name)[0])
            self.assertFalse(base.evidence.inspect_output(body.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])
            for item in row.split():
                self.assertFalse(base.evidence.inspect_output(self.transcript(case, [row.replace(item, "")]), 0, name)[0])
        for old, new in (("acknowledged_rules=2004", "acknowledged_rules=2003"), ("stage_inventory_calls=60", "stage_inventory_calls=2004"), ("batches=1", "batches=0"), ("checkpoint_equal=true", "checkpoint_equal=false"), ("stage_identity_reads=16", "stage_identity_reads=15"), ("stage_object_reads=16", "stage_object_reads=0")):
            self.assertFalse(base.evidence.inspect_output(self.transcript("batch1000", [good["batch1000"].replace(old, new)]), 0, base.experiments.CASES["batch1000"])[0])
        self.assertFalse(base.evidence.inspect_output(self.transcript("cleanup", [good["cleanup"].replace("acknowledged_removals=14", "acknowledged_removals=15")]), 0, base.experiments.CASES["cleanup"])[0])
        body = self.transcript("batch1000", [good["batch1000"], "code=private-value"])
        success, safe = base.evidence.inspect_output(body, 0, base.experiments.CASES["batch1000"])
        self.assertFalse(success)
        self.assertNotIn("private-value", "\n".join(safe))

    def test_selected_case_is_standalone_and_has_bounded_native_budget(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "tools"
            base.HarnessContract().tool_tree(tools)
            image, parent, live = (root / name for name in ("image", "parent.test", "live.test"))
            for path in (image, parent, live):
                path.write_bytes(b"fixture")
            for case, name in base.experiments.CASES.items():
                live_binary = live if case in base.experiments.LIVE_CASES else None
                terminal_binary = parent if case == "owned-delay" else None
                command = base.runner.build_command(image, tools, parent, base.HERE, ssh=True, live_binary=live_binary, native_case=case, terminal_binary=terminal_binary)
                self.assertEqual(command[-3:], ["--ssh", "--case", case])
                self.assertIn("1440s" if case == "cleanup1000-confirm" else "900s" if case in base.experiments.LONG_CASES else "480s", command)
                args = base.transport.test_arguments(name)
                self.assertEqual(args[2], "^"+name+"$")
                if case == "cleanup1000-confirm":
                    go_budget = 1260
                elif case in base.experiments.LONG_CASES:
                    go_budget = 600
                elif case in ("read-scale", "read-scale-from", "presence-id-scale", "selection-scale", "transition-budget", "discard-bounded", "device-reset", "device-restore"):
                    go_budget = 300
                else:
                    go_budget = 240
                self.assertEqual(args[-1], f"{go_budget}s")
                self.assertEqual(args[0], "/lab/routeros-live.test" if case in base.experiments.LIVE_CASES else "/lab/routeros-terminal.test" if case == "owned-delay" else "/lab/routeros.test")
                self.assertEqual(base.transport.test_wall_seconds(name), go_budget + 10)
                for option in ("properties", "live", "bgp_cache", "ssh_disconnect", "bgp_generated_ipv4"):
                    with self.assertRaises(base.runner.InvalidInput):
                        base.runner.build_command(image, tools, parent, base.HERE, ssh=True, live_binary=live_binary, native_case=case, **{option: True})
            with self.assertRaises(base.runner.InvalidInput):
                base.runner.build_command(image, tools, parent, base.HERE, ssh=True, native_case="unknown")
            for settings in ({"ssh": True}, {"ssh": False, "live_binary": live}):
                with self.assertRaises(base.runner.InvalidInput):
                    base.runner.build_command(image, tools, parent, base.HERE, native_case="discard1000", **settings)

    def transition_rows(self, zero=False):
        contract = base.experiments
        rows = []
        for stage in ("baseline", "transition"):
            history = 0
            for number, (kind, resource, action) in enumerate(contract.TRANSITION_OPERATIONS[stage], 1):
                # A completed operation may add no history action; never infer
                # the device's actual undo cost from an operation count.
                delta = 0 if zero or number == 1 else 1
                rows.append({"transition_stage": stage, "transition_operation": str(number), "transition_kind": kind,
                             "transition_resource": resource, "transition_action": action,
                             "transition_history_before": str(history), "transition_history_after": str(history+delta),
                             "transition_history_delta": str(delta), "transition_owner_preserved": "true"})
                history += delta
            fields = contract.TRANSITION_BASELINE_FIELDS if stage == "baseline" else contract.TRANSITION_UPDATE_FIELDS
            row = {key: contract.TRANSITION_REQUIRED.get(key, "1000") for key in fields}
            if stage == "transition": row.update(transition_maximum_history=str(history), transition_history_total_delta=str(history))
            rows.append(row)
        rows.append({key: contract.TRANSITION_REQUIRED.get(key, "1000") for key in contract.TRANSITION_CANCEL_FIELDS})
        return rows

    def transition_transcript(self, rows):
        return self.transcript("transition-budget", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def test_transition_budget_requires_every_actual_step_and_checkpoint_proof(self):
        name = base.experiments.CASES["transition-budget"]
        self.assertEqual(name, "TestNativeCHRLiveIdentityAuthTransitionCancel")
        for zero in (False, True):
            rows = self.transition_rows(zero)
            self.assertTrue(base.evidence.inspect_output(self.transition_transcript(rows), 0, name)[0])
        rows = self.transition_rows()
        for index, row in enumerate(rows):
            for field in row:
                bad = [dict(r) for r in rows]
                del bad[index][field]
                self.assertFalse(base.evidence.inspect_output(self.transition_transcript(bad), 0, name)[0], (index, field))
        for bad in (rows[1:], rows+[rows[-1]], rows[:5]+rows[6:], rows[:5]+list(reversed(rows[5:14]))+rows[14:]):
            self.assertFalse(base.evidence.inspect_output(self.transition_transcript(bad), 0, name)[0])
        good = self.transition_transcript(rows)
        self.assertFalse(base.evidence.inspect_output(good, 1, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])

    def test_transition_budget_rejects_history_gaps_mismatched_totals_or_scope(self):
        name = base.experiments.CASES["transition-budget"]
        for index, field, value in (
            (0, "transition_history_before", "1"), (1, "transition_history_after", "81"),
            (2, "transition_history_delta", "0"), (3, "transition_owner_preserved", "false"),
            (5, "transition_history_before", "2"), (6, "transition_kind", "set-entry"),
            (7, "transition_resource", "filter-rule"), (8, "transition_action", "set"),
            (14, "transition_activate_operations", "8"), (14, "transition_prepared_additions", "5"),
            (14, "transition_history_total_delta", "0"), (14, "transition_maximum_history", "7"),
            (15, "transition_forbidden_calls", "1"), (15, "transition_all_parameters_verified", "true"),
            (15, "transition_history_capacity_verified", "true"), (15, "transition_manual_cleanup", "true"),
            (15, "transition_old_credential_restored", "false"), (15, "transition_old_auth_reference_restored", "false"),
            (15, "transition_cancel_milliseconds", "240000"),
        ):
            rows = self.transition_rows()
            rows[index][field] = value
            self.assertFalse(base.evidence.inspect_output(self.transition_transcript(rows), 0, name)[0], field)
        # Each row is internally consistent, but it must also connect to the
        # previous actual owner's count rather than begin a new history chain.
        rows = self.transition_rows()
        rows[6].update(transition_history_before="1", transition_history_after="2", transition_history_delta="1")
        self.assertFalse(base.evidence.inspect_output(self.transition_transcript(rows), 0, name)[0])

    def test_transition_budget_has_closed_files_fields_codes_and_safe_failures(self):
        name = base.experiments.CASES["transition-budget"]
        for field, value in (("transition_native_id", "private-data"), ("transition_kind", "private-data"),
                             ("transition_stage", "other"), ("transition_history_after", "81"), ("code", "private-data")):
            _, safe = base.evidence.inspect_output(self.transition_transcript([{field: value}]), 1, name)
            self.assertFalse(any(field+"="+value in line for line in safe))
        for code in base.experiments.TRANSITION_CODES:
            verified, safe = base.evidence.inspect_output(self.transition_transcript([{"transition_update_stage": "false", "code": code}]), 1, name)
            self.assertFalse(verified)
            self.assertTrue(any("code="+code in line for line in safe))
        good = self.transition_transcript(self.transition_rows())
        self.assertFalse(base.evidence.inspect_output(good.replace(b"native_transition_budget_test.go", b"native_extensions_test.go"), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"transition_owner_preserved=true", b"transition_owner_preserved=true transition_owner_preserved=true", 1), 0, name)[0])

    def test_transition_budget_source_uses_only_closed_logs_and_original_budgets(self):
        source = (base.HERE.parents[2] / "internal/routeros/live/native_transition_budget_test.go").read_text().split("func TestNativeTransitionHistoryAndScopeGuards", 1)[0]
        name = base.experiments.CASES["transition-budget"]
        formats = re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source)
        self.assertGreater(len(formats), 8)
        for format in formats:
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=")
                if field == "%s":
                    self.assertEqual(value, "false")
                    continue
                if value == "%s":
                    value = {"transition_stage": "baseline", "transition_kind": "set-entry", "transition_resource": "filter-rule", "transition_action": "set", "code": "none"}[field]
                elif value == "%d": value = str(base.experiments.TRANSITION_NUMBERS[field][0])
                self.assertTrue(base.experiments.valid_case_field(name, field, value), field)
        for field in re.findall(r'nativeTransitionFailure\(t, "([a-z_]+)"', source):
            self.assertTrue(base.experiments.valid_case_field(name, field, "false"), field)
        for code in re.findall(r'fail\("(native-transition-[a-z-]+)"', source):
            self.assertTrue(base.experiments.valid_case_field(name, "code", code), code)
        self.assertIn("4*time.Minute", source)
        self.assertIn("600, 120, 15", source)
        self.assertIn("Timeout: 90 * time.Second", source)
        self.assertIn('const nativeTransitionIPv4Family = "ip"', source)
        self.assertNotIn('oldPeer.Config.Family != "ipv4"', source)

    def read_scale_rows(self):
        rows = [dict(base.experiments.READ_SCALE_REQUIRED)]
        for phase in base.experiments.READ_SCALE_PHASES:
            row = {key: "0" for key in base.experiments.READ_SCALE_NUMBERS | base.experiments.READ_SCALE_TIMES}
            row.update(read_scale_phase=phase, read_scale_milliseconds="1000")
            row["read_scale_api_writes"] = "2008" if phase == "setup" else "0"
            row["read_scale_inventory_calls"] = str({"scan-one": 1, "scan-two": 1, "observe": 2}.get(phase, 0))
            row["read_scale_presence_reads"] = str({"exact-rules": 6, "protected": 4}.get(phase, 0))
            rows.append(row)
        rows.append({"read_scale_total_milliseconds": "7000"})
        return rows

    def selection_scale_rows(self):
        rows = [dict(base.experiments.SELECTION_SCALE_REQUIRED)]
        for phase in base.experiments.SELECTION_SCALE_PHASES:
            row = {key: "0" for key in base.experiments.SELECTION_SCALE_COUNTS | base.experiments.SELECTION_SCALE_TIMES}
            row.update(selection_scale_phase=phase, selection_scale_milliseconds="1000")
            if phase == "setup": row["selection_scale_api_writes"] = "2008"
            elif phase == "toggle": row["selection_scale_api_writes"] = "2"
            elif phase.endswith("-getter"):
                row.update(selection_scale_inventory_calls="1", selection_scale_disabled_getters="2004", selection_scale_property_items="2006", selection_scale_property_batches="32")
            else:
                count = 2 if phase.endswith("-observe") else 1
                row.update(selection_scale_inventory_calls=str(count), selection_scale_property_items=str(2*count), selection_scale_property_batches=str(count),
                           selection_scale_predicate_calls=str(2*count), selection_scale_false_queries=str(count), selection_scale_true_queries=str(count),
                           selection_scale_false_ids_total=str(2005*count), selection_scale_true_ids_total=str(count))
            rows.append(row)
        for mode in ("initial", "toggled"):
            rows.append({"selection_scale_mode": mode, **{key: "true" for key in base.experiments.SELECTION_SCALE_PROOFS}})
        rows.append({"selection_scale_total_milliseconds": "9000"})
        return rows

    def selection_scale_transcript(self, rows):
        return self.transcript("selection-scale", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def test_selection_scale_requires_full_getter_comparison_and_both_observations(self):
        name = base.experiments.CASES["selection-scale"]
        rows = self.selection_scale_rows()
        self.assertEqual(name, "TestNativeCHRLiveDisabledSelectionScaleCompare")
        good = self.selection_scale_transcript(rows)
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for i, row in enumerate(rows):
            for key in row:
                missing = [dict(row) for row in rows]
                del missing[i][key]
                self.assertFalse(base.evidence.inspect_output(self.selection_scale_transcript(missing), 0, name)[0], key)
        for phase, field, value in (
            ("setup", "selection_scale_api_writes", "2007"),
            ("toggle", "selection_scale_api_writes", "1"),
            ("initial-getter", "selection_scale_disabled_getters", "0"),
            ("initial-getter", "selection_scale_predicate_calls", "2"),
            ("initial-selection", "selection_scale_disabled_getters", "1"),
            ("initial-selection", "selection_scale_false_ids_total", "2004"),
            ("initial-selection", "selection_scale_true_queries", "0"),
            ("toggled-observe", "selection_scale_inventory_calls", "1"),
            ("toggled-observe", "selection_scale_credential_reads", "1"),
            ("toggled-observe", "selection_scale_api_writes", "1"),
        ):
            changed = [dict(row) for row in rows]
            next(row for row in changed if row.get("selection_scale_phase") == phase)[field] = value
            self.assertFalse(base.evidence.inspect_output(self.selection_scale_transcript(changed), 0, name)[0])
        for key in base.experiments.SELECTION_SCALE_PROOFS:
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"=true").encode(), (key+"=false").encode()), 0, name)[0], key)
        self.assertFalse(base.evidence.inspect_output(good.replace(b"selection_scale_gates_unchanged=true", b"selection_scale_gates_unchanged=false"), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"selection_scale_publication_verified=false", b"selection_scale_publication_verified=true"), 0, name)[0])
        for field, value in (("selection_scale_unreviewed", "private"), ("selection_scale_phase", "private"), ("selection_scale_milliseconds", "300001"), ("code", "unbounded-private")):
            _, safe = base.evidence.inspect_output(self.selection_scale_transcript([{field: value}]), 1, name)
            self.assertFalse(any(field+"="+value in row for row in safe))

    def test_selection_scale_source_has_fixed_proof_fields_and_no_owner_operations(self):
        source = (base.HERE.parents[2]/"internal/routeros/live/native_selection_scale_test.go").read_text().split("func TestNativeSelectionScaleGuard", 1)[0]
        name = base.experiments.CASES["selection-scale"]
        for operation in (".Stage(", ".Take(", ".Confirm(", ".Cancel(", "Action: api.Remove"):
            self.assertNotIn(operation, source)
        self.assertIn("ReadFilterRuleDisabled", source)
        for format in re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source):
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=")
                if value == "%s":
                    enums = {"selection_scale_phase": "setup", "selection_scale_mode": "initial"}
                    self.assertIn(field, enums)
                    value = enums[field]
                elif value == "%d":
                    value = "0"
                self.assertTrue(base.experiments.valid_case_field(name, field, value), field)

    def read_scale_transcript(self, rows):
        return self.transcript("read-scale", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def test_read_scale_requires_actual_reads_and_no_measurement_writes(self):
        name = base.experiments.CASES["read-scale"]
        rows = self.read_scale_rows()
        self.assertEqual(name, "TestNativeCHRLiveLargeReadPerformance")
        self.assertTrue(base.evidence.inspect_output(self.read_scale_transcript(rows), 0, name)[0])
        for i, row in enumerate(rows):
            for key in row:
                changed = [dict(row) for row in rows]
                del changed[i][key]
                self.assertFalse(base.evidence.inspect_output(self.read_scale_transcript(changed), 0, name)[0], key)
        for phase, field, value in (
            ("setup", "read_scale_api_writes", "2007"),
            ("scan-one", "read_scale_api_writes", "1"),
            ("scan-two", "read_scale_inventory_calls", "0"),
            ("observe", "read_scale_inventory_calls", "1"),
            ("observe", "read_scale_credential_reads", "1"),
            ("exact-rules", "read_scale_presence_reads", "5"),
            ("protected", "read_scale_presence_reads", "3"),
            ("protected", "read_scale_getter_milliseconds", "1001"),
        ):
            changed = [dict(row) for row in rows]
            next(row for row in changed if row.get("read_scale_phase") == phase)[field] = value
            self.assertFalse(base.evidence.inspect_output(self.read_scale_transcript(changed), 0, name)[0])
        for key, value in base.experiments.READ_SCALE_REQUIRED.items():
            changed = [dict(row) for row in rows]
            changed[0][key] = "false" if value == "true" else "true" if value == "false" else str(int(value)+1)
            self.assertFalse(base.evidence.inspect_output(self.read_scale_transcript(changed), 0, name)[0])
        for changed in (rows+[rows[1]], rows[:1]+list(reversed(rows[1:-1]))+rows[-1:], rows[:-1]+[{"read_scale_total_milliseconds": "240001"}]):
            self.assertFalse(base.evidence.inspect_output(self.read_scale_transcript(changed), 0, name)[0])

    def test_read_scale_exact_files_fields_and_read_only_fixture_source(self):
        name = base.experiments.CASES["read-scale"]
        good = self.read_scale_transcript(self.read_scale_rows())
        for unknown in ({"read_scale_private": "secret"}, {"read_scale_api_resource": "private-name"}, {"read_scale_milliseconds": "300001"}, {"removal_scale_batch_rules": "2004"}):
            verified, safe = base.evidence.inspect_output(self.read_scale_transcript(self.read_scale_rows()+[unknown]), 0, name)
            self.assertFalse(verified)
            key, value = next(iter(unknown.items()))
            self.assertFalse(any(key+"="+value in row for row in safe))
        for filename in ("native_removal_scale_test.go", "native_read_scale_other.go"):
            self.assertFalse(base.evidence.inspect_output(good.replace(b"native_read_scale_test.go", filename.encode()), 0, name)[0])
        source = (base.HERE.parents[2]/"internal/routeros/live/native_read_scale_test.go").read_text()
        for mutation in (".Stage(", ".Take(", ".Confirm(", ".Cancel(", "Action: api.Remove"):
            self.assertNotIn(mutation, source)
        for format in re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source):
            for word in format.split():
                field, separator, value = word.partition("=")
                # Ordinary guard tests intentionally use human-readable test
                # failures; the native selector uses only structured fields.
                if not separator:
                    continue
                if field == "%s":
                    continue
                if value == "%s":
                    enums = {"read_scale_phase": "setup", "read_scale_api_resource": "filter-rule", "api_code": "none", "deadline_kind": "none", "limit_kind": "none", "code": "none"}
                    self.assertIn(field, enums)
                    value = enums[field]
                elif value == "%d":
                    value = "0"
                elif value == "%t":
                    value = "false"
                self.assertTrue(base.experiments.valid_case_field(name, field, value), field)

    def test_from_variant_requires_full_menu_exclusion_and_two_completed_absences(self):
        name = base.experiments.CASES["read-scale-from"]
        rows = self.read_scale_rows()
        rows[0] = dict(base.experiments.READ_SCALE_FROM_REQUIRED)
        absent = {key: "0" for key in base.experiments.READ_SCALE_NUMBERS | base.experiments.READ_SCALE_TIMES}
        absent.update(read_scale_phase="absent", read_scale_milliseconds="1", read_scale_presence_reads="2")
        rows.insert(-1, absent)
        def transcript(values):
            return self.transcript("read-scale-from", [" ".join(key+"="+value for key, value in row.items()) for row in values])
        good = transcript(rows)
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for field in ("read_scale_from_variant", "read_scale_missing_id_excluded", "read_scale_absence_verified", "read_scale_absent_queries"):
            missing = [dict(row) for row in rows]
            del missing[0][field]
            self.assertFalse(base.evidence.inspect_output(transcript(missing), 0, name)[0])
        for field, changed in (("read_scale_absent_queries", "1"), ("read_scale_missing_id_excluded", "false"), ("read_scale_absence_verified", "false")):
            changed_rows = [dict(row) for row in rows]
            changed_rows[0][field] = changed
            self.assertFalse(base.evidence.inspect_output(transcript(changed_rows), 0, name)[0])
        for field, changed in (("read_scale_presence_reads", "1"), ("read_scale_api_writes", "1"), ("read_scale_property_items", "1")):
            changed_rows = [dict(row) for row in rows]
            changed_rows[-2][field] = changed
            self.assertFalse(base.evidence.inspect_output(transcript(changed_rows), 0, name)[0])
        for code in ("command-failed", "transport-failed", "invalid-protocol"):
            self.assertFalse(base.evidence.inspect_output(transcript(rows+[{"api_code": code}]), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(self.read_scale_transcript(self.read_scale_rows()), 0, name)[0])
        baseline = base.experiments.CASES["read-scale"]
        self.assertFalse(base.evidence.inspect_output(good.replace(name.encode(), baseline.encode()), 0, baseline)[0])

    def test_id_only_query_proves_boolean_presence_without_object_attributes(self):
        name = base.experiments.CASES["presence-id-scale"]
        rows = [dict(base.experiments.PRESENCE_ID_REQUIRED)]
        for phase in base.experiments.PRESENCE_ID_PHASES:
            row = {key: "0" for key in base.experiments.READ_SCALE_NUMBERS | base.experiments.READ_SCALE_TIMES}
            row.update(read_scale_phase=phase, read_scale_milliseconds="1000")
            row["read_scale_api_writes"] = "2008" if phase == "setup" else "0"
            row["read_scale_inventory_calls"] = "1" if phase == "inventory" else "0"
            row["read_scale_presence_reads"] = str({"existing": 3, "absent": 2}.get(phase, 0))
            rows.append(row)
        rows.append({"read_scale_total_milliseconds": "5000"})
        def transcript(values):
            return self.transcript("presence-id-scale", [" ".join(key+"="+value for key, value in row.items()) for row in values])
        good = transcript(rows)
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for i, row in enumerate(rows):
            for key in row:
                missing = [dict(row) for row in rows]
                del missing[i][key]
                self.assertFalse(base.evidence.inspect_output(transcript(missing), 0, name)[0], key)
        for key, value in base.experiments.PRESENCE_ID_REQUIRED.items():
            changed = [dict(row) for row in rows]
            changed[0][key] = "false" if value == "true" else "true" if value == "false" else str(int(value)+1)
            self.assertFalse(base.evidence.inspect_output(transcript(changed), 0, name)[0])
        for phase, field, value in (("inventory", "read_scale_inventory_calls", "0"), ("existing", "read_scale_presence_reads", "2"), ("absent", "read_scale_presence_reads", "1"), ("absent", "read_scale_api_writes", "1"), ("existing", "read_scale_property_items", "1"), ("existing", "read_scale_credential_reads", "1")):
            changed = [dict(row) for row in rows]
            next(row for row in changed if row.get("read_scale_phase") == phase)[field] = value
            self.assertFalse(base.evidence.inspect_output(transcript(changed), 0, name)[0])
        for extra in ({"api_code": "command-failed"}, {"api_code": "transport-failed"}, {"presence_id_private": "private"}, {"read_scale_from_variant": "true"}):
            self.assertFalse(base.evidence.inspect_output(transcript(rows+[extra]), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])

    def discard1000_rows(self, single_rule=0):
        rows = [{key: value for key, value in base.experiments.DISCARD1000_REQUIRED.items()}, {
            "removal_scale_generation_rules": "2004", "removal_scale_prepare_rule_acks": "2004",
            "removal_scale_add_total": "2009", "removal_scale_add_unique_ids": "2009",
            "removal_scale_remove_attempts": "2009", "removal_scale_remove_acks": "2009",
            "removal_scale_remove_unique_ids": "2009", "removal_scale_forbidden_calls": "0",
            "removal_scale_batches": "9", "removal_scale_batch_rules": str(2004-single_rule),
            "removal_scale_single_removals": str(5+single_rule), "removal_scale_single_rule_removals": str(single_rule),
        }, {"extension_batch_runtime": "true", "code": "none", "batch_acknowledged": "2004"}]
        for phase in base.experiments.DISCARD1000_PHASES:
            row = {field: "0" for field in base.experiments.DISCARD1000_METRICS}
            row.update(removal_scale_phase=phase, removal_scale_milliseconds="1000", removal_scale_inventory_calls="2")
            if phase == "stage":
                row.update(removal_scale_api_writes="2011", removal_scale_api_adds="2009", removal_scale_ssh_owned_writes="4", removal_scale_ssh_takes="1")
            if phase == "discard":
                row.update(removal_scale_api_writes="2009", removal_scale_api_removes="2009", removal_scale_presence_reads="24048")
            if phase in ("stage", "checkpoint", "fresh", "discard"):
                row.update(removal_scale_false_queries="2", removal_scale_true_queries="2", removal_scale_false_ids_total="4012", removal_scale_selection_observations="1")
            rows.append(row)
        for index, timing in enumerate(base.experiments.DISCARD1000_TIMINGS[:-1], 1):
            rows.append({"removal_scale_timing": timing, "removal_scale_timing_milliseconds": "10",
                         "removal_scale_elapsed_milliseconds": str(index*10), "removal_scale_remaining_milliseconds": str(540000-index*10),
                         "removal_scale_context_done": "false", "removal_scale_context_deadline": "false"})
        completed = 0
        for sequence in range(1, 10):
            prefix = 224 if sequence < 9 else 212-single_rule
            for event in ("enter", "exit"):
                total = completed if event == "enter" else completed+prefix
                elapsed = 100 + sequence*20 + (10 if event == "exit" else 0)
                rows.append({"removal_scale_batch_event": event, "removal_scale_batch_sequence": str(sequence),
                             "removal_scale_batch_requested": "256" if sequence < 9 else "212",
                             "removal_scale_batch_completed": "0" if event == "enter" else str(prefix),
                             "removal_scale_batch_total_completed": str(total), "removal_scale_batch_milliseconds": "0" if event == "enter" else "10",
                             "removal_scale_elapsed_milliseconds": str(elapsed), "removal_scale_remaining_milliseconds": str(540000-elapsed),
                             "removal_scale_batch_error": "false"})
            completed += prefix
        rows.append({"removal_scale_timing": "total", "removal_scale_timing_milliseconds": "7140",
                     "removal_scale_elapsed_milliseconds": "7140", "removal_scale_remaining_milliseconds": "532860",
                     "removal_scale_context_done": "false", "removal_scale_context_deadline": "false",
                     "removal_scale_accounted_milliseconds": "7040", "removal_scale_unaccounted_milliseconds": "100"})
        return rows

    def discard1000_transcript(self, rows):
        return self.transcript("discard1000", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def global_tail1000_rows(self, single_rule=0):
        rows = self.discard1000_rows(single_rule)
        rows[0].update(base.experiments.DISCARD1000_GLOBAL_REQUIRED)
        # Count fields have one authoritative row, separate from boolean
        # declarations; ACK and observed deletion are independent values.
        for field in base.experiments.DISCARD1000_GLOBAL_COUNTS:
            rows[0].pop(field, None)
        rows[1].update(removal_scale_batch_rule_acks=str(2004-single_rule), removal_scale_absent_callbacks="0",
                       removal_scale_full_tail_reads="9", removal_scale_pending_guards="9",
                       removal_scale_formal_completed="2009", removal_scale_formal_total="2009")
        discard = next(row for row in rows if row.get("removal_scale_phase") == "discard")
        discard.update(removal_scale_object_reads=str(2*(2004-single_rule)), removal_scale_presence_reads="0")
        out = []
        previous = 0
        for row in rows:
            if row.get("removal_scale_batch_event") == "enter":
                if previous == 0:
                    out.append({"removal_scale_budget_event": "start", "removal_scale_discard_budget_milliseconds": "420000",
                                "removal_scale_discard_elapsed_milliseconds": "0", "removal_scale_discard_budget_remaining_milliseconds": "420000",
                                "removal_scale_lifecycle_remaining_milliseconds": "539900"})
                enter = dict(row)
                enter.update(removal_scale_batch_acknowledged="0", removal_scale_batch_total_acknowledged=str(previous),
                             removal_scale_batch_verified_tail="false", removal_scale_batch_pending="false")
                out.append(enter)
            elif row.get("removal_scale_batch_event") == "exit":
                prefix = int(row["removal_scale_batch_completed"])
                exit = dict(row)
                exit.update(removal_scale_batch_completed="0", removal_scale_batch_total_completed=str(previous),
                            removal_scale_batch_acknowledged=str(prefix), removal_scale_batch_total_acknowledged=str(previous+prefix),
                            removal_scale_batch_verified_tail="false", removal_scale_batch_pending="true")
                out.append(exit)
                tail = dict(row)
                tail.update(removal_scale_batch_event="tail", removal_scale_batch_acknowledged=str(prefix),
                            removal_scale_batch_total_acknowledged=str(previous+prefix), removal_scale_batch_verified_tail="true", removal_scale_batch_pending="false",
                            removal_scale_batch_milliseconds=str(int(row["removal_scale_batch_milliseconds"])+10),
                            removal_scale_elapsed_milliseconds=str(int(row["removal_scale_elapsed_milliseconds"])+10),
                            removal_scale_remaining_milliseconds=str(int(row["removal_scale_remaining_milliseconds"])-10))
                out.append(tail)
                previous += prefix
            else:
                if row.get("removal_scale_timing") == "total":
                    out.append({"removal_scale_budget_event": "end", "removal_scale_discard_budget_milliseconds": "420000",
                                "removal_scale_discard_elapsed_milliseconds": "1000", "removal_scale_discard_budget_remaining_milliseconds": "419000",
                                "removal_scale_lifecycle_remaining_milliseconds": "538900"})
                out.append(row)
                if "removal_scale_phase" in row:
                    out.append({"removal_scale_phase_budget": row["removal_scale_phase"], "removal_scale_phase_remaining_milliseconds": "500000",
                                "removal_scale_phase_context_done": "false", "removal_scale_phase_context_deadline": "false"})
        for row in out:
            if "removal_scale_batch_event" in row:
                elapsed = int(row["removal_scale_elapsed_milliseconds"])
                life, own = 540000-elapsed, 420000-(elapsed-100)
                row["removal_scale_lifecycle_remaining_milliseconds"] = str(life)
                row["removal_scale_discard_budget_remaining_milliseconds"] = str(own)
                row["removal_scale_remaining_milliseconds"] = str(min(life, own, 30000 if row["removal_scale_batch_event"] == "tail" else 600000))
        return out

    def global_tail1000_transcript(self, rows):
        return self.transcript("discard1000-global-tail", [" ".join(key+"="+value for key, value in row.items()) for row in rows])

    def test_global_tail1000_distinguishes_ack_tail_and_formal_completion(self):
        name = base.experiments.CASES["discard1000-global-tail"]
        self.assertEqual(name, "TestNativeCHRLiveRuleRemoval1000GlobalTail")
        for single in (0, 1):
            self.assertTrue(base.evidence.inspect_output(self.global_tail1000_transcript(self.global_tail1000_rows(single)), 0, name)[0])
        rows = self.global_tail1000_rows()
        for i, row in enumerate(rows):
            for field in row:
                missing = [dict(row) for row in rows]
                del missing[i][field]
                self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(missing), 0, name)[0], field)
        for field, value in (("removal_scale_batch_rule_acks", "2003"), ("removal_scale_absent_callbacks", "1"),
                             ("removal_scale_full_tail_reads", "8"), ("removal_scale_pending_guards", "8"),
                             ("removal_scale_formal_completed", "2008"), ("removal_scale_formal_total", "2008")):
            changed = [dict(row) for row in rows]
            changed[1][field] = value
            self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(changed), 0, name)[0], field)
        for event, field, value in (
            ("exit", "removal_scale_batch_completed", "224"),
            ("exit", "removal_scale_batch_total_completed", "224"),
            ("exit", "removal_scale_batch_verified_tail", "true"),
            ("exit", "removal_scale_batch_pending", "false"),
            ("tail", "removal_scale_batch_verified_tail", "false"),
            ("tail", "removal_scale_batch_pending", "true"),
            ("tail", "removal_scale_batch_completed", "223"),
            ("tail", "removal_scale_batch_acknowledged", "223"),
        ):
            changed = [dict(row) for row in rows]
            next(row for row in changed if row.get("removal_scale_batch_event") == event)[field] = value
            self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(changed), 0, name)[0], field)
        without_tail = [row for row in rows if row.get("removal_scale_batch_event") != "tail"]
        self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(without_tail), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(self.discard1000_rows()), 0, name)[0])
        legacy = base.experiments.CASES["discard1000"]
        self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(rows).replace(name.encode(), legacy.encode()), 0, legacy)[0])

    def test_global_tail1000_reports_parent_lifetime_separately_from_discard_budget(self):
        name = base.experiments.CASES["discard1000-global-tail"]
        diagnostic = {"removal_scale_budget_event": "end", "removal_scale_discard_budget_milliseconds": "420000",
                      "removal_scale_discard_elapsed_milliseconds": "190000", "removal_scale_discard_budget_remaining_milliseconds": "230000",
                      "removal_scale_lifecycle_remaining_milliseconds": "0"}
        self.assertTrue(base.experiments.valid_case_row(name, diagnostic))
        raw = self.global_tail1000_transcript([diagnostic])
        verified, safe = base.evidence.inspect_output(raw, 1, name)
        self.assertFalse(verified)
        self.assertTrue(any("removal_scale_discard_budget_remaining_milliseconds=230000" in row and "removal_scale_lifecycle_remaining_milliseconds=0" in row for row in safe))
        inconsistent = dict(diagnostic, removal_scale_discard_budget_remaining_milliseconds="0")
        self.assertFalse(base.experiments.valid_case_row(name, inconsistent))
        rows = self.global_tail1000_rows()
        enter = next(row for row in rows if row.get("removal_scale_batch_event") == "enter")
        enter["removal_scale_remaining_milliseconds"] = "540000"
        self.assertFalse(base.evidence.inspect_output(self.global_tail1000_transcript(rows), 0, name)[0])

    def test_discard1000_requires_all_proofs_metrics_and_exact_removal_counts(self):
        name = base.experiments.CASES["discard1000"]
        self.assertEqual(name, "TestNativeCHRLiveRuleRemoval1000Discard")
        for single in (0, 1):
            self.assertTrue(base.evidence.inspect_output(self.discard1000_transcript(self.discard1000_rows(single)), 0, name)[0])
        rows = self.discard1000_rows()
        good = self.discard1000_transcript(rows)
        for i, row in enumerate(rows):
            for key in row:
                with self.subTest(row=i, missing=key):
                    modified = [dict(value) for value in rows]
                    del modified[i][key]
                    self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(modified), 0, name)[0])
        for key, value in base.experiments.DISCARD1000_REQUIRED.items():
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"="+value).encode(), (key+"="+("false" if value == "true" else "true")).encode()), 0, name)[0])
        for key, value in (
            ("removal_scale_generation_rules", "2003"), ("removal_scale_prepare_rule_acks", "2003"),
            ("removal_scale_add_total", "2010"), ("removal_scale_add_unique_ids", "2008"),
            ("removal_scale_remove_attempts", "2008"), ("removal_scale_remove_acks", "2008"),
            ("removal_scale_remove_unique_ids", "2008"), ("removal_scale_forbidden_calls", "1"),
            ("removal_scale_batches", "0"), ("removal_scale_batch_rules", "2003"),
            ("removal_scale_batches", "1"), ("removal_scale_batches", "2005"),
            ("removal_scale_single_removals", "6"), ("removal_scale_single_rule_removals", "2"),
        ):
            modified = [dict(row) for row in rows]
            modified[1][key] = value
            self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(modified), 0, name)[0], key)
        for invalid in (good.replace(b"--- PASS:", b"--- SKIP:"), good.replace(name.encode(), (name+"Extra").encode()), good.replace(b"=== RUN", b"=== SKIP")):
            self.assertFalse(base.evidence.inspect_output(invalid, 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good, 1, name)[0])

    def test_discard1000_rejects_duplicate_out_of_order_and_impossible_phase_io(self):
        name = base.experiments.CASES["discard1000"]
        rows = self.discard1000_rows()
        mutations = [rows+[rows[1]], rows+[rows[-1]], rows[:3]+list(reversed(rows[3:]))]
        for phase, field, value in (
            ("discard", "removal_scale_presence_reads", "8015"),
            ("discard", "removal_scale_api_removes", "2008"),
            ("discard", "removal_scale_ssh_owned_writes", "1"),
            ("fresh", "removal_scale_ssh_takes", "1"),
            ("final", "removal_scale_ssh_releases", "1"),
            ("checkpoint", "removal_scale_api_writes", "1"),
            ("stage", "removal_scale_api_writes", "2008"),
            ("stage", "removal_scale_api_writes", "2009"),
            ("stage", "removal_scale_api_writes", "2010"),
            ("stage", "removal_scale_api_writes", "2012"),
            ("stage", "removal_scale_ssh_owned_writes", "0"),
            ("baseline", "removal_scale_property_batches", "1"),
            ("baseline", "removal_scale_inventory_calls", "0"),
            ("baseline", "removal_scale_milliseconds", "540000"),
        ):
            modified = [dict(row) for row in rows]
            next(row for row in modified if row.get("removal_scale_phase") == phase)[field] = value
            mutations.append(modified)
        for modified in mutations:
            self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(modified), 0, name)[0])

    def test_discard1000_accepts_both_existing_read_paths_with_absence_evidence(self):
        name = base.experiments.CASES["discard1000"]
        for direct in (False, True):
            rows = self.discard1000_rows()
            discard = next(row for row in rows if row.get("removal_scale_phase") == "discard")
            discard["removal_scale_object_reads"] = "4008" if direct else "0"
            discard["removal_scale_presence_reads"] = "4008" if direct else "8016"
            self.assertTrue(base.evidence.inspect_output(self.discard1000_transcript(rows), 0, name)[0])
            for missing in (["removal_scale_object_reads", "removal_scale_presence_reads"] if direct else ["removal_scale_presence_reads"]):
                changed = [dict(row) for row in rows]
                row = next(row for row in changed if row.get("removal_scale_phase") == "discard")
                row[missing] = str(int(row[missing])-1)
                self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(changed), 0, name)[0])

    def test_discard1000_requires_actual_selection_path_and_complete_query_counters(self):
        for global_tail in (False, True):
            make = self.global_tail1000_rows if global_tail else self.discard1000_rows
            transcript = self.global_tail1000_transcript if global_tail else self.discard1000_transcript
            name = base.experiments.CASES["discard1000-global-tail" if global_tail else "discard1000"]
            for phase, field, value in (
                ("stage", "selection_observations", "0"), ("discard", "selection_observations", "0"),
                ("checkpoint", "false_queries", "1"), ("fresh", "true_queries", "1"),
                ("fresh", "false_ids_total", "4011"), ("fresh", "true_ids_total", "1"),
                ("checkpoint", "disabled_getters", "1"), ("final", "false_queries", "1"),
            ):
                rows = make()
                row = next(row for row in rows if row.get("removal_scale_phase") == phase)
                row["removal_scale_"+field] = value
                self.assertFalse(base.evidence.inspect_output(transcript(rows), 0, name)[0], (global_tail, phase, field))
            rows = make()
            row = next(row for row in rows if row.get("removal_scale_phase") == "discard")
            # Per-object reads remain actual getters and are not falsely
            # reported as part of the bulk full-observation optimization.
            row.update(removal_scale_property_items="4", removal_scale_disabled_getters="4")
            self.assertTrue(base.evidence.inspect_output(transcript(rows), 0, name)[0])
            del row["removal_scale_true_queries"]
            self.assertFalse(base.evidence.inspect_output(transcript(rows), 0, name)[0])

    def test_discard1000_whitelist_redacts_unknown_fields_values_files_and_codes(self):
        name = base.experiments.CASES["discard1000"]
        rows = self.discard1000_rows()
        good = self.discard1000_transcript(rows)
        for field, values in (
            ("removal_scale_phase", ("private-device-data",)),
            ("removal_scale_adapter_code", ("rule-removal-private-device-data",)),
            ("removal_scale_inventory_calls", ("-1", "01", "1.0", "1000001", "1e3", "9"*5000)),
            ("removal_scale_milliseconds", ("540001", "-1")),
            ("removal_scale_remaining_milliseconds", ("540001", "-1")),
            ("removal_scale_timing", ("private-device-data",)),
            ("removal_scale_batch_event", ("private-device-data",)),
            ("removal_scale_batch_requested", ("0", "257")),
            ("removal_scale_elapsed_milliseconds", ("600001",)),
            ("removal_scale_unreviewed", ("private-device-data",)),
            ("discard_original_baseline_restored", ("true",)),
            ("probe_secret", ("private-device-data",)),
        ):
            for value in values:
                raw = self.discard1000_transcript(rows+[{field: value}])
                verified, safe = base.evidence.inspect_output(raw, 0, name)
                self.assertFalse(verified, (field, value))
                self.assertNotIn(field+"="+value, "\n".join(safe))
        for filename in ("native_discard_test.go", "native_removal_scale_private.go", "native_rollback_review_test.go"):
            verified, safe = base.evidence.inspect_output(good.replace(b"native_removal_scale_test.go", filename.encode()), 0, name)
            self.assertFalse(verified)
            self.assertFalse(any("removal_scale_" in line for line in safe))
        for code in ("rule-removal-session-lost", "rule-removal-context-ended", "journal-rule-removal-readback-digest", "native-removal-presence-reader-unavailable"):
            self.assertTrue(base.experiments.valid_case_field(name, "removal_scale_adapter_code", code))
            verified, safe = base.evidence.inspect_output(self.discard1000_transcript(rows+[{"removal_scale_adapter_code": code}]), 1, name)
            self.assertFalse(verified)
            self.assertTrue(any("removal_scale_adapter_code="+code in line for line in safe))

    def test_discard1000_native_source_has_only_explicit_reviewed_log_fields(self):
        source = (base.HERE.parents[2] / "internal/routeros/live/native_removal_scale_test.go").read_text().split("type nativeRemovalScaleGuardRecorder", 1)[0]
        name = base.experiments.CASES["discard1000"]
        global_name = base.experiments.CASES["discard1000-global-tail"]
        formats = re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source)
        self.assertGreater(len(formats), 10)
        fields = set()
        for format in formats:
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=")
                if field == "%s":
                    # The failure helper's caller fields are checked below.
                    self.assertEqual(value, "false")
                    continue
                fields.add(field)
                if value == "%s":
                    enums = {"removal_scale_phase": "baseline", "removal_scale_phase_budget": "baseline", "removal_scale_timing": "first-open", "removal_scale_batch_event": "enter", "removal_scale_budget_event": "start", "code": "none", "removal_scale_adapter_code": "none"}
                    self.assertIn(field, enums)
                    value = enums[field]
                elif value == "%d":
                    self.assertIn(field, base.experiments.NUMBERS)
                    value = str(base.experiments.NUMBERS[field][0])
                elif value == "%t":
                    value = "false"
                self.assertTrue(base.experiments.valid_case_field(name, field, value) or base.experiments.valid_case_field(global_name, field, value), field)
        for field in re.findall(r'nativeRemovalScaleFailure\(t, "([a-z_]+)"', source):
            fields.add(field)
            self.assertTrue(base.experiments.valid_case_field(name, field, "false"), field)
        self.assertTrue(base.experiments.DISCARD1000_REQUIRED.keys() <= fields)

    def test_discard1000_diagnostics_cannot_hide_missing_prefix_or_time(self):
        name = base.experiments.CASES["discard1000"]
        for field, changed in (
            ("removal_scale_accounted_milliseconds", "7050"),
            ("removal_scale_unaccounted_milliseconds", "99"),
            ("removal_scale_context_deadline", "true"),
        ):
            rows = self.discard1000_rows()
            rows[-1][field] = changed
            self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(rows), 0, name)[0])
        for event, field, changed in (
            ("enter", "removal_scale_batch_completed", "1"),
            ("enter", "removal_scale_batch_total_completed", "1"),
            ("exit", "removal_scale_batch_completed", "225"),
            ("exit", "removal_scale_batch_total_completed", "225"),
            ("exit", "removal_scale_batch_sequence", "2"),
            ("exit", "removal_scale_batch_requested", "255"),
            ("exit", "removal_scale_remaining_milliseconds", "540000"),
            ("exit", "removal_scale_elapsed_milliseconds", "1"),
            ("exit", "removal_scale_batch_error", "true"),
        ):
            rows = self.discard1000_rows()
            row = next(row for row in rows if row.get("removal_scale_batch_event") == event)
            row[field] = changed
            self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(rows), 0, name)[0])
        rows = self.discard1000_rows()
        first_exit = next(row for row in rows if row.get("removal_scale_batch_event") == "exit")
        rows.append(first_exit)
        self.assertFalse(base.evidence.inspect_output(self.discard1000_transcript(rows), 0, name)[0])
        # Partial progress plus a classified failure is useful diagnostic
        # output, but it can never substitute for complete native proof.
        partial = dict(first_exit, removal_scale_batch_error="true")
        verified, safe = base.evidence.inspect_output(self.discard1000_transcript([partial]), 1, name)
        self.assertFalse(verified)
        self.assertTrue(any("removal_scale_batch_error=true" in row for row in safe))
        rows = self.discard1000_rows()
        rows[-1].update(removal_scale_timing_milliseconds="540001", removal_scale_elapsed_milliseconds="540001",
                        removal_scale_unaccounted_milliseconds="532961", removal_scale_remaining_milliseconds="0",
                        removal_scale_context_done="true", removal_scale_context_deadline="true")
        verified, safe = base.evidence.inspect_output(self.discard1000_transcript(rows), 0, name)
        self.assertFalse(verified)
        self.assertTrue(any("removal_scale_elapsed_milliseconds=540001" in row for row in safe))

    def test_discard1000_getter_deadline_metadata_is_closed_and_consistent(self):
        name = base.experiments.CASES["discard1000"]
        diagnostic = {"extension_getter_resource": "filter-rule", "extension_getter_field": "disabled",
                      "api_code": "transport-failed", "getter_count": "100", "getter_milliseconds": "132",
                      "after_batch_milliseconds": "0", "getter_context_done": "false", "getter_context_deadline": "false",
                      "reply_bytes": "30", "reply_records": "1", "reply_sentences": "1", "limit_kind": "none",
                      "deadline": "true", "deadline_kind": "command"}
        verified, safe = base.evidence.inspect_output(self.discard1000_transcript([diagnostic]), 1, name)
        self.assertFalse(verified)
        self.assertTrue(any("deadline_kind=command" in row and "getter_context_done=false" in row for row in safe))
        cancelled = dict(diagnostic, api_code="cancelled-or-deadline", deadline="false", getter_context_done="true")
        self.assertTrue(base.experiments.valid_case_row(name, cancelled))
        empty_eof = dict(diagnostic, deadline="false", deadline_kind="none", reply_bytes="0", reply_records="0", reply_sentences="1")
        self.assertTrue(base.experiments.valid_case_row(name, empty_eof))
        for field in diagnostic:
            missing = dict(diagnostic)
            del missing[field]
            self.assertFalse(base.experiments.valid_case_row(name, missing), field)
        for field, changed in (("deadline", "false"), ("deadline_kind", "none"), ("reply_records", "2"), ("reply_bytes", "0"), ("getter_context_deadline", "true")):
            inconsistent = dict(diagnostic)
            inconsistent[field] = changed
            self.assertFalse(base.experiments.valid_case_row(name, inconsistent), field)
        for field, changed in (("deadline_kind", "private-device-data"), ("reply_bytes", "999999999"), ("extension_getter_field", "tcp-md5-key")):
            invalid = dict(diagnostic)
            invalid[field] = changed
            _, safe = base.evidence.inspect_output(self.discard1000_transcript([invalid]), 1, name)
            self.assertFalse(any(field+"="+changed in row for row in safe))

    def test_discard_requires_exact_scope_restore_and_new_bootstrap(self):
        name = base.experiments.CASES["discard"]
        proof = " ".join(key+"="+value for key, value in base.experiments.DISCARD_REQUIRED.items())
        counts = "discard_original_additions=17 discard_remove_attempts=17 discard_remove_acknowledged=17"
        good = self.transcript("discard", [proof, counts])
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for key, value in base.experiments.DISCARD_REQUIRED.items():
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"="+value).encode(), b""), 0, name)[0])
        for original, changed in (
            (b"discard_native_forbidden_calls=0", b"discard_native_forbidden_calls=1"),
            (b"discard_large_scale_verified=false", b"discard_large_scale_verified=true"),
            (b"discard_remove_attempts=17", b"discard_remove_attempts=18"),
            (b"discard_remove_acknowledged=17", b"discard_remove_acknowledged=16"),
            (b"discard_original_additions=17", b"discard_original_additions=0"),
            (b"--- PASS:", b"--- SKIP:"),
        ):
            self.assertFalse(base.evidence.inspect_output(good.replace(original, changed), 0, name)[0])
        bad = self.transcript("discard", [proof, counts, "discard_adapter_code=private-device-data"])
        result, safe = base.evidence.inspect_output(bad, 0, name)
        self.assertFalse(result)
        self.assertNotIn("private-device-data", "\n".join(safe))

    def bounded_discard_rows(self):
        e = base.experiments
        rows = []
        for number in range(1,6):
            rows.append({"bounded_discard_request":str(number),"bounded_discard_limit":"2" if number==1 else "1", "bounded_discard_completed":str(6+number), "bounded_discard_total":"11", "bounded_discard_pending":"true" if number<5 else "false", "bounded_discard_complete":"false" if number<5 else "true", "bounded_discard_return_verified":"true"})
            if number==1: rows.append({key:e.BOUNDED_DISCARD_REQUIRED[key] for key in e.BOUNDED_DISCARD_RECOVERY})
        rows.append({key:e.BOUNDED_DISCARD_REQUIRED[key] for key in e.BOUNDED_DISCARD_FINAL})
        rows += [dict(e.GLOBAL_TAIL_REQUIRED), {"discard_original_additions":"11","discard_remove_attempts":"11","discard_remove_acknowledged":"11","global_tail_batches":"1","global_tail_verified_full_reads":"1","global_tail_pending_guards":"1"}]
        return rows

    def bounded_discard_transcript(self,rows):
        return self.transcript("discard-bounded",[" ".join(key+"="+value for key,value in row.items()) for row in rows])

    def test_bounded_discard_requires_each_verified_return_and_readonly_recovery(self):
        name=base.experiments.CASES["discard-bounded"]
        rows=self.bounded_discard_rows()
        self.assertTrue(base.evidence.inspect_output(self.bounded_discard_transcript(rows),0,name)[0])
        for index,row in enumerate(rows):
            for field in row:
                bad=[dict(r) for r in rows];del bad[index][field]
                self.assertFalse(base.evidence.inspect_output(self.bounded_discard_transcript(bad),0,name)[0],field)
        for index,field,value in ((0,"bounded_discard_complete","true"),(0,"bounded_discard_completed","8"),(1,"bounded_discard_recovery_mutations","1"),(1,"bounded_discard_automatic_continuation","true"),(2,"bounded_discard_limit","2"),(5,"bounded_discard_complete","false"),(6,"bounded_discard_large_scale_verified","true"),(8,"global_tail_verified_full_reads","0")):
            bad=[dict(r) for r in rows];bad[index][field]=value
            self.assertFalse(base.evidence.inspect_output(self.bounded_discard_transcript(bad),0,name)[0],field)
        for bad in (rows+[rows[0]], rows[1:], [rows[1],rows[0]]+rows[2:]):
            self.assertFalse(base.evidence.inspect_output(self.bounded_discard_transcript(bad),0,name)[0])

    def test_bounded_discard_source_and_output_are_closed(self):
        name=base.experiments.CASES["discard-bounded"]
        source=(base.HERE.parents[2]/"internal/routeros/live/native_bounded_discard_test.go").read_text()
        for text in re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"',source):
            for word in text.split():
                field,separator,value=word.partition("=")
                self.assertEqual(separator,"=")
                if value=="%d":value=str(base.experiments.NUMBERS[field][0])
                if value=="%t":value="false"
                self.assertTrue(base.experiments.valid_case_field(name,field,value),field)
        for field in re.findall(r'nativeDiscardFailure\(t, "([a-z_]+)"',source):
            self.assertTrue(base.experiments.valid_case_field(name,field,"false"),field)
        for field,value in (("bounded_discard_native_id","private-data"),("bounded_discard_request","6"),("code","discard-private-data")):
            _,safe=base.evidence.inspect_output(self.bounded_discard_transcript([{field:value}]),1,name)
            self.assertFalse(any(field+"="+value in line for line in safe))
        for field in base.experiments.BOUNDED_DISCARD_FAILURES:
            passed,safe=base.evidence.inspect_output(self.bounded_discard_transcript([{field:"false"}]),1,name)
            self.assertFalse(passed)
            self.assertTrue(any(field+"=false" in line for line in safe),field)
        good=self.bounded_discard_transcript(self.bounded_discard_rows())
        self.assertFalse(base.evidence.inspect_output(good.replace(b"native_bounded_discard_test.go",b"native_extensions_test.go"),0,name)[0])

    def test_global_tail_cycle_requires_real_tail_after_every_ack_batch(self):
        name = base.experiments.CASES["discard-global-tail"]
        required = base.experiments.GLOBAL_TAIL_REQUIRED
        self.assertEqual(name, "TestNativeCHRLivePreparedDiscardGlobalTailCycle")
        proof = " ".join(key+"="+value for key, value in required.items())
        counts = "discard_original_additions=11 discard_remove_attempts=11 discard_remove_acknowledged=11 global_tail_batches=1 global_tail_verified_full_reads=1 global_tail_pending_guards=1"
        good = self.transcript("discard-global-tail", [proof, counts])
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for key, value in required.items():
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"="+value).encode(), b""), 0, name)[0], key)
        for word in counts.split():
            self.assertFalse(base.evidence.inspect_output(good.replace(word.encode(), b""), 0, name)[0], word)
        for old, new in (
            ("global_tail_rule_acks=6", "global_tail_rule_acks=5"),
            ("global_tail_batches=1", "global_tail_batches=0"),
            ("global_tail_verified_full_reads=1", "global_tail_verified_full_reads=0"),
            ("global_tail_pending_guards=1", "global_tail_pending_guards=0"),
            ("global_tail_absent_callbacks=0", "global_tail_absent_callbacks=1"),
            ("discard_remove_attempts=11", "discard_remove_attempts=10"),
            ("discard_remove_acknowledged=11", "discard_remove_acknowledged=10"),
            ("global_tail_large_scale_verified=false", "global_tail_large_scale_verified=true"),
            ("discard_original_baseline_restored=true", "discard_original_baseline_restored=false"),
        ):
            self.assertFalse(base.evidence.inspect_output(good.replace(old.encode(), new.encode()), 0, name)[0], old)
        self.assertFalse(base.evidence.inspect_output(self.transcript("discard-global-tail", [proof, counts, "global_tail_batches=1"]), 0, name)[0])
        for field in ("global_tail_private", "removal_scale_batch_rules"):
            raw = self.transcript("discard-global-tail", [proof, counts, field+"=private-data"])
            verified, safe = base.evidence.inspect_output(raw, 0, name)
            self.assertFalse(verified)
            self.assertNotIn("private-data", "\n".join(safe))
        self.assertFalse(base.evidence.inspect_output(good.replace(b"native_global_tail_test.go", b"native_extensions_test.go"), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])
        self.assertFalse(base.evidence.inspect_output(good, 1, name)[0])
        for code in ("rule-removal-proof-mode-invalid", "rule-removal-absence-not-permitted", "rule-removal-tail-unverified", "rule-removal-acknowledged-target-present"):
            self.assertTrue(base.experiments.valid_case_field(name, "discard_adapter_code", code))
            self.assertFalse(base.evidence.inspect_output(self.transcript("discard-global-tail", [proof, counts, "discard_adapter_code="+code]), 0, name)[0])

    def test_global_tail_native_logs_are_fixed_fields_and_bounded_counts(self):
        name = base.experiments.CASES["discard-global-tail"]
        source = (base.HERE.parents[2]/"internal/routeros/live/native_global_tail_test.go").read_text().split("func TestNativeGlobalTailFixtureRejects", 1)[0]
        for format in re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source):
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=")
                if value == "%d":
                    self.assertIn(field, base.experiments.GLOBAL_TAIL_COUNTS)
                    value = "0"
                self.assertTrue(base.experiments.valid_case_field(name, field, value), field)

    def test_presence_cannot_replace_native_absence_with_error_or_broader_claim(self):
        name = base.experiments.CASES["presence"]
        required = base.experiments.PRESENCE_REQUIRED
        proof = " ".join(key+"="+value for key, value in required.items())
        good = self.transcript("presence", [proof])
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for key, value in required.items():
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"="+value).encode(), b""), 0, name)[0])
        for original, changed in ((b"presence_absent_queries=2", b"presence_absent_queries=1"), (b"presence_batch_gc_verified=false", b"presence_batch_gc_verified=true"), (b"presence_object_exact=true", b"presence_object_exact=false")):
            self.assertFalse(base.evidence.inspect_output(good.replace(original, changed), 0, name)[0])

    def test_rollback_review_must_be_fresh_readonly_and_reuse_preparation(self):
        name = base.experiments.CASES["review-reuse"]
        required = base.experiments.REVIEW_REUSE_REQUIRED
        proof = " ".join(key+"="+value for key,value in required.items())
        good = self.transcript("review-reuse", [proof])
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for key,value in required.items():
            self.assertFalse(base.evidence.inspect_output(good.replace((key+"="+value).encode(), b""), 0, name)[0])
        for original,changed in ((b"review_reuse_second_add_rules=0",b"review_reuse_second_add_rules=1"),(b"review_reuse_review_forbidden_calls=0",b"review_reuse_review_forbidden_calls=1"),(b"review_reuse_large_scale_verified=false",b"review_reuse_large_scale_verified=true"),(b"review_reuse_old_confirm_rejected=true",b"review_reuse_old_confirm_rejected=false")):
            self.assertFalse(base.evidence.inspect_output(good.replace(original,changed), 0,name)[0])

    def test_delayed_owner_stream_cannot_claim_early_or_stale_rollback(self):
        name = base.experiments.CASES["owned-delay"]
        lines = ["=== RUN   "+name,
                 "    native_owned_order_test.go:438: owned_order_before_release_still_owned=true baseline_observed=true rollback_claimed=false",
                 "    native_owned_order_test.go:472: owned_order_data_before_eof=true owner_released=true checkpoint_restored=true late_write_observed=false api_activation_write=false",
                 "--- PASS: "+name+" (7.00s)", "PASS"]
        good = "\n".join(lines).encode()
        self.assertTrue(base.evidence.inspect_output(good, 0, name)[0])
        for expected, forbidden in ((b"rollback_claimed=false", b"rollback_claimed=true"), (b"late_write_observed=false", b"late_write_observed=true"), (b"owned_order_data_before_eof=true", b""), (b"owner_released=true", b"owner_released=false")):
            self.assertFalse(base.evidence.inspect_output(good.replace(expected, forbidden), 0, name)[0])


if __name__ == "__main__":
    unittest.main()
