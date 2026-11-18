"""Closed output contract for the 1,000-prefix confirmed cleanup fixture."""
import re

import live_schema


CASE = "cleanup1000-confirm"
TEST = "TestNativeCHRLiveLargeUpdateConfirmBoundedCleanup"
GO_SECONDS = 1260
WALL_SECONDS = 1440
CPU_SECONDS = 960

PHASES = ("a-stage", "a-confirm", "b-stage", "b-confirm")
FAILURES = frozenset((*PHASES, *"gates journal open-runtime target open-journal initial-observe build-a a-acks observe-a build-b targets cleanup-pending cleanup-request cleanup-boundary first-boundary close-journal close-runtime recover-read-only partial-review cleanup-incomplete final-strict exact-once old-generation-retained final-review".split()))
BOOLS = frozenset("a_confirmed b_confirmed final_strict old_generation_absent key_exact review_verified".split())
NUMBERS = {key: (0, 2004) for key in "a_prepare_acks batch_acks committed ack_total recovered approved acked single_removals".split()}
NUMBERS.update({key: (0, 32) for key in "batch request request_limit reopen batches global_tails reopens".split()})
NUMBERS.update({key: (0, 1260000) for key in "batch_milliseconds phase_milliseconds request_milliseconds total_milliseconds".split()})
NUMBERS.update({"prefixes": (1000, 1000), "recovery_writes": (0, 0)})


def valid_field(field, value):
    key = field.removeprefix("large_cleanup_")
    if key == field:
        return False
    if key in BOOLS:
        return value in ("true", "false")
    if key in NUMBERS:
        low, high = NUMBERS[key]
        return len(value) <= len(str(high)) and re.fullmatch(r"0|[1-9][0-9]*", value) is not None and low <= int(value) <= high
    if key == "phase":
        return value in PHASES
    if key == "failed":
        return value in FAILURES
    if key == "failure_code":
        return live_schema.valid_code(value) or value in ("native-large-cleanup-checkpoint-unverified", "native-large-cleanup-unreviewed-removal", "native-large-cleanup-wrong-batch-mode", "native-large-cleanup-single-absence-unexpected", "cleanup-awaiting-explicit-continuation")
    return False


def complete(rows):
    def values(key):
        field = "large_cleanup_" + key
        return [row[field] for row in rows if field in row]

    if values("failed") or values("failure_code"):
        return False
    fixed = {"prefixes": "1000", "a_prepare_acks": "2004", "approved": "2004", "acked": "2004", **{key: "true" for key in BOOLS}}
    if any(values(key) != [value] for key, value in fixed.items()) or values("phase") != list(PHASES) or len(values("total_milliseconds")) != 1:
        return False
    requests = [row for row in rows if "large_cleanup_request" in row]
    batches = [row for row in rows if "large_cleanup_batch" in row]
    reopens = [row for row in rows if "large_cleanup_reopen" in row]
    if len(requests) < 2 or len(batches) < 2 or not reopens:
        return False
    if values("batches") != [str(len(batches))] or values("global_tails") != values("batches") or values("reopens") != [str(len(reopens))] or len(values("single_removals")) != 1:
        return False
    prior = 0
    committed = []
    for index, row in enumerate(requests, 1):
        got = int(row.get("large_cleanup_committed", "0"))
        if row.get("large_cleanup_request") != str(index) or row.get("large_cleanup_request_limit") != ("1" if index == 1 else "2") or row.get("large_cleanup_ack_total") != str(got) or "large_cleanup_request_milliseconds" not in row or not prior < got <= 2004:
            return False
        committed.append(got)
        prior = got
    if prior != 2004 or reopens[0].get("large_cleanup_recovered") != str(committed[0]):
        return False
    for index, row in enumerate(reopens, 1):
        if row.get("large_cleanup_reopen") != str(index) or row.get("large_cleanup_recovery_writes") != "0" or int(row.get("large_cleanup_recovered", "0")) not in committed[:-1]:
            return False
    count = int(values("single_removals")[0])
    for index, row in enumerate(batches, 1):
        acks = int(row.get("large_cleanup_batch_acks", "0"))
        if row.get("large_cleanup_batch") != str(index) or not 1 <= acks <= 256 or "large_cleanup_batch_milliseconds" not in row:
            return False
        count += acks
    return count == 2004
