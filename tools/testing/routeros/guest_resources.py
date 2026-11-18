"""Bounded pre-cleanup guest facts. Never return process names or arguments."""
import os
import re
import math
import subprocess

from large_cleanup import CPU_SECONDS as LARGE_CLEANUP_CPU_SECONDS

CPU_LIMIT_SECONDS = 360  # default fixture budget; explicit long cases may use 600
EXPECTED_CPU_LIMITS = frozenset((CPU_LIMIT_SECONDS, 600, LARGE_CLEANUP_CPU_SECONDS))
MAX_PROC_BYTES = 8192
MAX_TICKS = 1000000000000
STATES = {
    b"R": "running", b"S": "sleeping", b"D": "disk-sleep", b"T": "stopped",
    b"t": "tracing-stop", b"Z": "zombie", b"X": "dead", b"x": "dead", b"I": "idle",
}
BOOLEANS = frozenset(("guest_process_present", "guest_exited_before_stop", "guest_cpu_observed", "guest_cpu_limit_observed", "guest_cpu_limit_matches", "guest_rusage_observed"))
NUMBERS = {
    "guest_exit_code": (0, 255), "guest_exit_signal": (0, 64),
    "guest_cpu_user_ticks": (0, MAX_TICKS), "guest_cpu_system_ticks": (0, MAX_TICKS),
    "guest_cpu_total_ticks": (0, MAX_TICKS), "guest_cpu_ticks_per_second": (0, 1000000),
    "guest_cpu_limit_seconds": (min(EXPECTED_CPU_LIMITS), max(EXPECTED_CPU_LIMITS)),
    "guest_cpu_soft_limit_seconds": (0, 86400), "guest_cpu_hard_limit_seconds": (0, 86400),
    "guest_cpu_user_microseconds": (0, 86400000000), "guest_cpu_system_microseconds": (0, 86400000000),
    "guest_cpu_total_microseconds": (0, 86400000000), "guest_max_rss_kib": (0, 16777216),
}
ENUMS = {"guest_resource_phase": frozenset(("before-stop",)),
         "guest_exit_state": frozenset(("running", "exited", "signaled", "unknown")),
         "guest_stat_state": frozenset((*STATES.values(), "unknown"))}
FIELDS = BOOLEANS | NUMBERS.keys() | ENUMS.keys()
NO_CHILD = object()
CHILD_RUNNING = object()
CHILD_REAPED = object()
WAIT_UNKNOWN = object()


def empty_observation(present=False, expected_cpu_limit=CPU_LIMIT_SECONDS):
    if type(expected_cpu_limit) is not int or expected_cpu_limit not in EXPECTED_CPU_LIMITS:
        raise ValueError("guest-cpu-limit-selection")
    return {
        "guest_resource_phase": "before-stop", "guest_process_present": bool(present),
        "guest_exited_before_stop": False, "guest_exit_state": "unknown", "guest_exit_code": 0, "guest_exit_signal": 0,
        "guest_stat_state": "unknown", "guest_cpu_observed": False,
        "guest_cpu_user_ticks": 0, "guest_cpu_system_ticks": 0, "guest_cpu_total_ticks": 0, "guest_cpu_ticks_per_second": 0,
        "guest_cpu_limit_seconds": expected_cpu_limit, "guest_cpu_limit_observed": False,
        "guest_cpu_soft_limit_seconds": 0, "guest_cpu_hard_limit_seconds": 0, "guest_cpu_limit_matches": False,
        "guest_rusage_observed": False, "guest_cpu_user_microseconds": 0, "guest_cpu_system_microseconds": 0,
        "guest_cpu_total_microseconds": 0, "guest_max_rss_kib": 0,
    }


def bounded_integer(raw, maximum):
    if len(raw) > len(str(maximum)) or not re.fullmatch(rb"[0-9]+", raw):
        raise ValueError("bounded-integer")
    value = int(raw)
    if value > maximum:
        raise ValueError("bounded-integer")
    return value


