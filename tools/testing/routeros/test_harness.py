"""Offline contract tests; these do not boot QEMU or open network listeners."""
import base64
import importlib.util
import io
import os
from pathlib import Path
import re
import struct
import sys
import tempfile
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location("fixture_" + name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


runner, console, live_schema = load("run"), load("console"), load("live_schema")
guest_resources = load("guest_resources")
with mock.patch.dict(sys.modules, {"live_schema": live_schema}):
    experiments = load("experiments")
    with mock.patch.dict(sys.modules, {"experiments": experiments}):
        evidence = load("result")
bootstrap = load("bootstrap")
original_path = list(sys.path)
with mock.patch.dict(sys.modules, {"console": console, "result": evidence, "live_schema": live_schema, "experiments": experiments, "guest_resources": guest_resources}):
    ssh = load("ssh_fixture")
    with mock.patch.dict(sys.modules, {"ssh_fixture": ssh}):
        bgp = load("bgp_cache")
        with mock.patch.dict(sys.modules, {"bgp_cache": bgp}):
            transport = load("transport")
sys.path[:] = original_path


class HarnessContract(unittest.TestCase):
    def tool_tree(self, root):
        for relative in runner.QEMU_SUBTREES:
            (root / relative).mkdir(parents=True, exist_ok=True)
        binary = root / "usr/bin/qemu-system-x86_64"
        binary.write_text("synthetic executable fixture")
        binary.chmod(0o700)
        (root / "usr/share/seabios/bios-256k.bin").write_bytes(b"synthetic firmware")

    def test_namespace_mounts_are_explicit_and_shell_free(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "QEMU tool tree;not-a-command"
            self.tool_tree(tools)
            image = root / "image;not-a-command.img"
            binary = root / "test-binary"
            image.write_bytes(b"fixture")
            binary.write_bytes(b"fixture")
            command = runner.build_command(image, tools, binary, HERE)
            properties = runner.build_command(image, tools, binary, HERE, properties=True)
            self.assertEqual(properties, command + ["--properties"])
            ssh_command = runner.build_command(image, tools, binary, HERE, ssh=True)
            ssh_mounts = [(ssh_command[i + 1], ssh_command[i + 2]) for i, value in enumerate(ssh_command) if value == "--ro-bind"]
            plain_destinations = [command[i + 2] for i, value in enumerate(command) if value == "--ro-bind"]
            self.assertTrue(all(tool not in plain_destinations for tool in runner.SSH_TOOLS))
            self.assertEqual([dst for _, dst in ssh_mounts if dst in runner.SSH_TOOLS], list(runner.SSH_TOOLS))
            self.assertEqual(ssh_command[-2:], ["/lab/bootstrap.py", "--ssh"])
        self.assertIn("--unshare-all", command)
        self.assertIn("--clearenv", command)
        self.assertIn("--die-with-parent", command)
        self.assertIn("--new-session", command)
        self.assertNotIn("/bin/sh", command)
        self.assertNotIn("-c", command)
        self.assertEqual(command[-5:], ["/usr/bin/python3", "-I", "-S", "-B", "/lab/bootstrap.py"])
        mounts = [(command[i + 1], command[i + 2]) for i, value in enumerate(command) if value == "--ro-bind"]
        native_mounts = [(src, dst) for src, dst in mounts if dst.startswith("/native/")]
        self.assertEqual([dst for _, dst in native_mounts], ["/native/" + sub for sub in runner.QEMU_SUBTREES])
        self.assertNotIn(str(tools), [src for src, _ in mounts])
        self.assertNotIn("/", [src for src, _ in mounts])
        self.assertFalse(any(src.startswith("/home/") and dst.startswith("/home") for src, dst in mounts))
        self.assertEqual([command[i + 1] for i, value in enumerate(command) if value == "--tmpfs"], ["/scratch"])
        self.assertIn("805306368", command)
        readonly = [command[i + 1] for i, value in enumerate(command) if value == "--remount-ro"]
        self.assertIn("/", readonly)
        self.assertIn("/dev", readonly)
        self.assertIn(str(image), command)
        self.assertNotIn("sh -c", " ".join(command))
        self.assertEqual(set(runner.SAFE_ENV), {"PATH", "LANG"})

    def test_qemu_tree_cannot_redirect_a_mounted_subtree_to_host_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.tool_tree(root)
            (root / "usr/share/qemu").rmdir()
            (root / "usr/share/qemu").symlink_to("/tmp", target_is_directory=True)
            with self.assertRaises(runner.InvalidInput):
                runner.qemu_subtrees(root)

    def test_unpinned_image_is_rejected_before_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            image = Path(temporary) / "wrong.img"
            with image.open("wb") as out:
                out.truncate(runner.IMAGE_SIZE)
            with self.assertRaises(runner.InvalidInput):
                runner.verify_image(image)

    def test_guest_has_only_disposable_image_and_private_api_port(self):
        command = console.qemu_arguments()
        self.assertIn("pc,accel=tcg", command)
        self.assertIn("file=/base/chr.img,format=raw,if=virtio,snapshot=on", command)
        self.assertIn("user,id=n0,restrict=on,ipv6=off,hostfwd=tcp:127.0.0.1:18729-:8729", command)
        self.assertFalse(any("12222" in value for value in command))
        self.assertNotIn("-enable-kvm", command)
        self.assertIn("-nodefaults", command)
        self.assertIn("on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny", command)
        ssh_command = console.qemu_arguments(ssh=True)
        self.assertEqual(len(ssh_command), len(command))
        self.assertEqual(sum(left != right for left, right in zip(command, ssh_command)), 1)
        self.assertIn("user,id=n0,restrict=on,ipv6=off,hostfwd=tcp:127.0.0.1:18729-:8729,hostfwd=tcp:127.0.0.1:12222-:22", ssh_command)

    def successful_output(self):
        lines = ["=== RUN   " + evidence.TEST]
        for index, (name, value) in enumerate(evidence.REQUIRED.items(), 1):
            lines.append(f"    native_integration_test.go:{index}: {name}={value}")
        lines += ["--- PASS: " + evidence.TEST + " (2.50s)", "PASS"]
        return "\n".join(lines).encode()

    def test_evidence_requires_the_actual_test_and_readback_not_just_exit_zero(self):
        complete = self.successful_output()
        self.assertTrue(evidence.inspect_output(complete, 0)[0])
        for data, code in ((b"PASS\n", 0), (complete, 1), (complete.replace(b"rollback_readback_verified=true", b"rollback_readback_verified=false"), 0), (complete.replace(b"--- PASS:", b"--- SKIP:"), 0)):
            self.assertFalse(evidence.inspect_output(data, code)[0])

    def test_guest_errors_and_private_values_are_never_forwarded(self):
        secret = "private-secret-that-must-not-appear"
        for prefix in ("", "    native_integration_test.go:42: password=", "    native_integration_test.go:42: terminal_code="):
            success, output = evidence.inspect_output(self.successful_output() + b"\n" + (prefix + secret).encode(), 0)
            self.assertFalse(success)
            self.assertNotIn(secret, "\n".join(output))
        self.assertEqual(evidence.inspect_output(b"x" * 65537, 0), (False, []))

    def successful_properties(self):
        # Synthetic safe observations, not a claim of native execution.
        rows = (
            "probe_fib_case=omit add_acknowledged=true",
            "probe_fib_case=on add_acknowledged=true",
            "probe_fib_unset_matches_omitted_representation=true",
            "probe_kind=routing-table probe_field=fib probe_source=print present=false empty=false true=false false=false other=false",
            "probe_kind=routing-table probe_field=fib probe_source=get present=true empty=false true=true false=false other=false",
            "probe_explicit_connection_table_stage=after-set print_matches_expected=true getter_matches_expected=true getter_known=true api_code=none",
            "probe_explicit_connection_table_stage=add-explicit print_matches_expected=true getter_matches_expected=true getter_known=true api_code=none",
            "probe_md5_stage=unset full_getter_accepted=true full_known=false full_matches_empty=false api_code=none",
            "probe_md5_stage=clear setter_acknowledged=true api_code=none uncertain=false",
            "probe_md5_stage=clear full_getter_accepted=true full_known=true full_matches_expected=true full_matches_empty=true api_code=none",
            "probe_md5_stage=clear limited_getter_accepted=false limited_known=false limited_matches_fixture=false limited_matches_empty=false api_code=command-failed",
        )
        lines = ["=== RUN   " + evidence.PROPERTY_TEST]
        lines += [f"    native_api_test.go:{index}: {row}" for index, row in enumerate(rows, 1)]
        lines += ["--- PASS: " + evidence.PROPERTY_TEST + " (2.81s)", "PASS"]
        return "\n".join(lines).encode()

    def test_properties_require_their_own_completed_test_and_closed_enum_values(self):
        complete = self.successful_properties()
        self.assertTrue(evidence.inspect_output(complete, 0, evidence.PROPERTY_TEST)[0])
        self.assertFalse(evidence.inspect_output(complete, 0)[0])
        self.assertFalse(evidence.inspect_output(self.successful_output(), 0, evidence.PROPERTY_TEST)[0])
        for data, code in (
            (complete, 1), (b"PASS\n", 0),
            (complete.replace(b"--- PASS:", b"--- SKIP:"), 0),
            (complete.replace(b"probe_md5_stage=clear", b"probe_md5_stage=set"), 0),
            (complete.replace(b"probe_explicit_connection_table_stage=add-explicit", b"probe_explicit_connection_table_stage=after-set"), 0),
            (complete.replace(b"api_code=none", b"api_code=1234"), 0),
            (complete.replace(b"probe_field=fib", b"probe_field=not-reviewed"), 0),
            (complete.replace(b"full_known=true", b"full_known=7.24.6"), 0),
        ):
            self.assertFalse(evidence.inspect_output(data, code, evidence.PROPERTY_TEST)[0])

    def test_properties_never_forward_unknown_fields_raw_ids_or_private_values(self):
        for word in ("probe_unreviewed=true", "probe_field=*1234", "probe_kind=private-secret", "api_code=private-secret", "password=private-secret", "probe_source=private-secret", "full_known=private-secret"):
            raw = self.successful_properties() + b"\n    native_api_test.go:42: " + word.encode()
            passed, lines = evidence.inspect_output(raw, 0, evidence.PROPERTY_TEST)
            self.assertFalse(passed)
            self.assertNotIn(word, "\n".join(lines))
        self.assertEqual(evidence.inspect_output(b"x" * 65537, 0, evidence.PROPERTY_TEST), (False, []))

    def test_properties_flag_survives_privilege_drop_only_as_fixed_argv(self):
        keys = ("PATH HOME TMPDIR LD_LIBRARY_PATH QEMU_MODULE_DIR LANG TERM PYTHONDONTWRITEBYTECODE GOMAXPROCS GOMEMLIMIT").split()
        environment = {key: "synthetic" for key in keys}
        environment["UNTRUSTED_HOST_SECRET"] = "not-forwarded"
        with mock.patch.dict(os.environ, environment, clear=True), mock.patch.object(bootstrap.os, "umask"), mock.patch.object(bootstrap.subprocess, "run", return_value=mock.Mock(returncode=0)) as setup, mock.patch.object(bootstrap.os, "execve") as execute:
            bootstrap.main(["--properties"])
        self.assertEqual(setup.call_args.args[0], ["/usr/sbin/ip", "link", "set", "lo", "up"])
        executable, command, forwarded = execute.call_args.args
        self.assertEqual(executable, "/usr/bin/setpriv")
        self.assertIn("--bounding-set=-all", command)
        self.assertIn("--no-new-privs", command)
        self.assertEqual(command[-2:], ["/lab/transport.py", "--properties"])
        self.assertNotIn("UNTRUSTED_HOST_SECRET", forwarded)
        self.assertEqual(transport.test_arguments(evidence.PROPERTY_TEST)[2], "^TestNativeCHRAPIProperties$")
        self.assertEqual(transport.test_arguments(evidence.TEST)[2], "^TestNativeCHRSafeModeRollback$")
        with self.assertRaises(console.FixtureFailure):
            transport.test_arguments(".*")

    def test_properties_setup_uses_fresh_private_credentials_and_fixed_read_only_policy(self):
        first, second = "ab" * 24, "cd" * 24
        simulated = mock.Mock()
        with mock.patch.object(transport.secrets, "token_hex", side_effect=[first, second]) as random_password, mock.patch.object(transport, "private_file") as store, mock.patch.object(transport, "emit") as logs:
            transport.prepare_properties(simulated, "admin-fixture")
            transport.prepare_properties(simulated, "admin-fixture")
        self.assertEqual(random_password.call_args_list, [mock.call(24), mock.call(24)])
        self.assertEqual(store.call_args_list, [mock.call("/scratch/limited-password", first), mock.call("/scratch/limited-password", second)])
        commands = [call.args[0] for call in simulated.command.call_args_list]
        self.assertEqual(commands[0], "/user/group/add name=mdlab_read policy=read,api")
        self.assertEqual(commands[1], '/user/add name=mdlab-read group=mdlab_read password="' + first + '"')
        self.assertEqual(simulated.send.call_args_list[:2], [mock.call(b"admin+ct200w60h\r"), mock.call(b"admin-fixture\r")])
        self.assertNotIn(first, repr(logs.call_args_list))
        self.assertNotIn("admin-fixture", repr(logs.call_args_list))
        with tempfile.TemporaryDirectory() as temporary:
            password = Path(temporary) / "limited-password"
            transport.private_file(password, first)
            self.assertEqual(password.stat().st_mode & 0o777, 0o600)
            self.assertEqual(password.read_text(), first)
            with self.assertRaises(FileExistsError):
                transport.private_file(password, second)

    def successful_ssh(self, test):
        # Synthetic fixed observations only, never native execution evidence.
        lines = ["=== RUN   " + test]
        lines += [f"    native_ssh_test.go:{index}: {field}={value}" for index, (field, value) in enumerate(evidence.SSH_REQUIRED[test].items(), 1)]
        lines += ["--- PASS: " + test + " (1.22s)", "PASS"]
        return "\n".join(lines).encode()

    def test_ssh_cancellation_and_confirmation_require_independent_readback_proofs(self):
        for test, field in ((evidence.SSH_CANCEL_TEST, "ssh_rollback_readback_verified"), (evidence.SSH_CONFIRM_TEST, "ssh_confirm_retained_after_close")):
            complete = self.successful_ssh(test)
            self.assertTrue(evidence.inspect_output(complete, 0, test)[0])
            for raw, code in ((b"PASS", 0), (complete, 1), (complete.replace(b"--- PASS:", b"--- SKIP:"), 0), (complete.replace((field + "=true").encode(), (field + "=false").encode()), 0)):
                self.assertFalse(evidence.inspect_output(raw, code, test)[0])
            for other in (evidence.TEST, evidence.PROPERTY_TEST, evidence.SSH_CANCEL_TEST, evidence.SSH_CONFIRM_TEST):
                if other != test:
                    self.assertFalse(evidence.inspect_output(complete, 0, other)[0])
            for word in ("ssh_unreviewed=true", "ssh_confirm_version=private-value", "ssh_confirm_retained_after_close=*1234", "terminal_code=private-value", "api_code=private-value", "ssh_password=private-value"):
                passed, lines = evidence.inspect_output(complete + b"\n    native_ssh_test.go:42: " + word.encode(), 0, test)
                self.assertFalse(passed)
                self.assertNotIn(word, "\n".join(lines))
            self.assertEqual(transport.test_arguments(test)[2], "^" + test + "$")
            self.assertEqual(evidence.inspect_output(b"x" * 65537, 0, test), (False, []))

    def test_ssh_followups_have_fixed_order_and_stop_on_each_failed_test(self):
        calls = []
        def run(environment, test):
            calls.append(test)
        with mock.patch.object(transport, "prepare_properties", side_effect=lambda *_: calls.append("properties-setup")), mock.patch.object(transport, "relogin", side_effect=lambda *_: calls.append("login")), mock.patch.object(transport, "prepare_ssh", side_effect=lambda *_: calls.append("ssh-setup")), mock.patch.object(transport, "run_test", side_effect=run), mock.patch.object(transport, "emit"):
            transport.run_followups(None, "", {}, properties=True, ssh=True)
            self.assertEqual(calls, ["properties-setup", evidence.PROPERTY_TEST, "ssh-setup", evidence.SSH_CANCEL_TEST, evidence.SSH_CONFIRM_TEST])
            calls.clear()
            transport.run_followups(None, "", {}, ssh=True)
            self.assertEqual(calls, ["login", "ssh-setup", evidence.SSH_CANCEL_TEST, evidence.SSH_CONFIRM_TEST])
            calls.clear()
            transport.run_followups(None, "", {})
            self.assertEqual(calls, [])
        for failed in (evidence.PROPERTY_TEST, evidence.SSH_CANCEL_TEST):
            calls.clear()
            def fail(environment, test):
                calls.append(test)
                if test == failed:
                    raise console.FixtureFailure("synthetic-test-failed")
            with mock.patch.object(transport, "prepare_properties"), mock.patch.object(transport, "prepare_ssh"), mock.patch.object(transport, "run_test", side_effect=fail), mock.patch.object(transport, "emit"):
                with self.assertRaises(console.FixtureFailure):
                    transport.run_followups(None, "", {}, properties=True, ssh=True)
            self.assertEqual(calls[-1], failed)
            self.assertNotIn(evidence.SSH_CONFIRM_TEST, calls)

    def public_wire(self, value=b"synthetic-public-key", algorithm=b"ssh-rsa"):
        return base64.b64encode(len(algorithm).to_bytes(4, "big") + algorithm + value).decode("ascii")

    def test_ssh_host_key_requires_trusted_serial_bytes_and_public_export(self):
        wire = self.public_wire()
        converted = ("ssh-rsa " + wire + "\n").encode()
        scanned = ("[127.0.0.1]:12222 ssh-rsa " + wire + "\n").encode()
        self.assertEqual(ssh.pinned_host(converted, scanned), scanned.decode())
        annotated = converted.rstrip() + b" local-conversion-comment\n"
        self.assertEqual(ssh.pinned_host(annotated, b"# scanner protocol comment\n" + scanned), scanned.decode())
        with self.assertRaises(console.FixtureFailure):
            ssh.pinned_host(annotated, b"# comments alone do not establish a key\n")
        for bad in (scanned + scanned, scanned.replace(b"127.0.0.1", b"192.0.2.1"), scanned.replace(wire.encode(), self.public_wire(b"different-key").encode()), scanned.replace(b"ssh-rsa", b"ssh-ed25519"), b"private-value", b"x" * 16385):
            with self.assertRaises(console.FixtureFailure):
                ssh.pinned_host(converted, bad)
        self.assertEqual(ssh.public_export_name("mdlab_host_rsa.pem\r\nmdlab_host_rsa_pub.pem\r\n"), "mdlab_host_rsa_pub.pem")
        for listing in ("mdlab_host_rsa.pem", "../mdlab_host_rsa_pub.pem", "mdlab_host_ed25519_pub.pem", "mdlab_host_rsa_pub.pem\nmdlab_host_ed25519_pub.pem", "mdlab_host_rsa_pub.pem\nmdlab_host_rsa_pub.pem"):
            with self.assertRaises(console.FixtureFailure):
                ssh.public_export_name(listing)
        ed_wire = self.public_wire(algorithm=b"ssh-ed25519")
        with self.assertRaises(console.FixtureFailure):
            ssh.pinned_host(("ssh-ed25519 " + ed_wire + "\n").encode(), ("[127.0.0.1]:12222 ssh-ed25519 " + ed_wire + "\n").encode())
        for body in ("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----", "not-public-pem", "x" * 32769):
            with self.assertRaises(console.FixtureFailure):
                ssh.public_pem(body)

    def test_ssh_setup_never_reads_guest_private_host_key_or_host_credentials(self):
        wire = self.public_wire()
        public = "ssh-ed25519 " + self.public_wire(algorithm=b"ssh-ed25519") + " native-fixture"
        pem = "-----BEGIN PUBLIC KEY-----\nAA==\n-----END PUBLIC KEY-----\n"
        peer = mock.Mock()
        peer.command.side_effect = ["", "", "mdlab_host_rsa.pem\nmdlab_host_rsa_pub.pem", pem]
        with mock.patch.object(ssh, "generated_public_key", return_value=public), mock.patch.object(ssh, "tool_output", side_effect=[b"", ("ssh-rsa " + wire + "\n").encode(), ("[127.0.0.1]:12222 ssh-rsa " + wire + "\n").encode()]) as tool:
            store = mock.Mock()
            ssh.prepare_ssh(peer, store)
        commands = [call.args[0] for call in peer.command.call_args_list]
        self.assertEqual(commands[0], '/user/ssh-keys/add user=admin key="' + public + '"')
        self.assertEqual(commands[-1], ':put [/file/get [/file/find where name="mdlab_host_rsa_pub.pem"] contents]')
        self.assertFalse(any('name="mdlab_host_rsa.pem"' in command for command in commands))
        self.assertEqual(tool.call_args_list[0].args[0], ["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "native-fixture", "-f", "/scratch/ssh_identity"])
        self.assertEqual(tool.call_args_list[-1].args[0], ["/usr/bin/ssh-keyscan", "-T", "8", "-p", "12222", "-t", "rsa", "127.0.0.1"])
        self.assertEqual([call.args[0] for call in store.call_args_list], ["/scratch/host-public.pem", "/scratch/known_hosts"])
        self.assertNotIn("SSH_AUTH_SOCK", ssh.SSH_ENV)
        self.assertNotIn("LD_LIBRARY_PATH", ssh.SSH_ENV)
        with self.assertRaises(console.FixtureFailure):
            ssh.tool_output(["/bin/sh", "-c", "forbidden"], 1024, 1)
        self.assertEqual(ssh.safe_failure_code(console.FixtureFailure("ssh-host-key-mismatch")), "ssh-host-key-mismatch")
        self.assertEqual(ssh.safe_failure_code(console.FixtureFailure("private-value")), "unclassified")
        self.assertEqual(ssh.safe_failure_code(RuntimeError("private-value")), "unclassified")

    def test_native_ssh_source_logs_still_obey_the_closed_filter_contract(self):
        source = (HERE.parents[2] / "internal/routeros/native_ssh_test.go").read_text()
        formats = re.findall(r't\.(?:Logf|Log|Fatalf|Fatal)\("([^"\n]*)"', source)
        self.assertGreater(len(formats), 20)
        self.assertNotIn("ssh_identity_length", source)
        self.assertNotIn("ssh_identity_observed_lines", source)
        for format in formats:
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=", "unstructured native SSH output")
                if value == "%s":
                    self.assertIn(field, ("api_code", "terminal_code"))
                    value = "none"
                elif value == "%t":
                    value = "true"
                self.assertTrue(evidence.valid_field(field, value, properties=False, ssh=True), "unreviewed native SSH log field: " + field)

    def test_live_requires_explicit_ssh_and_read_only_independent_binary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.tool_tree(root / "tools")
            image, binary, live_binary = root / "image", root / "routeros.test", root / "live.test"
            for path in (image, binary, live_binary):
                path.write_bytes(b"synthetic")
            default = runner.build_command(image, root / "tools", binary, HERE, ssh=True)
            live = runner.build_command(image, root / "tools", binary, HERE, ssh=True, live=True, live_binary=live_binary)
            self.assertNotIn("/lab/routeros-live.test", default)
            self.assertEqual(live[-3:], ["/lab/bootstrap.py", "--ssh", "--live"])
            mounts = [(live[i + 1], live[i + 2]) for i, value in enumerate(live) if value == "--ro-bind"]
            self.assertIn((str(live_binary), "/lab/routeros-live.test"), mounts)
            self.assertNotIn("--bind", live)
            for options in ({"live": True}, {"live": True, "live_binary": live_binary}, {"live_binary": live_binary}):
                with self.assertRaises(runner.InvalidInput):
                    runner.build_command(image, root / "tools", binary, HERE, **options)
        required = ["--image", "/unused", "--qemu-root", "/unused", "--test-binary", "/unused"]
        for modes in (["--live"], ["--live-test-binary", "/unused"], ["--live", "--live-test-binary", "/unused"]):
            with mock.patch("sys.stderr", new=io.StringIO()), mock.patch.object(runner.subprocess, "run") as execute:
                with self.assertRaises(SystemExit):
                    runner.main(required + modes)
                execute.assert_not_called()

    def test_live_selectors_are_exact_and_future_prefix_matches_are_not_authorized(self):
        self.assertEqual(live_schema.TESTS, ("TestNativeCHRLiveObserve", "TestNativeCHRLiveGeneratedPrepare", "TestNativeCHRLiveResetIdle", "TestNativeCHRLiveSupervisorCancel", "TestNativeCHRLiveSupervisorConfirm", "TestNativeCHRLiveSupervisorRestartReadOnly"))
        for test in live_schema.TESTS:
            arguments = transport.test_arguments(test)
            self.assertEqual(arguments[0], "/lab/routeros-live.test")
            self.assertEqual(arguments[2], "^" + test + "$")
        for test in ("TestNativeCHRLive", "TestNativeCHRLiveUnreviewed", "TestNativeCHRLiveSupervisorRestartReadOnlyExtra", "^TestNativeCHRLive.*", "TestNativeCHRLiveObserve|TestNativeCHRLiveUnreviewed"):
            with self.assertRaises(console.FixtureFailure):
                transport.test_arguments(test)
            self.assertEqual(evidence.inspect_output(b"PASS", 0, test), (False, []))
        calls = []
        with mock.patch.object(transport, "relogin"), mock.patch.object(transport, "prepare_ssh"), mock.patch.object(transport, "run_test", side_effect=lambda environment, test: calls.append(test)), mock.patch.object(transport, "emit"):
            transport.run_followups(None, "", {}, ssh=True, live=True)
        self.assertEqual(calls, [evidence.SSH_CANCEL_TEST, evidence.SSH_CONFIRM_TEST, *live_schema.TESTS])
        with mock.patch.object(transport, "run_test") as execute:
            with self.assertRaises(console.FixtureFailure):
                transport.run_followups(None, "", {}, live=True)
            execute.assert_not_called()

    def successful_live(self, test):
        summaries = {
            "TestNativeCHRLiveObserve": ["live_complete_empty_namespace=true cross_channel_identity=true pending_count_known=true native_activation_permitted=false", "live_readback_after_owner_close=true"],
            "TestNativeCHRLiveGeneratedPrepare": ["live_connection_switch_field=connect explicit_yes=true print_present=true print_empty=false getter_known=true getter_true=true api_code=none", "live_connection_switch_field=listen explicit_yes=true print_present=false print_empty=false getter_known=true getter_true=true api_code=none", "live_generated_prepare_native_readback=true activation_exercised=false undo_budget_verified=false"],
            "TestNativeCHRLiveResetIdle": ["idle_reset_new_unowned_session=true cross_channel_identity=true old_owner_resumed=false"],
            "TestNativeCHRLiveSupervisorCancel": ["native_supervisor_mode=cancel phase=prepare resource=routing-table action=add history_before=0 history_after=0 history_delta=0", "native_supervisor_mode=cancel phase=activate resource=bgp-connection action=enable history_before=0 history_after=1 history_delta=1", "native_supervisor_cancel_checkpoint_verified=true prepared_retained=true manual_cleanup_before_proof=false"],
            "TestNativeCHRLiveSupervisorConfirm": ["native_supervisor_mode=confirm phase=prepare resource=routing-table action=add history_before=0 history_after=0 history_delta=0", "native_supervisor_mode=confirm phase=activate resource=bgp-connection action=enable history_before=0 history_after=1 history_delta=1", "native_supervisor_confirm_strict_snapshot_verified=true post_owner_close_retained=true manual_cleanup_before_proof=false"],
            "TestNativeCHRLiveSupervisorRestartReadOnly": ["native_supervisor_mode=restart phase=prepare resource=routing-table action=add history_before=0 history_after=0 history_delta=0", "native_supervisor_mode=restart phase=activate resource=bgp-connection action=enable history_before=0 history_after=1 history_delta=1", "native_supervisor_restart_readonly_verified=true exact_checkpoint_verified=true fresh_runtime_identity_verified=true old_confirmation_rejected=true forbidden_native_calls=0 manual_cleanup_before_proof=false"],
        }
        lines = ["=== RUN   " + test]
        filename = "native_restart_test.go" if test == "TestNativeCHRLiveSupervisorRestartReadOnly" else "native_test.go"
        lines += [f"    {filename}:{index}: {row}" for index, row in enumerate(summaries[test], 1)]
        lines += ["--- PASS: " + test + " (2.00s)", "PASS"]
        return "\n".join(lines).encode()

    def test_live_output_requires_each_proof_without_raw_values_or_open_codes(self):
        for test in live_schema.TESTS:
            complete = self.successful_live(test)
            self.assertTrue(evidence.inspect_output(complete, 0, test)[0])
            self.assertFalse(evidence.inspect_output(complete, 1, test)[0])
            self.assertFalse(evidence.inspect_output(b"PASS", 0, test)[0])
            self.assertFalse(evidence.inspect_output(complete.replace(b"--- PASS:", b"--- SKIP:"), 0, test)[0])
            for other in live_schema.TESTS:
                if other != test:
                    self.assertFalse(evidence.inspect_output(complete, 0, other)[0])
            for word in ("code=private-value", "code=inspection-unknown-property-value-bgp-connection-private-value", "field=private-value", "resource=private-value", "live_unreviewed=true", "operation_index=100001", "operation_index=-1", "operation_index=01", "api_code=private-value"):
                success, safe = evidence.inspect_output(complete + b"\n    native_test.go:42: " + word.encode(), 0, test)
                self.assertFalse(success)
                self.assertNotIn(word, "\n".join(safe))
        self.assertTrue(live_schema.valid_field("code", "inspection-unknown-property-value-bgp-connection-connect"))
        self.assertTrue(live_schema.valid_field("code", "required-property-getter-failed-bgp-connection-listen"))
        for number in ("0", "10", "100000"):
            self.assertTrue(live_schema.valid_field("operation_index", number))
        self.assertFalse(evidence.inspect_output(self.successful_live(live_schema.TESTS[1]).replace(b"undo_budget_verified=false", b"undo_budget_verified=true"), 0, live_schema.TESTS[1])[0])
        for test in live_schema.SUPERVISOR_TESTS:
            complete = self.successful_live(test)
            for changed in (
                complete.replace(b"history_delta=1", b"history_delta=2"),
                complete.replace(b"history_after=1", b"history_after=81"),
                complete.replace(b"phase=activate", b"phase=prepare"),
                complete.replace(b"manual_cleanup_before_proof=false", b"manual_cleanup_before_proof=true"),
            ):
                self.assertFalse(evidence.inspect_output(changed, 0, test)[0])
            for number in ("-1", "81", "01", "private-value"):
                self.assertFalse(live_schema.valid_field("history_delta", number))

    def test_native_live_source_logs_obey_reviewed_fields_and_bounded_types(self):
        source = "\n".join((HERE.parents[2] / "internal/routeros/live" / name).read_text() for name in ("native_test.go", "native_supervisor_test.go", "native_restart_test.go"))
        formats = re.findall(r't\.(?:Logf|Log|Fatalf|Fatal|Error|Errorf|Skip)\("([^"\n]*)"', source)
        self.assertGreater(len(formats), 20)
        for format in formats:
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=", "unstructured live output")
                if value == "%s":
                    self.assertTrue(field == "code" or field in live_schema.ENUMS, "unbounded live string: " + field)
                    value = "none" if field == "code" else next(iter(live_schema.ENUMS[field]))
                elif value == "%t":
                    value = "true"
                elif value == "%d":
                    self.assertIn(field, ("operation_index", "history_before", "history_after", "history_delta"))
                    value = "0"
                self.assertTrue(live_schema.valid_field(field, value), "unreviewed live log field: " + field)

    def test_restart_readonly_requires_every_proof_and_no_forbidden_call(self):
        test = "TestNativeCHRLiveSupervisorRestartReadOnly"
        complete = self.successful_live(test)
        for field, value in live_schema.REQUIRED[test].items():
            removed = complete.replace((field + "=" + value).encode(), b"")
            self.assertFalse(evidence.inspect_output(removed, 0, test)[0], "missing restart proof accepted: " + field)
        for changed in (
            complete.replace(b"forbidden_native_calls=0", b"forbidden_native_calls=1"),
            complete.replace(b"exact_checkpoint_verified=true", b"exact_checkpoint_verified=false"),
            complete.replace(b"fresh_runtime_identity_verified=true", b"fresh_runtime_identity_verified=false"),
            complete.replace(b"old_confirmation_rejected=true", b"old_confirmation_rejected=false"),
            complete.replace(b"native_supervisor_mode=restart", b"native_supervisor_mode=confirm"),
        ):
            self.assertFalse(evidence.inspect_output(changed, 0, test)[0])
        self.assertEqual(transport.test_arguments(test)[2], "^TestNativeCHRLiveSupervisorRestartReadOnly$")
        self.assertEqual(transport.test_wall_seconds(test), 310)
        for number in ("1", "-1", "00", "private-value"):
            self.assertFalse(live_schema.valid_field("forbidden_native_calls", number))

    def remote_open(self):
        return bgp.packet(1, struct.pack("!BHH4sB", 4, 64500, 90, bytes((192, 0, 2, 1)), 0))

    def test_bgp_fixture_announces_only_two_prefixes_once_on_one_open(self):
        protocol = bgp.Protocol()
        opened = self.remote_open()
        replies = []
        for at in range(0, len(opened), 3):
            replies += protocol.receive(opened[at:at + 3])
        self.assertEqual(len(replies), 1)
        self.assertEqual(protocol.opens, 1)
        first = protocol.receive(bgp.packet(4))
        self.assertEqual(first, [bgp.announcement() + bgp.packet(2, b"\x00\x00\x00\x00")])
        self.assertEqual(protocol.receive(bgp.packet(4)), [])
        self.assertEqual((protocol.opens, protocol.updates, protocol.refreshes), (1, 1, 0))
        update = bgp.announcement()
        self.assertEqual(update[:16], b"\xff" * 16)
        length, kind = struct.unpack("!HB", update[16:19])
        self.assertEqual((length, kind), (len(update), 2))
        body = update[19:]
        self.assertEqual(body[:2], b"\x00\x00")
        attribute_length = int.from_bytes(body[2:4], "big")
        self.assertEqual(body[4 + attribute_length:], b"\x18\xcb\x00\x71\x18\xc6\x33\x64")
        with self.assertRaises(console.FixtureFailure):
            protocol.receive(self.remote_open())

    def test_bgp_refresh_malformed_and_unbounded_streams_cannot_be_success(self):
        protocol = bgp.Protocol()
        protocol.receive(self.remote_open())
        protocol.receive(bgp.packet(4))
        refresh = b"\xff" * 16 + struct.pack("!HB", 23, 5) + b"\x00\x01\x00\x01"
        with self.assertRaises(console.FixtureFailure):
            protocol.receive(refresh)
        self.assertEqual((protocol.updates, protocol.refreshes), (1, 1))
        for malformed in (b"x" * 19, b"\xff" * 16 + struct.pack("!HB", 4097, 1), b"\xff" * 16 + struct.pack("!HB", 18, 1), b"x" * 16385, bgp.packet(4)):
            with self.assertRaises(console.FixtureFailure):
                bgp.Protocol().receive(malformed)
        protocol = bgp.Protocol()
        protocol.receive(self.remote_open())
        with self.assertRaises(console.FixtureFailure):
            for _ in range(257):
                protocol.receive(bgp.packet(4))

    def test_bgp_readback_projection_and_final_proof_never_return_raw_routes(self):
        rows = "prefix=198.51.100.0/24 filtered=true active= unreachable=\r\nprefix=203.0.113.0/24 filtered= active=true unreachable=\r\n"
        self.assertEqual(bgp.route_projection(rows), bgp.expected_routes("203.0.113.0/24"))
        self.assertEqual(bgp.route_projection("fixed command echo\n"), {})
        for bad in (rows + rows, rows.replace("203.0.113.0/24", "192.0.2.0/24"), rows.replace("filtered=true", "filtered=unknown"), rows.replace("active=true", "active=true private-value")):
            with self.assertRaises(console.FixtureFailure):
                bgp.route_projection(bad)
        line = bgp.proof_line(dict(bgp.PROOF))
        self.assertNotIn("203.0.113", line)
        self.assertNotIn("198.51.100", line)
        self.assertIn("bgp_cache_fixture=fixed-single-peer-ipv4", line)
        for change in ({"bgp_update_once": False}, {"bgp_update_once": 1}, {"bgp_cache_fixture": "private-value"}, {"raw_routes": rows}):
            with self.assertRaises(console.FixtureFailure):
                bgp.proof_line(dict(bgp.PROOF, **change))
        self.assertTrue(bgp.owner_state("enabled: yes\r\ncurrent: yes\r\n", True, True))
        self.assertFalse(bgp.owner_state("enabled: yes\r\ncurrent: yes\r\ncurrent: yes\r\n", True, True))
        self.assertFalse(bgp.owner_state("enabled: no\r\ncurrent: no\r\n", True, True))

    def test_bgp_mode_requires_ssh_and_adds_only_the_fixed_private_forward(self):
        default = console.qemu_arguments(ssh=True)
        enabled = console.qemu_arguments(ssh=True, bgp_cache=True)
        self.assertEqual(len(enabled), len(default))
        self.assertEqual(sum(left != right for left, right in zip(default, enabled)), 1)
        network = enabled[enabled.index("-netdev") + 1]
        self.assertTrue(network.endswith(",guestfwd=tcp:10.0.2.100:179-tcp:127.0.0.1:11790"))
        self.assertNotIn("guestfwd", " ".join(default))
        self.assertNotIn("cmd:", network)
        with self.assertRaises(console.FixtureFailure):
            console.qemu_arguments(bgp_cache=True)
        required = ["--image", "/unused", "--qemu-root", "/unused", "--test-binary", "/unused", "--bgp-cache"]
        with mock.patch("sys.stderr", new=io.StringIO()), mock.patch.object(runner.subprocess, "run") as execute:
            with self.assertRaises(SystemExit):
                runner.main(required)
            execute.assert_not_called()
        calls = []
        peer = object()
        with mock.patch.object(transport, "relogin"), mock.patch.object(transport, "prepare_ssh"), mock.patch.object(transport, "run_test", side_effect=lambda environment, test: calls.append(test)), mock.patch.object(bgp, "run_probe", side_effect=lambda *_: calls.append("bgp") or dict(bgp.PROOF)), mock.patch.object(transport, "emit"):
            transport.run_followups(None, "", {}, ssh=True, bgp_cache=True, bgp_peer=peer)
        self.assertEqual(calls, [evidence.SSH_CANCEL_TEST, evidence.SSH_CONFIRM_TEST, "bgp"])
        with self.assertRaises(console.FixtureFailure):
            transport.run_followups(None, "", {}, bgp_cache=True, bgp_peer=peer)
        arguments = bgp.owner_arguments()
        self.assertEqual(arguments[0], "/usr/bin/ssh")
        self.assertEqual(arguments[-4:], ["-l", "admin+ct200w60h", "--", "127.0.0.1"])
        for option in ("StrictHostKeyChecking=yes", "IdentityAgent=none", "ProxyCommand=none", "ProxyJump=none", "ControlPath=none", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "ClearAllForwardings=yes"):
            self.assertIn(option, arguments)
        self.assertEqual(bgp.SSH_OWNER_ENV["TERM"], "dumb")
        self.assertEqual(bgp.SSH_OWNER_ENV["SSH_ASKPASS_REQUIRE"], "never")
        self.assertNotIn("SSH_AUTH_SOCK", bgp.SSH_OWNER_ENV)
        self.assertNotIn("LD_LIBRARY_PATH", bgp.SSH_OWNER_ENV)
        self.assertEqual(bgp.safe_failure_code(RuntimeError("private-value")), "unclassified")

    def test_disconnect_is_standalone_with_only_its_longer_wall_budget(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.tool_tree(root / "tools")
            image, binary = root / "image", root / "routeros.test"
            image.write_bytes(b"synthetic")
            binary.write_bytes(b"synthetic")
            normal = runner.build_command(image, root / "tools", binary, HERE, ssh=True)
            isolated = runner.build_command(image, root / "tools", binary, HERE, ssh_disconnect=True)
            self.assertEqual(normal[2], "480s")
            self.assertEqual(isolated[2], "900s")
            normalized = list(isolated)
            normalized[2] = "480s"
            normalized[-1] = "--ssh"
            self.assertEqual(normalized, normal)
            for mode in ("properties", "ssh", "live", "bgp_cache"):
                with self.assertRaises(runner.InvalidInput):
                    runner.build_command(image, root / "tools", binary, HERE, ssh_disconnect=True, **{mode: True})
        required = ["--image", "/unused", "--qemu-root", "/unused", "--test-binary", "/unused", "--ssh-disconnect"]
        for flag in ("--ssh", "--properties", "--live", "--bgp-cache"):
            with mock.patch("sys.stderr", new=io.StringIO()), mock.patch.object(runner.subprocess, "run") as execute:
                with self.assertRaises(SystemExit):
                    runner.main(required + [flag])
                execute.assert_not_called()
        arguments = transport.test_arguments(evidence.SSH_DISCONNECT_TEST)
        self.assertEqual(arguments, ["/lab/routeros.test", "-test.run", "^TestNativeCHRSSHDisconnectRollback$", "-test.v", "-test.timeout", "12m"])
        self.assertEqual(transport.test_wall_seconds(evidence.SSH_DISCONNECT_TEST), 730)
        self.assertEqual(transport.test_wall_seconds(evidence.TEST), 160)

    def test_disconnect_additional_opt_in_is_local_and_runs_exactly_one_test(self):
        environment = {"MOEDOVE_CHR_NATIVE_TEST": "1", "PATH": "/synthetic"}
        console_stub = object()
        with mock.patch.object(transport, "prepare_ssh") as prepare, mock.patch.object(transport, "run_test") as run, mock.patch.object(transport, "relogin") as relogin, mock.patch.object(transport, "emit"):
            transport.run_disconnect(console_stub, environment)
        prepare.assert_called_once_with(console_stub, transport.private_file)
        relogin.assert_not_called()
        run.assert_called_once_with(dict(environment, MOEDOVE_CHR_FAILURE_TEST="1"), evidence.SSH_DISCONNECT_TEST)
        self.assertNotIn("MOEDOVE_CHR_FAILURE_TEST", environment)
        keys = ("PATH HOME TMPDIR LD_LIBRARY_PATH QEMU_MODULE_DIR LANG TERM PYTHONDONTWRITEBYTECODE GOMAXPROCS GOMEMLIMIT").split()
        with mock.patch.dict(os.environ, {key: "synthetic" for key in keys}, clear=True), mock.patch.object(bootstrap.os, "umask"), mock.patch.object(bootstrap.subprocess, "run", return_value=mock.Mock(returncode=0)), mock.patch.object(bootstrap.os, "execve") as execute:
            bootstrap.main(["--ssh-disconnect"])
        _, arguments, forwarded = execute.call_args.args
        self.assertEqual(arguments[-2:], ["/lab/transport.py", "--ssh-disconnect"])
        for limit in ("--as=4294967296", "--cpu=360", "--nproc=128", "--nofile=256", "--fsize=268435456", "--core=0"):
            self.assertIn(limit, arguments)
        self.assertNotIn("MOEDOVE_CHR_FAILURE_TEST", forwarded)

    def test_cpu_budget_is_fixed_by_the_explicit_case_without_other_limit_changes(self):
        keys = ("PATH HOME TMPDIR LD_LIBRARY_PATH QEMU_MODULE_DIR LANG TERM PYTHONDONTWRITEBYTECODE GOMAXPROCS GOMEMLIMIT").split()
        for case in (None, *experiments.CASES):
            flags = [] if case is None else ["--ssh", "--case", case]
            with mock.patch.dict(os.environ, {key: "synthetic" for key in keys}, clear=True), mock.patch.object(bootstrap.os, "umask"), mock.patch.object(bootstrap.subprocess, "run", return_value=mock.Mock(returncode=0)), mock.patch.object(bootstrap.os, "execve") as execute:
                bootstrap.main(flags)
            _, arguments, _ = execute.call_args.args
            expected = 960 if case == "cleanup1000-confirm" else 600 if case in experiments.LONG_CASES else 360
            self.assertIn("--cpu="+str(expected), arguments)
            self.assertEqual(bootstrap.cpu_limit(case), expected)
            for limit in ("--as=4294967296", "--nproc=128", "--nofile=256", "--fsize=268435456", "--core=0"):
                self.assertIn(limit, arguments)
        with self.assertRaises(ValueError):
            bootstrap.cpu_limit("caller-supplied-budget")

    def successful_disconnect(self):
        lines = ["=== RUN   " + evidence.SSH_DISCONNECT_TEST]
        lines += [f"    native_failures_test.go:{index}: {field}={value}" for index, (field, value) in enumerate(evidence.DISCONNECT_REQUIRED.items(), 1)]
        lines += ["    native_failures_test.go:200: ssh_disconnect_wait_ms=3014", "--- PASS: " + evidence.SSH_DISCONNECT_TEST + " (4.10s)", "PASS"]
        return "\n".join(lines).encode()

    def test_disconnect_requires_real_undo_and_bounded_wait_without_raw_data(self):
        complete = self.successful_disconnect()
        test = evidence.SSH_DISCONNECT_TEST
        self.assertTrue(evidence.inspect_output(complete, 0, test)[0])
        for raw, code in ((b"PASS", 0), (complete, 1), (complete.replace(b"--- PASS:", b"--- SKIP:"), 0), (complete.replace(b"ssh_disconnect_rollback_readback_verified=true", b"ssh_disconnect_rollback_readback_verified=false"), 0), (complete.replace(b"ssh_disconnect_manual_cleanup=false", b"ssh_disconnect_manual_cleanup=true"), 0), (complete.replace(b"    native_failures_test.go:200: ssh_disconnect_wait_ms=3014\n", b""), 0)):
            self.assertFalse(evidence.inspect_output(raw, code, test)[0])
        for value in ("0", "600000"):
            self.assertTrue(evidence.inspect_output(complete.replace(b"wait_ms=3014", ("wait_ms=" + value).encode()), 0, test)[0])
        for value in ("-1", "600001", "03014", "3.014", "private-value"):
            success, output = evidence.inspect_output(complete.replace(b"wait_ms=3014", ("wait_ms=" + value).encode()), 0, test)
            self.assertFalse(success)
            self.assertNotIn("wait_ms=" + value, "\n".join(output))
        for word in ("ssh_disconnect_version=7.24.6", "ssh_disconnect_unreviewed=true", "api_code=private-value", "system_id=private-value"):
            success, output = evidence.inspect_output(complete + b"\n    native_failures_test.go:42: " + word.encode(), 0, test)
            self.assertFalse(success)
            self.assertNotIn(word, "\n".join(output))
        source = (HERE.parents[2] / "internal/routeros/native_failures_test.go").read_text()
        for format in re.findall(r't\.(?:Logf|Log|Fatalf|Fatal|Skip)\("([^"\n]*)"', source):
            for word in format.split():
                field, separator, value = word.partition("=")
                self.assertEqual(separator, "=")
                if value == "%s":
                    self.assertIn(field, ("api_code", "terminal_code"))
                    value = "none"
                elif value == "%d":
                    self.assertEqual(field, "ssh_disconnect_wait_ms")
                    value = "3014"
                self.assertTrue(evidence.valid_field(field, value, properties=False, disconnect=True), "unreviewed disconnect log field: " + field)


if __name__ == "__main__":
    unittest.main()
