"""Pre-cleanup child accounting contracts; no QEMU, VM or budget changes."""
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
import unittest
from types import SimpleNamespace
from unittest import mock

import test_harness as base

resources = base.guest_resources


def proc_stat(pid=123, state="Z", user=35900, system=100, comm="private-name (with) spaces\nand-secret"):
    fields = [state] + ["0"]*49
    fields[11], fields[12] = str(user), str(system)
    return (str(pid)+" ("+comm+") "+" ".join(fields)+"\n").encode()


class FakeProcess:
    def __init__(self, result, events, cached=None):
        self.pid, self.result, self.events, self.returncode = 123, result, events, cached
        self._waitpid_lock = threading.Lock()

    def poll(self):
        raise AssertionError("resource observer must not call poll")

    def wait4(self, pid, flags):
        if (pid, flags) != (self.pid, os.WNOHANG) or not self._waitpid_lock.locked():
            raise AssertionError("wait4 must hold the owned child lock")
        self.events.append("wait4")
        if self.result is None:
            return 0, 0, None
        return pid, self.result << 8 if self.result >= 0 else -self.result, None


class GuestResourceContract(unittest.TestCase):
    def test_zombie_stat_is_read_before_wait4_and_comm_never_leaves_parser(self):
        events = []
        process = FakeProcess(-9, events)
        def reader(pid, name):
            self.assertEqual(pid, 123)
            events.append(name)
            return proc_stat() if name == "stat" else b"Max cpu time 360 360 seconds\n"
        row = resources.observe(process, reader=reader, clock_ticks=100, waiter=process.wait4)
        self.assertEqual(events, ["stat", "limits", "wait4"])
        self.assertEqual(row["guest_stat_state"], "zombie")
        self.assertEqual(row["guest_exit_state"], "signaled")
        self.assertEqual(row["guest_exit_signal"], 9)
        self.assertTrue(row["guest_exited_before_stop"])
        self.assertEqual(row["guest_cpu_total_ticks"], 36000)
        self.assertEqual(row["guest_cpu_ticks_per_second"], 100)
        self.assertTrue(row["guest_cpu_limit_matches"])
        safe = resources.format_observation(row)
        self.assertNotIn("private-name", safe)
        self.assertNotIn("secret", safe)
        self.assertNotIn("comm", safe)
        # Facts are emitted, not an invented cause such as CPU-limit-killed.
        self.assertNotIn("cause", safe)

    def test_running_and_already_reaped_children_are_distinct(self):
        for result, expected in ((None, "running"), (7, "exited"), (-24, "signaled")):
            process = FakeProcess(result, [])
            row = resources.observe(process, reader=lambda pid, name: proc_stat(state="S") if name == "stat" else b"Max cpu time 360 360 seconds\n", clock_ticks=100, waiter=process.wait4)
            self.assertEqual(row["guest_exit_state"], expected)
            self.assertEqual(row["guest_exited_before_stop"], result is not None)
            self.assertTrue(resources.valid_observation(row))
        events = []
        process = FakeProcess(-9, events, cached=-9)
        def forbidden_reader(*_):
            self.fail("an already-reaped PID may have been reused")
        row = resources.observe(process, reader=forbidden_reader)
        self.assertEqual(events, [])
        self.assertFalse(row["guest_cpu_observed"])
        self.assertEqual(row["guest_exit_signal"], 9)

    def test_malformed_missing_and_unbounded_proc_data_remain_unknown(self):
        bad = [proc_stat(pid=124), proc_stat(state="?"), proc_stat(user=-1), proc_stat(user=10**14), b"123 (private-data) Z", b"x"*(resources.MAX_PROC_BYTES+1)]
        for raw in bad:
            process = FakeProcess(None, [])
            row = resources.observe(process, reader=lambda pid, name: raw if name == "stat" else b"Max cpu time unlimited unlimited seconds\n", clock_ticks=100, waiter=process.wait4)
            self.assertFalse(row["guest_cpu_observed"])
            self.assertFalse(row["guest_cpu_limit_observed"])
            self.assertTrue(resources.valid_observation(row))
        def missing(*_):
            raise FileNotFoundError("private-path")
        process = FakeProcess(-9, [])
        row = resources.observe(process, reader=missing, waiter=process.wait4)
        self.assertFalse(row["guest_cpu_observed"])
        self.assertTrue(row["guest_exited_before_stop"])
        self.assertNotIn("private", resources.format_observation(row))

    def test_schema_only_accepts_closed_typed_bounded_facts(self):
        row = resources.empty_observation()
        self.assertTrue(resources.valid_observation(row))
        for key in row:
            missing = dict(row)
            del missing[key]
            self.assertFalse(resources.valid_observation(missing))
        for field, value in (("guest_environment", "secret"), ("guest_cpu_total_ticks", -1), ("guest_cpu_total_ticks", resources.MAX_TICKS+1), ("guest_cpu_ticks_per_second", True), ("guest_exit_signal", 65), ("guest_exit_state", "private-name"), ("guest_cpu_limit_seconds", 900), ("guest_cpu_observed", "true")):
            invalid = dict(row)
            invalid[field] = value
            self.assertFalse(resources.valid_observation(invalid), field)
            with self.assertRaises(ValueError):
                resources.format_observation(invalid)
        inconsistent = resources.empty_observation(True)
        inconsistent.update(guest_exit_state="running", guest_exit_signal=9)
        self.assertFalse(resources.valid_observation(inconsistent))
        for raw in (b"Max cpu time 361 360 seconds\n", b"Max cpu time 360 360 unknown\n", b"Max cpu time 360 360 seconds\nMax cpu time 360 360 seconds\n"):
            with self.assertRaises(ValueError):
                resources.parse_limits(raw)

    def test_report_errors_do_not_print_exception_or_prevent_cleanup(self):
        with mock.patch.object(base.transport.guest_resources, "observe", side_effect=RuntimeError("private-path")), mock.patch.object(base.transport, "emit") as output:
            base.transport.report_guest_resources(FakeProcess(None, []))
        self.assertEqual(output.call_count, 1)
        self.assertNotIn("private-path", output.call_args.args[0])
        self.assertIn("guest_exit_state=unknown", output.call_args.args[0])
        source = (base.HERE/"transport.py").read_text().rsplit("    finally:\n", 1)[1]
        self.assertLess(source.index("report_guest_resources(process, cpu_limit(options.native_case))"), source.index("stop_process(process)"))
        self.assertLess(source.index("report_guest_resources(process, cpu_limit(options.native_case))"), source.index("stopped.set()"))
        self.assertIn("guest_resources.py", base.runner.SOURCE_FILES)
        self.assertEqual(base.bootstrap.cpu_limit(None), 360)
        for case in base.experiments.LONG_CASES:
            self.assertEqual(base.bootstrap.cpu_limit(case), 600)
        self.assertEqual(base.bootstrap.cpu_limit("cleanup1000-confirm"), 960)
        self.assertEqual(resources.CPU_LIMIT_SECONDS, 360)

    def test_only_closed_declared_cpu_budgets_are_accepted(self):
        for expected in (360, 600, 960):
            empty = resources.empty_observation(expected_cpu_limit=expected)
            self.assertEqual(empty["guest_cpu_limit_seconds"], expected)
            self.assertTrue(resources.valid_observation(empty))
            for actual in (360, 600, 960):
                process = FakeProcess(None, [])
                row = resources.observe(process, reader=lambda pid, name: proc_stat(state="S") if name == "stat" else f"Max cpu time {actual} {actual} seconds\n".encode(), waiter=process.wait4, expected_cpu_limit=expected)
                self.assertEqual(row["guest_cpu_limit_seconds"], expected)
                self.assertEqual(row["guest_cpu_limit_matches"], expected == actual)
                self.assertTrue(resources.valid_observation(row))
        for invalid in (0, 359, 361, 420, 599, 601, 900, 959, 961, True, 360.0, "600", None):
            with self.assertRaises(ValueError):
                resources.empty_observation(expected_cpu_limit=invalid)
            with self.assertRaises(ValueError):
                resources.observe(None, expected_cpu_limit=invalid)
            row = resources.empty_observation()
            row["guest_cpu_limit_seconds"] = invalid
            self.assertFalse(resources.valid_observation(row))
        with mock.patch.object(base.transport.guest_resources, "observe", side_effect=RuntimeError("private")), mock.patch.object(base.transport, "emit") as output:
            base.transport.report_guest_resources(None, expected_cpu_limit=960)
        self.assertIn("guest_cpu_limit_seconds=960", output.call_args.args[0])
        self.assertIn("guest_exit_state=unknown", output.call_args.args[0])

    def test_unknown_report_budget_cannot_escape_final_cleanup(self):
        with mock.patch.object(base.transport, "emit") as output:
            base.transport.report_guest_resources(None, expected_cpu_limit=961)
        output.assert_not_called()

    def wait_for_zombie(self, process, seconds=2):
        deadline = time.monotonic()+seconds
        while time.monotonic() < deadline:
            state, _, _ = resources.parse_stat(resources.read_proc(process.pid, "stat"), process.pid)
            if state == "zombie":
                return
            time.sleep(0.005)
        self.fail("short local child did not become a waitable zombie")

    def test_real_short_child_exit_is_sampled_before_reaping(self):
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", "import os; os._exit(7)"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        try:
            self.wait_for_zombie(process)
            row = resources.observe(process)
            self.assertEqual((row["guest_exit_state"], row["guest_exit_code"], row["guest_stat_state"]), ("exited", 7, "zombie"))
            self.assertTrue(row["guest_cpu_observed"])
            self.assertTrue(row["guest_rusage_observed"])
            self.assertGreater(row["guest_cpu_ticks_per_second"], 0)
            self.assertTrue(resources.valid_observation(row))
            self.assertEqual(process.returncode, 7)
            self.assertEqual(process.wait(timeout=0.1), 7)
        finally:
            base.transport.stop_process(process)

    def test_real_short_child_signal_is_not_confused_with_harness_stop(self):
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", "import time; time.sleep(10)"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        try:
            os.kill(process.pid, signal.SIGTERM) # only this test's owned child
            self.wait_for_zombie(process)
            row = resources.observe(process)
            self.assertEqual((row["guest_exit_state"], row["guest_exit_signal"], row["guest_stat_state"]), ("signaled", signal.SIGTERM, "zombie"))
            self.assertTrue(row["guest_exited_before_stop"])
            self.assertTrue(row["guest_rusage_observed"])
            self.assertTrue(resources.valid_observation(row))
        finally:
            base.transport.stop_process(process)

    def test_wait4_accounting_is_independent_from_proc_and_reaped_once(self):
        events = []
        process = FakeProcess(-9, events)
        def reader(pid, name):
            events.append(name)
            return proc_stat(user=31248, system=2451) if name == "stat" else b"Max cpu time 360 360 seconds\n"
        def waiter(pid, flags):
            self.assertEqual((pid, flags), (123, os.WNOHANG))
            events.append("wait4")
            return pid, signal.SIGKILL, SimpleNamespace(ru_utime=339.125, ru_stime=20.875, ru_maxrss=800000)
        row = resources.observe(process, reader=reader, clock_ticks=100, waiter=waiter)
        self.assertEqual(events, ["stat", "limits", "wait4"])
        self.assertEqual(row["guest_cpu_total_ticks"], 33699)
        self.assertEqual(row["guest_cpu_total_microseconds"], 360000000)
        self.assertEqual(row["guest_max_rss_kib"], 800000)
        self.assertTrue(resources.valid_observation(row))
        self.assertEqual(process.returncode, -signal.SIGKILL)
        calls = []
        resources.observe(process, reader=lambda *_: calls.append("proc"), waiter=lambda *_: calls.append("wait"))
        self.assertEqual(calls, [])

    def test_wait4_running_and_missing_child_never_fabricate_accounting(self):
        reader = lambda pid, name: proc_stat(state="S") if name == "stat" else b"Max cpu time 360 360 seconds\n"
        row = resources.observe(FakeProcess(None, []), reader=reader, waiter=lambda *_: (0, 0, None))
        self.assertEqual(row["guest_exit_state"], "running")
        self.assertFalse(row["guest_rusage_observed"])
        def missing(*_):
            raise ChildProcessError("already reaped by another waiter")
        row = resources.observe(FakeProcess(0, []), reader=reader, waiter=missing)
        self.assertEqual(row["guest_exit_state"], "unknown")
        self.assertFalse(row["guest_exited_before_stop"])
        self.assertTrue(resources.valid_observation(row))

    def test_unknown_wait_outcomes_never_fall_back_to_poll(self):
        for outcome in (OSError("private-wait-error"), ValueError("private-status"), TypeError("private-type"), (456, 0, None)):
            process = FakeProcess(0, [])
            def waiter(*_):
                if isinstance(outcome, Exception):
                    raise outcome
                return outcome
            row = resources.observe(process, reader=lambda pid, name: proc_stat() if name == "stat" else b"Max cpu time 360 360 seconds\n", waiter=waiter)
            self.assertEqual(row["guest_exit_state"], "unknown")
            self.assertIsNone(process.returncode)
            self.assertTrue(resources.valid_observation(row))
            self.assertNotIn("private", resources.format_observation(row))
        for pid in (-1, 0, True, 2147483648):
            process = FakeProcess(0, [])
            process.pid = pid
            def forbidden(*_):
                self.fail("invalid PID reached a reader or waiter")
            row = resources.observe(process, reader=forbidden, waiter=forbidden)
            self.assertEqual(row["guest_exit_state"], "unknown")

    def test_busy_lock_released_before_return_does_not_allow_a_second_reaper(self):
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", "import os; os._exit(7)"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        original = process._waitpid_lock
        try:
            self.wait_for_zombie(process)
            original.acquire()
            class ReleaseBusyOnce:
                def acquire(self, blocking=False):
                    # Models a competing waiter reporting 'running' and then
                    # releasing its lock just before our failed try-lock returns.
                    original.release()
                    return False
                def release(self):
                    raise AssertionError("a lock not acquired must not be released")
            process._waitpid_lock = ReleaseBusyOnce()
            def forbidden_reader(*_):
                self.fail("a busy waiter must prevent proc sampling")
            with mock.patch.object(process, "poll", side_effect=AssertionError("fallback poll would lose rusage")):
                row = resources.observe(process, reader=forbidden_reader)
            self.assertEqual(row["guest_exit_state"], "unknown")
            self.assertFalse(row["guest_cpu_observed"])
            self.assertIsNone(process.returncode)
            process._waitpid_lock = original
            # The first call did not reap: final status and rusage are still
            # available to an uncontended, fully locked observation.
            final = resources.observe(process)
            self.assertEqual(final["guest_exit_code"], 7)
            self.assertTrue(final["guest_rusage_observed"])
        finally:
            process._waitpid_lock = original
            if original.locked():
                original.release()
            base.transport.stop_process(process)

    def test_proc_sampling_holds_the_same_lock_against_concurrent_poll(self):
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", "import os; os._exit(7)"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        sampling, polled = threading.Event(), threading.Event()
        results = []
        def competing_poll():
            if sampling.wait(2):
                results.append(process.poll())
            polled.set()
        competitor = threading.Thread(target=competing_poll, daemon=True)
        try:
            self.wait_for_zombie(process)
            competitor.start()
            def reader(pid, name):
                self.assertTrue(process._waitpid_lock.locked())
                if name == "stat":
                    sampling.set()
                    self.assertTrue(polled.wait(2), "competing poll must remain nonblocking")
                    self.assertEqual(results, [None])
                    self.assertIsNone(process.returncode)
                return resources.read_proc(pid, name)
            row = resources.observe(process, reader=reader)
            competitor.join(timeout=2)
            self.assertFalse(competitor.is_alive())
            self.assertEqual((row["guest_exit_state"], row["guest_exit_code"]), ("exited", 7))
            self.assertTrue(row["guest_cpu_observed"])
            self.assertTrue(row["guest_rusage_observed"])
            self.assertTrue(resources.valid_observation(row))
            self.assertEqual(process.wait(timeout=0.1), 7)
        finally:
            sampling.set()
            if competitor.ident is not None:
                competitor.join(timeout=2)
            base.transport.stop_process(process)

    def test_real_multithreaded_child_final_accounting(self):
        code = "import threading,time,os\ndef work():\n end=time.thread_time()+0.12\n while time.thread_time()<end: pass\nthreads=[threading.Thread(target=work) for _ in range(3)]\n[t.start() for t in threads]\n[t.join() for t in threads]\nos._exit(0)"
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", code], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        try:
            self.wait_for_zombie(process, 10)
            row = resources.observe(process)
            self.assertEqual(row["guest_exit_state"], "exited")
            self.assertTrue(row["guest_rusage_observed"])
            self.assertGreaterEqual(row["guest_cpu_total_microseconds"], 300000)
            self.assertTrue(resources.valid_observation(row))
            self.assertEqual(process.wait(timeout=0.1), 0)
        finally:
            base.transport.stop_process(process)

    def test_real_owned_cpu_limit_signal_has_final_usage(self):
        code = "import resource\nresource.setrlimit(resource.RLIMIT_CPU,(1,1))\nwhile True: pass"
        process = subprocess.Popen([sys.executable, "-I", "-S", "-c", code], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, close_fds=True)
        try:
            self.wait_for_zombie(process, 10)
            row = resources.observe(process)
            self.assertEqual(row["guest_exit_signal"], signal.SIGKILL)
            self.assertEqual((row["guest_cpu_soft_limit_seconds"], row["guest_cpu_hard_limit_seconds"]), (1, 1))
            self.assertFalse(row["guest_cpu_limit_matches"])
            self.assertTrue(row["guest_rusage_observed"])
            self.assertGreaterEqual(row["guest_cpu_total_microseconds"], 800000)
            self.assertLess(row["guest_cpu_total_microseconds"], 3000000)
            self.assertTrue(resources.valid_observation(row))
        finally:
            base.transport.stop_process(process)


if __name__ == "__main__":
    unittest.main()
