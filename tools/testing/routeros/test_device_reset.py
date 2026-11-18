"""Offline checks of the single-use QMP bridge and native reset evidence."""
import os
import stat
import struct
import threading
from types import SimpleNamespace
import unittest
from unittest import mock

from console import FixtureFailure, qemu_arguments
import device_reset as reset
from test_harness import evidence


ACK = {"return": {}, "id": "fixture-device-reset"}
HOST_RESET = {"event": "RESET", "data": {"guest": False, "reason": "host-qmp-system-reset"}}
RESET_COMMAND = b'{"execute":"system_reset","id":"fixture-device-reset"}\n'


def socket_peer(connection, pid):
    connection.getsockopt.return_value = struct.pack("3i", pid, os.geteuid(), os.getegid())


class DeviceResetContract(unittest.TestCase):
    def controller(self):
        controller = reset.DeviceReset.__new__(reset.DeviceReset)
        controller.qmp = mock.Mock()
        controller.listener = mock.Mock()
        controller.client = None
        controller.buffer = bytearray()
        controller.stopped = threading.Event()
        controller.finished = threading.Event()
        controller.reset_attempted = controller.completed = False
        controller.failure = None
        return controller

    def test_only_reset_case_opens_the_private_monitor(self):
        plain = qemu_arguments(ssh=True)
        self.assertEqual(qemu_arguments(ssh=True, device_reset=True), plain[:-1] + [
            "-qmp", "unix:/scratch/device-reset-qmp.sock,server=on,wait=off",
        ])
        self.assertEqual(plain[-1], "-no-reboot")
        self.assertNotIn("-qmp", plain)
        for other in ({"ssh": False}, {"bgp_cache": True}, {"bgp_generated_ipv4": True}, {"bgp_generated_ipv6": True}):
            with self.subTest(other=other), self.assertRaises(FixtureFailure):
                qemu_arguments(**({"ssh": True, "device_reset": True} | other))

    def test_monitor_binding_checks_peer_pid_and_socket_identity_before_handshake(self):
        private_socket = SimpleNamespace(st_mode=stat.S_IFSOCK | 0o600, st_uid=os.geteuid(), st_dev=1, st_ino=2)
        for peer_pid, inode in ((123, 2), (124, 2), (123, 3)):
            with self.subTest(peer_pid=peer_pid, inode=inode):
                opened = SimpleNamespace(**(vars(private_socket) | {"st_ino": inode}))
                controller = self.controller()
                controller.process = SimpleNamespace(pid=123, returncode=None)
                controller._message = mock.Mock(side_effect=[{"QMP": {}}, {"return": {}, "id": "fixture-capabilities"}])
                socket_peer(controller.qmp, peer_pid)
                with mock.patch.object(reset.socket, "socket", return_value=controller.qmp), \
                        mock.patch.object(reset.os, "lstat", side_effect=[private_socket, opened]), \
                        mock.patch.object(reset.os, "unlink") as unlink:
                    if peer_pid == 123 and inode == 2:
                        controller._connect_qmp()
                        controller.qmp.sendall.assert_called_once_with(b'{"execute":"qmp_capabilities","id":"fixture-capabilities"}\n')
                        unlink.assert_called_once_with(reset.QMP_SOCKET)
                    else:
                        with self.assertRaisesRegex(FixtureFailure, "device-reset-qmp-binding"):
                            controller._connect_qmp()
                        controller._message.assert_not_called()
                        controller.qmp.sendall.assert_not_called()
                        unlink.assert_not_called()

    def test_bridge_replies_only_for_the_go_pid_and_exact_request(self):
        for peer_pid, chunks, failure in (
            (123, [b"re", b"set\n", b""], None),
            (124, [], "device-reset-client"),
            (123, [b"reset\nx"], "device-reset-request"),
            (123, [b"reset", b""], "device-reset-request"),
        ):
            with self.subTest(peer_pid=peer_pid, chunks=chunks):
                controller = self.controller()
                controller.client_pid = 123
                controller._reset = mock.Mock()
                connection = mock.Mock()
                socket_peer(connection, peer_pid)
                connection.recv.side_effect = chunks
                controller.listener.accept.return_value = (connection, "")

                controller._serve()

                controller.listener.accept.assert_called_once_with()
                controller.listener.close.assert_called_once_with()
                connection.close.assert_called_once_with()
                self.assertIs(controller.completed, failure is None)
                self.assertEqual(controller.failure, failure)
                self.assertTrue(controller.finished.is_set())
                if failure is None:
                    controller._reset.assert_called_once_with()
                    connection.sendall.assert_called_once_with(b"ok\n")
                else:
                    controller._reset.assert_not_called()
                    connection.sendall.assert_not_called()
                if peer_pid != controller.client_pid:
                    connection.recv.assert_not_called()

    def test_reset_requires_matching_ack_and_host_event_and_never_retries(self):
        for frames, failure in (
            ([ACK, HOST_RESET], None),
            ([HOST_RESET, ACK], None),
            ([ACK, FixtureFailure("device-reset-qmp-timeout")], "device-reset-qmp-timeout"),
            ([ACK, {"event": "RESET", "data": {"guest": True, "reason": "guest-reset"}}], "device-reset-qmp-order"),
            ([HOST_RESET, {"return": {}, "id": "different-command"}], "device-reset-qmp-order"),
        ):
            with self.subTest(frames=frames), mock.patch.object(reset.select, "select", return_value=([], [], [])):
                controller = self.controller()
                controller._message = mock.Mock(side_effect=frames)
                if failure is None:
                    controller._reset()
                else:
                    with self.assertRaisesRegex(FixtureFailure, failure):
                        controller._reset()
                with self.assertRaisesRegex(FixtureFailure, "device-reset-qmp-order"):
                    controller._reset()
                controller.qmp.sendall.assert_called_once_with(RESET_COMMAND)

    def test_stale_reset_cannot_authorize_a_new_command(self):
        controller = self.controller()
        controller._message = mock.Mock(return_value=HOST_RESET)
        with mock.patch.object(reset.select, "select", return_value=([controller.qmp], [], [])), \
                self.assertRaisesRegex(FixtureFailure, "device-reset-qmp-order"):
            controller._reset()
        controller.qmp.sendall.assert_not_called()

    def test_evidence_requires_complete_readback_and_closed_outcomes(self):
        name = reset.TEST
        proof = " ".join("device_reset_" + field + "=true" for field in sorted(reset.REQUIRED))
        body = f"=== RUN   {name}\n    native_device_reset_test.go:1: {proof} device_reset_outcome=candidate-retained\n--- PASS: {name} (0.01s)\nPASS\n".encode()
        self.assertTrue(evidence.inspect_output(body, 0, name)[0])
        self.assertFalse(evidence.inspect_output(body, 1, name)[0])
        for field in reset.REQUIRED:
            token = f"device_reset_{field}=true".encode()
            with self.subTest(field=field):
                for replacement in (b"", token + b" " + token, token.replace(b"true", b"false")):
                    self.assertFalse(evidence.inspect_output(body.replace(token, replacement), 0, name)[0])
        stale = body.replace(b"candidate-retained", b"unreviewed-secret")
        passed, safe = evidence.inspect_output(stale, 0, name)
        self.assertFalse(passed)
        self.assertNotIn("unreviewed-secret", "\n".join(safe))
        self.assertFalse(evidence.inspect_output(body.replace(b"native_device_reset_test.go", b"unreviewed_test.go"), 0, name)[0])
        self.assertFalse(evidence.inspect_output(body.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])

    def test_write_restoration_requires_its_own_complete_evidence(self):
        reset_rows = {"device_reset_" + field: "true" for field in reset.REQUIRED}
        reset_rows["device_reset_outcome"] = "candidate-retained"
        restore_rows = {field: "true" for field in reset.RESTORE_REQUIRED}
        self.assertTrue(reset.complete([reset_rows, restore_rows], restore=True))
        self.assertFalse(reset.complete([reset_rows, restore_rows]))
        self.assertFalse(reset.complete([reset_rows], restore=True))
        self.assertFalse(reset.complete([{**reset_rows, "device_reset_outcome": "checkpoint-restored"}, restore_rows], restore=True))


if __name__ == "__main__":
    unittest.main()
