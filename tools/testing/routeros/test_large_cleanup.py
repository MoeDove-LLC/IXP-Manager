import unittest

import large_cleanup
from result import inspect_output


class LargeCleanupContract(unittest.TestCase):
    def transcript(self):
        rows = ["large_cleanup_prefixes=1000"]
        rows += [f"large_cleanup_phase={phase} large_cleanup_phase_milliseconds=100" for phase in ("a-stage", "a-confirm")]
        rows += ["large_cleanup_a_confirmed=true", "large_cleanup_a_prepare_acks=2004"]
        rows += [f"large_cleanup_phase={phase} large_cleanup_phase_milliseconds=100" for phase in ("b-stage", "b-confirm")]
        rows += ["large_cleanup_b_confirmed=true"]
        total, batch = 0, 0
        for i, acks in enumerate(([200], [200, 200], [200, 200], [200, 200], [200, 200], [204]), 1):
            for value in acks:
                batch += 1
                rows.append(f"large_cleanup_batch={batch} large_cleanup_batch_acks={value} large_cleanup_batch_milliseconds=30000")
            total += sum(acks)
            rows.append(f"large_cleanup_request={i} large_cleanup_request_limit={1 if i == 1 else 2} large_cleanup_committed={total} large_cleanup_ack_total={total} large_cleanup_request_milliseconds=80000")
            if i == 1:
                rows.append(f"large_cleanup_reopen=1 large_cleanup_recovered={total} large_cleanup_recovery_writes=0")
        rows += [f"large_cleanup_approved=2004 large_cleanup_acked=2004 large_cleanup_batches={batch} large_cleanup_global_tails={batch} large_cleanup_single_removals=0 large_cleanup_reopens=1 large_cleanup_final_strict=true large_cleanup_old_generation_absent=true large_cleanup_key_exact=true large_cleanup_review_verified=true", "large_cleanup_total_milliseconds=600000"]
        output = ["=== RUN   " + large_cleanup.TEST]
        output += ["    native_cleanup_scale_test.go:123: " + row for row in rows]
        output += [f"--- PASS: {large_cleanup.TEST} (600.00s)", "PASS", ""]
        return "\n".join(output).encode()

    def test_complete_proof_binds_exact_scale_rounds_and_no_recovery_writes(self):
        output = self.transcript()
        self.assertTrue(inspect_output(output, 0, large_cleanup.TEST)[0])
        for original, changed in (
            (b"large_cleanup_prefixes=1000", b"large_cleanup_prefixes=30"),
            (b"large_cleanup_a_confirmed=true", b"large_cleanup_a_confirmed=false"),
            (b"large_cleanup_a_prepare_acks=2004", b"large_cleanup_a_prepare_acks=2003"),
            (b"large_cleanup_recovered=200", b"large_cleanup_recovered=201"),
            (b"large_cleanup_recovery_writes=0", b"large_cleanup_recovery_writes=1"),
            (b"large_cleanup_global_tails=10", b"large_cleanup_global_tails=9"),
            (b"large_cleanup_committed=2004", b"large_cleanup_committed=2003"),
            (b"large_cleanup_batch_acks=204", b"large_cleanup_batch_acks=203"),
            (b"large_cleanup_old_generation_absent=true", b"large_cleanup_old_generation_absent=false"),
            (b"large_cleanup_key_exact=true", b"large_cleanup_key_exact=false"),
        ):
            with self.subTest(field=original):
                self.assertFalse(inspect_output(output.replace(original, changed), 0, large_cleanup.TEST)[0])

    def test_unknown_text_never_enters_a_native_transcript(self):
        changed = self.transcript().replace(b"large_cleanup_prefixes=1000", b"large_cleanup_config=unsafe-secret")
        verified, filtered = inspect_output(changed, 0, large_cleanup.TEST)
        self.assertFalse(verified)
        self.assertFalse(any("unsafe-secret" in row for row in filtered))


if __name__ == "__main__":
    unittest.main()
