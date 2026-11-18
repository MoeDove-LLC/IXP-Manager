"""One fixed reset of the already-owned disposable QEMU process; no proxy API."""
import json
import os
from pathlib import Path
import select
import socket
import stat
import struct
import threading
import time

from console import FixtureFailure

TEST = "TestNativeCHRLiveDeviceResetRecovery"
RESTORE_TEST = "TestNativeCHRLiveDeviceResetRestore"
CASES = {"device-reset": TEST, "device-restore": RESTORE_TEST}
QMP_SOCKET = "/scratch/device-reset-qmp.sock"
RESET_SOCKET = "/scratch/device-reset.sock"
REQUIRED = frozenset("baseline_confirmed pending_owner_verified qmp_acknowledged fresh_identity_verified uptime_restarted old_confirmation_rejected owner_released recovery_proven read_only".split())
FAILURES = frozenset("fixture_enabled runtime empty initial_plan journal baseline_verified update_plan pending_snapshot_verified before_uptime_known old_runtime_closed observed_state".split())
OUTCOMES = frozenset(("checkpoint-restored", "candidate-retained", "unrecognized-state"))
RESTORE_REQUIRED = frozenset(("device_restore_review_verified", "device_restore_stage_verified", "device_restore_confirm_verified", "device_restore_journal_reopened"))
FAILURE_CODES = frozenset(("device-reset-qmp-binding", "device-reset-qmp-format", "device-reset-qmp-limit", "device-reset-qmp-timeout", "device-reset-qmp-error", "device-reset-qmp-order", "device-reset-request", "device-reset-client", "device-reset-console", "device-reset-unproven"))


def valid_field(field, value, restore=False):
    if restore and (field in RESTORE_REQUIRED or field == "device_restore_fresh_controller"):
        return value in ("true", "false")
    if field == "device_reset_outcome":
        return value in OUTCOMES
    return field.startswith("device_reset_") and field.removeprefix("device_reset_") in REQUIRED | FAILURES and value in ("true", "false")


def complete(rows, restore=False):
    values = {}
    for row in rows:
        for field, value in row.items():
            if field in values or not valid_field(field, value, restore) or value == "false":
                return False
            values[field] = value
    if restore and (values.get("device_reset_outcome") != "candidate-retained" or any(values.get(field) != "true" for field in RESTORE_REQUIRED)):
        return False
    return all(values.get("device_reset_" + field) == "true" for field in REQUIRED) and values.get("device_reset_outcome") in OUTCOMES


def safe_failure_code(error):
    return error.args[0] if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURE_CODES else "unclassified"