def parse_stat(raw, pid):
    if not isinstance(raw, bytes) or len(raw) > MAX_PROC_BYTES:
        raise ValueError("stat-bounds")
    prefix = str(pid).encode("ascii") + b" ("
    # comm may itself contain spaces, parentheses, or newlines. Ignore it
    # entirely; only the fixed numeric fields after its final ')' are used.
    end = raw.rfind(b")")
    if not raw.startswith(prefix) or end < len(prefix) or raw[end+1:end+2] != b" ":
        raise ValueError("stat-shape")
    fields = raw[end+2:].split()
    if len(fields) < 20 or fields[0] not in STATES:
        raise ValueError("stat-shape")
    user = bounded_integer(fields[11], MAX_TICKS)  # proc stat field 14
    system = bounded_integer(fields[12], MAX_TICKS)  # field 15
    if user + system > MAX_TICKS:
        raise ValueError("stat-bounds")
    return STATES[fields[0]], user, system


def parse_limits(raw):
    if not isinstance(raw, bytes) or len(raw) > MAX_PROC_BYTES:
        raise ValueError("limits-bounds")
    rows = [line for line in raw.splitlines() if line.startswith(b"Max cpu time")]
    if len(rows) != 1:
        raise ValueError("limits-shape")
    match = re.fullmatch(rb"Max cpu time\s+([0-9]+)\s+([0-9]+)\s+seconds\s*", rows[0])
    if not match:
        raise ValueError("limits-unavailable")
    soft, hard = (bounded_integer(value, 86400) for value in match.groups())
    if soft > hard:
        raise ValueError("limits-shape")
    return soft, hard


def read_proc(pid, name):
    if name not in ("stat", "limits"):
        raise ValueError("proc-selection")
    with open("/proc/" + str(pid) + "/" + name, "rb") as source:
        return source.read(MAX_PROC_BYTES+1)


def reap_usage_locked(process, waiter):
    """The caller owns Popen's waitpid lock through sampling and this wait.

    Unknown outcomes never authorize a fallback poll: poll would discard final
    accounting and can manufacture exit zero when another reaper caused ECHILD.
    """
    if waiter is None:
        return WAIT_UNKNOWN, None
    try:
        pid, status, usage = waiter(process.pid, os.WNOHANG)
        if pid == 0:
            return CHILD_RUNNING, None
        if pid != process.pid:
            return WAIT_UNKNOWN, None
        process.returncode = os.waitstatus_to_exitcode(status)
        return CHILD_REAPED, usage
    except ChildProcessError:
        return NO_CHILD, None
    except (OSError, ValueError, TypeError):
        return WAIT_UNKNOWN, None


def observe(process, reader=read_proc, clock_ticks=None, waiter=None, expected_cpu_limit=CPU_LIMIT_SECONDS):
    out = empty_observation(process is not None, expected_cpu_limit)
    if process is None:
        return out
    real = isinstance(process, subprocess.Popen)
    lock = getattr(process, "_waitpid_lock", None)
    if real and lock is None:
        return out
    if lock is not None and not lock.acquire(blocking=False):
        return out
    try:
        return observe_locked(out, process, reader, clock_ticks,
                              os.wait4 if real and waiter is None else waiter)
    finally:
        if lock is not None:
            lock.release()


def exit_observation(out, result):
    if type(result) is int and 0 <= result <= 255:
        out.update(guest_exit_state="exited", guest_exited_before_stop=True, guest_exit_code=result)
    elif type(result) is int and -64 <= result < 0:
        out.update(guest_exit_state="signaled", guest_exited_before_stop=True, guest_exit_signal=-result)
    return out


