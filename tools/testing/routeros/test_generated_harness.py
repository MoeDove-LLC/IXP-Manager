"""Closed launch/evidence contracts for the opt-in generated-policy fixture."""
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import test_harness as base
from test_harness import HERE, console, evidence, live_schema, runner, transport


class GeneratedHarnessContract(unittest.TestCase):
    def test_ipv6_has_explicit_passt_and_live_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "tools"
            base.HarnessContract().tool_tree(tools)
            image, binary, live_binary, passt = (root / name for name in ("image", "base.test", "live.test", "passt"))
            for path in (image, binary, live_binary, passt):
                path.write_bytes(b"fixture")
                path.chmod(0o700)
            args = dict(ssh=True, live_binary=live_binary, bgp_generated_ipv6=True, passt_binary=passt)
            command = runner.build_command(image, tools, binary, HERE, **args)
            self.assertEqual(command[-2:], ["--ssh", "--bgp-generated-ipv6"])
            mounts = [(command[i+1], command[i+2]) for i, value in enumerate(command) if value == "--ro-bind"]
            for name in ("bgp_generated.py", "bgp_generated6.py", "ipv6_transport.py"):
                self.assertIn((str(HERE / name), "/lab/" + name), mounts)
            self.assertIn((str(passt), "/lab/passt"), mounts)
            self.assertNotIn("/dev/net/tun", command)
            self.assertIn("480s", command)
            for forbidden in ("properties", "live", "bgp_cache", "ssh_disconnect", "bgp_generated_ipv4"):
                with self.assertRaises(runner.InvalidInput):
                    runner.build_command(image, tools, binary, HERE, **args, **{forbidden: True})
            for change in ({"ssh": False}, {"live_binary": None}, {"passt_binary": None}, {"bgp_generated_ipv6": False}):
                with self.assertRaises(runner.InvalidInput):
                    runner.build_command(image, tools, binary, HERE, **(args | change))
        self.assertEqual(transport.test_arguments(live_schema.GENERATED_BGP6_TEST), ["/lab/routeros-live.test", "-test.run", "^"+live_schema.GENERATED_BGP6_TEST+"$", "-test.v", "-test.timeout", "300s"])

    def test_independent_mode_has_only_fixed_forwarding_and_readonly_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools = root / "tools"
            base.HarnessContract().tool_tree(tools)
            image, binary, live_binary = (root / name for name in ("image", "base.test", "live.test"))
            for path in (image, binary, live_binary):
                path.write_bytes(b"fixture")
            command = runner.build_command(image, tools, binary, HERE, ssh=True, live_binary=live_binary, bgp_generated_ipv4=True)
            self.assertEqual(command[-2:], ["--ssh", "--bgp-generated-ipv4"])
            self.assertIn("480s", command)
            mounts = [(command[i+1], command[i+2]) for i, value in enumerate(command) if value == "--ro-bind"]
            self.assertIn((str(HERE / "bgp_generated.py"), "/lab/bgp_generated.py"), mounts)
            self.assertIn((str(live_binary), "/lab/routeros-live.test"), mounts)
            plain = runner.build_command(image, tools, binary, HERE)
            self.assertNotIn("/lab/bgp_generated.py", plain)
            for forbidden in ("properties", "live", "bgp_cache", "ssh_disconnect"):
                with self.assertRaises(runner.InvalidInput):
                    runner.build_command(image, tools, binary, HERE, ssh=True, live_binary=live_binary, bgp_generated_ipv4=True, **{forbidden: True})
            with self.assertRaises(runner.InvalidInput):
                runner.build_command(image, tools, binary, HERE, live_binary=live_binary, bgp_generated_ipv4=True)
        args = console.qemu_arguments(ssh=True, bgp_generated_ipv4=True)
        network = args[args.index("-netdev")+1]
        self.assertIn("restrict=on,ipv6=off", network)
        self.assertIn("guestfwd=tcp:10.0.2.100:179-chardev:md_bgp_sender", network)
        self.assertIn("guestfwd=tcp:10.0.2.101:179-chardev:md_bgp_recipient", network)
        self.assertIn("socket,id=md_bgp_sender,host=127.0.0.1,port=11790", args)
        self.assertIn("socket,id=md_bgp_recipient,host=127.0.0.1,port=11791", args)
        self.assertEqual(network.count("guestfwd="), 2)
        with self.assertRaises(console.FixtureFailure):
            console.qemu_arguments(ssh=True, bgp_cache=True, bgp_generated_ipv4=True)

    def test_generated_selector_and_extra_environment_are_explicit(self):
        name = live_schema.GENERATED_BGP_TEST
        self.assertNotIn(name, live_schema.TESTS)
        self.assertEqual(transport.test_arguments(name), ["/lab/routeros-live.test", "-test.run", "^"+name+"$", "-test.v", "-test.timeout", "300s"])
        with self.assertRaises(console.FixtureFailure):
            transport.test_arguments(name+"Unreviewed")
        environment = {"MOEDOVE_CHR_NATIVE_TEST": "1"}
        with mock.patch.object(transport, "prepare_ssh") as prepare, mock.patch.object(transport, "emit"), mock.patch.object(transport, "run_test") as run:
            transport.run_generated(object(), environment)
            prepare.assert_called_once()
            run.assert_called_once_with({"MOEDOVE_CHR_NATIVE_TEST": "1", "MOEDOVE_CHR_GENERATED_BGP_TEST": "1"}, name)
        self.assertEqual(environment, {"MOEDOVE_CHR_NATIVE_TEST": "1"})

    def test_wire_proof_is_complete_and_does_not_claim_ipv6_or_forwarding(self):
        name = live_schema.GENERATED_BGP_TEST
        proof = " ".join(key+"="+value for key, value in live_schema.REQUIRED[name].items())
        lines = ["=== RUN   "+name, "    native_generated_bgp_test.go:499: "+proof, "--- PASS: "+name+" (100.00s)", "PASS"]
        complete = "\n".join(lines).encode()
        self.assertTrue(evidence.inspect_output(complete, 0, name)[0])
        for key, value in live_schema.REQUIRED[name].items():
            self.assertFalse(evidence.inspect_output(complete.replace((key+"="+value).encode(), b""), 0, name)[0])
            wrong = "false" if value == "true" else "true"
            self.assertFalse(evidence.inspect_output(complete.replace((key+"="+value).encode(), (key+"="+wrong).encode()), 0, name)[0])
        self.assertFalse(evidence.inspect_output(complete.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])
        self.assertFalse(evidence.inspect_output(complete, 1, name)[0])
        invalid = complete + b"\n    native_generated_bgp_test.go:499: generated_bgp_password=private-value"
        success, safe = evidence.inspect_output(invalid, 0, name)
        self.assertFalse(success)
        self.assertNotIn("private-value", "\n".join(safe))

    def test_ipv6_requires_native_sources_and_cannot_reuse_ipv4_proof(self):
        name = live_schema.GENERATED_BGP6_TEST
        proof = " ".join(key+"="+value for key, value in live_schema.REQUIRED[name].items())
        complete = ("=== RUN   "+name+"\n    native_generated_bgp6_test.go:575: "+proof+"\n--- PASS: "+name+" (100.00s)\nPASS\n").encode()
        self.assertTrue(evidence.inspect_output(complete, 0, name)[0])
        for key, value in live_schema.REQUIRED[name].items():
            self.assertFalse(evidence.inspect_output(complete.replace((key+"="+value).encode(), b""), 0, name)[0])
            wrong = "false" if value == "true" else "true"
            self.assertFalse(evidence.inspect_output(complete.replace((key+"="+value).encode(), (key+"="+wrong).encode()), 0, name)[0])
        self.assertFalse(evidence.inspect_output(complete, 1, name)[0])
        self.assertFalse(evidence.inspect_output(complete.replace(b"--- PASS:", b"--- SKIP:"), 0, name)[0])
        self.assertFalse(evidence.inspect_output(complete.replace(b"native_generated_bgp6_test.go", b"native_generated_bgp_test.go"), 0, name)[0])
        invalid = complete.replace(b"generated6_bgp_native_sources_verified=true", b"generated6_bgp_private_key=never-forward")
        success, safe = evidence.inspect_output(invalid, 0, name)
        self.assertFalse(success)
        self.assertNotIn("never-forward", "\n".join(safe))


if __name__ == "__main__":
    unittest.main()