class DeviceReset:
    """The raw monitor pathname disappears before the Go process is started.

    Only this non-inherited connected QMP fd can issue a command. The visible
    0600 bridge accepts exactly reset-newline plus EOF, once, from that Go PID.
    It replies only after both QMP success and the matching host RESET event.
    """
    def __init__(self, process):
        self.process = process
        self.qmp = self.listener = self.client = None
        self.worker = self.drain = None
        self.stopped = threading.Event()
        self.finished = threading.Event()
        self.buffer = bytearray()
        self.bytes_read = 0
        self.failure = None
        self.completed = False
        self.reset_attempted = False
        self.client_pid = None
        self.socket_identity = None
        try:
            self._connect_qmp()
            self.listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.listener.bind(RESET_SOCKET)  # Never unlink an unrecognized leaf.
            os.chmod(RESET_SOCKET, 0o600)
            info = os.lstat(RESET_SOCKET)
            self.socket_identity = info.st_dev, info.st_ino
            self.listener.listen(1)
            self.listener.settimeout(0.2)
            # Serial boot output must not block the same QEMU during reset.
            # This reader discards bounded bytes; it never sends guest commands.
            self.drain = threading.Thread(target=self._drain_serial, daemon=True)
            self.drain.start()
        except Exception:
            self.close()
            raise

    def _connect_qmp(self):
        info = os.lstat(QMP_SOCKET)
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077 or self.process.returncode is not None:
            raise FixtureFailure("device-reset-qmp-binding")
        self.qmp = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.qmp.settimeout(5)
        self.qmp.connect(QMP_SOCKET)
        pid, uid, _ = struct.unpack("3i", self.qmp.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
        after = os.lstat(QMP_SOCKET)
        if pid != self.process.pid or uid != os.geteuid() or (info.st_dev, info.st_ino) != (after.st_dev, after.st_ino):
            raise FixtureFailure("device-reset-qmp-binding")
        greeting = self._message(time.monotonic() + 5)
        if not isinstance(greeting.get("QMP"), dict):
            raise FixtureFailure("device-reset-qmp-format")
        self.qmp.sendall(b'{"execute":"qmp_capabilities","id":"fixture-capabilities"}\n')
        answer = self._message(time.monotonic() + 5)
        if answer != {"return": {}, "id": "fixture-capabilities"}:
            raise FixtureFailure("device-reset-qmp-format")
        # No QMP listener is exposed to the target test. PEP446 and close_fds
        # also prevent inheritance of this private connected monitor fd.
        os.unlink(QMP_SOCKET)

    def _message(self, deadline):
        while b"\n" not in self.buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or self.stopped.is_set():
                raise FixtureFailure("device-reset-qmp-timeout")
            self.qmp.settimeout(remaining)
            try:
                chunk = self.qmp.recv(4096)
            except TimeoutError:
                raise FixtureFailure("device-reset-qmp-timeout") from None
            if not chunk:
                raise FixtureFailure("device-reset-qmp-format")
            self.bytes_read += len(chunk)
            self.buffer.extend(chunk)
            if self.bytes_read > 65536 or len(self.buffer) > 16384:
                raise FixtureFailure("device-reset-qmp-limit")
        line, _, rest = self.buffer.partition(b"\n")
        self.buffer[:] = rest
        try:
            value = json.loads(line)
        except (ValueError, UnicodeError):
            raise FixtureFailure("device-reset-qmp-format") from None
        if not isinstance(value, dict):
            raise FixtureFailure("device-reset-qmp-format")
        return value

    def _reset(self):
        if self.reset_attempted:
            raise FixtureFailure("device-reset-qmp-order")
        # Discard only pre-command events; a stale RESET cannot be its proof.
        for _ in range(32):
            if not self.buffer and not select.select([self.qmp], [], [], 0)[0]:
                break
            event = self._message(time.monotonic() + 0.5)
            if "event" not in event or event["event"] == "RESET":
                raise FixtureFailure("device-reset-qmp-order")
        else:
            raise FixtureFailure("device-reset-qmp-limit")
        self.reset_attempted = True  # Never repeat an uncertain send.
        self.qmp.sendall(b'{"execute":"system_reset","id":"fixture-device-reset"}\n')
        acknowledged = observed = False
        deadline = time.monotonic() + 5
        for _ in range(32):
            event = self._message(deadline)
            if "error" in event:
                raise FixtureFailure("device-reset-qmp-error")
            if "event" in event:
                if event["event"] == "RESET":
                    data = event.get("data")
                    if observed or not isinstance(data, dict) or data.get("guest") is not False or data.get("reason") != "host-qmp-system-reset":
                        raise FixtureFailure("device-reset-qmp-order")
                    observed = True
            elif event == {"return": {}, "id": "fixture-device-reset"} and not acknowledged:
                acknowledged = True
            else:
                raise FixtureFailure("device-reset-qmp-order")
            if acknowledged and observed:
                return
        raise FixtureFailure("device-reset-qmp-limit")

    def start(self, child):
        if self.worker is not None or child.returncode is not None or child.pid <= 0:
            raise FixtureFailure("device-reset-client")
        self.client_pid = child.pid
        self.worker = threading.Thread(target=self._serve, daemon=True)
        self.worker.start()

    def _serve(self):
        try:
            while not self.stopped.is_set():
                try:
                    self.client, _ = self.listener.accept()
                    break
                except TimeoutError:
                    continue
            if self.client is None:
                return
            self.listener.close()  # One connection only, even on invalid input.
            self.client.settimeout(5)
            pid, uid, _ = struct.unpack("3i", self.client.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
            if pid != self.client_pid or uid != os.geteuid():
                raise FixtureFailure("device-reset-client")
            request = bytearray()
            while len(request) <= 6:
                chunk = self.client.recv(7 - len(request))
                if not chunk:
                    break
                request.extend(chunk)
            if bytes(request) != b"reset\n":
                raise FixtureFailure("device-reset-request")
            self._reset()
            self.client.sendall(b"ok\n")
            self.completed = True
        except Exception as error:
            self.failure = safe_failure_code(error)
        finally:
            if self.client is not None:
                self.client.close()  # Go requires exact ok-newline then EOF.
            self.finished.set()

    def _drain_serial(self):
        total = 0
        try:
            while not self.stopped.is_set():
                if not select.select([self.process.stdout], [], [], 0.2)[0]:
                    continue
                chunk = os.read(self.process.stdout.fileno(), 8192)
                total += len(chunk)
                if not chunk or total > 1 << 20:
                    if not self.stopped.is_set():
                        self.failure = "device-reset-console"
                    return
        except Exception:
            if not self.stopped.is_set():
                self.failure = "device-reset-console"

    def verify(self):
        if not self.finished.wait(1) or not self.completed or not self.reset_attempted or self.failure is not None:
            raise FixtureFailure("device-reset-unproven")

    def close(self):
        self.stopped.set()
        for connection in (self.client, self.listener, self.qmp):
            if connection is not None:
                try:
                    connection.close()
                except OSError:
                    pass
        for thread in (self.worker, self.drain):
            if thread is not None:
                thread.join(timeout=2)
        if self.socket_identity is not None:
            try:
                info = os.lstat(RESET_SOCKET)
                if (info.st_dev, info.st_ino) == self.socket_identity:
                    os.unlink(RESET_SOCKET)
            except FileNotFoundError:
                pass
        self.buffer[:] = b""