def observe_locked(out, process, reader, clock_ticks, waiter):
    # A Popen waiter cannot reap/recycle this PID during either proc read.
    # Cached status is read only after acquiring the same lock, and never
    # authorizes sampling a PID that may already belong to another process.
    if process.returncode is not None:
        return exit_observation(out, process.returncode)
    pid = process.pid
    if type(pid) is not int or not 0 < pid <= 2147483647:
        return out
    if waiter is not None:
        try:
            state, user, system = parse_stat(reader(pid, "stat"), pid)
            hz = os.sysconf("SC_CLK_TCK") if clock_ticks is None else clock_ticks
            if type(hz) is not int or not 1 <= hz <= 1000000:
                raise ValueError("clock-ticks")
            out.update(guest_stat_state=state, guest_cpu_observed=True, guest_cpu_user_ticks=user,
                       guest_cpu_system_ticks=system, guest_cpu_total_ticks=user+system, guest_cpu_ticks_per_second=hz)
        except (OSError, ValueError, TypeError):
            pass
        try:
            soft, hard = parse_limits(reader(pid, "limits"))
            out.update(guest_cpu_limit_observed=True, guest_cpu_soft_limit_seconds=soft, guest_cpu_hard_limit_seconds=hard,
                       guest_cpu_limit_matches=soft == hard == out["guest_cpu_limit_seconds"])
        except (OSError, ValueError, TypeError):
            pass
    state, usage = reap_usage_locked(process, waiter)
    if state is CHILD_RUNNING:
        out["guest_exit_state"] = "running"
        return out  # do not race a second waitpid that would lose final rusage
    if state is not CHILD_REAPED:
        return out  # lock/error/ECHILD unknown is never converted to cached exit 0
    if usage is not None:
        try:
            times = (usage.ru_utime, usage.ru_stime)
            if any(type(value) not in (int, float) or not math.isfinite(value) or not 0 <= value <= 86400 for value in times):
                raise ValueError("rusage-bound")
            user, system = (round(value*1000000) for value in times)
            rss = usage.ru_maxrss
            if user+system > 86400000000 or type(rss) is not int or not 0 <= rss <= 16777216:
                raise ValueError("rusage-bound")
            out.update(guest_rusage_observed=True, guest_cpu_user_microseconds=user,
                       guest_cpu_system_microseconds=system, guest_cpu_total_microseconds=user+system, guest_max_rss_kib=rss)
        except (AttributeError, ValueError, TypeError):
            pass
    return exit_observation(out, process.returncode)


def valid_observation(row):
    if type(row) is not dict or row.keys() != FIELDS:
        return False
    if any(type(row[field]) is not bool for field in BOOLEANS):
        return False
    if any(type(row[field]) is not int or not low <= row[field] <= high for field, (low, high) in NUMBERS.items()):
        return False
    if row["guest_cpu_limit_seconds"] not in EXPECTED_CPU_LIMITS:
        return False
    if any(type(row[field]) is not str or row[field] not in values for field, values in ENUMS.items()):
        return False
    kind = row["guest_exit_state"]
    if row["guest_exited_before_stop"] != (kind in ("exited", "signaled")):
        return False
    if kind != "exited" and row["guest_exit_code"] != 0 or (kind == "signaled") != (row["guest_exit_signal"] != 0):
        return False
    cpu_fields = ("guest_cpu_user_ticks", "guest_cpu_system_ticks", "guest_cpu_total_ticks", "guest_cpu_ticks_per_second")
    if row["guest_cpu_observed"]:
        if row["guest_stat_state"] == "unknown" or row["guest_cpu_ticks_per_second"] == 0 or row["guest_cpu_total_ticks"] != row["guest_cpu_user_ticks"] + row["guest_cpu_system_ticks"]:
            return False
    elif row["guest_stat_state"] != "unknown" or any(row[field] != 0 for field in cpu_fields):
        return False
    if row["guest_cpu_limit_observed"]:
        if row["guest_cpu_soft_limit_seconds"] > row["guest_cpu_hard_limit_seconds"]:
            return False
    elif row["guest_cpu_soft_limit_seconds"] != 0 or row["guest_cpu_hard_limit_seconds"] != 0:
        return False
    matches = row["guest_cpu_limit_observed"] and row["guest_cpu_soft_limit_seconds"] == row["guest_cpu_hard_limit_seconds"] == row["guest_cpu_limit_seconds"]
    if row["guest_cpu_limit_matches"] != matches:
        return False
    usage_fields = ("guest_cpu_user_microseconds", "guest_cpu_system_microseconds", "guest_cpu_total_microseconds", "guest_max_rss_kib")
    if row["guest_rusage_observed"]:
        if not row["guest_exited_before_stop"] or row["guest_cpu_total_microseconds"] != row["guest_cpu_user_microseconds"]+row["guest_cpu_system_microseconds"]:
            return False
    elif any(row[field] != 0 for field in usage_fields):
        return False
    if not row["guest_process_present"] and (kind != "unknown" or row["guest_cpu_observed"] or row["guest_cpu_limit_observed"] or row["guest_rusage_observed"]):
        return False
    return True


def format_observation(row):
    if not valid_observation(row):
        raise ValueError("guest-resource-schema")
    def value(item):
        return str(item).lower() if type(item) is bool else str(item)
    return "guest_resource " + " ".join(field + "=" + value(row[field]) for field in sorted(FIELDS))
