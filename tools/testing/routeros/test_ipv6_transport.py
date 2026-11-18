import unittest
from console import FixtureFailure, qemu_arguments
from ipv6_transport import capabilities_zero, passt_arguments, session_sources


class IPv6TransportContracts(unittest.TestCase):
    def test_all_capability_sets_and_no_new_privileges(self):
        status = "\n".join(f"{key}:\t0000000000000000" for key in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb")) + "\nNoNewPrivs:\t1\n"
        self.assertTrue(capabilities_zero(status))
        for key in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"):
            self.assertFalse(capabilities_zero(status.replace(f"{key}:\t0000000000000000", f"{key}:\t000001ffffffffff")))
        self.assertFalse(capabilities_zero(status.replace("NoNewPrivs:\t1", "NoNewPrivs:\t0")))
        self.assertFalse(capabilities_zero(status + "CapBnd:\t0\n"))

    def test_runtime_sources_are_exact_not_configuration_or_echo(self):
        lines = "MDS6SOURCE=2001:db8:ffff::100|64501\nMDS6SOURCE=2001:db8:ffff::101|64502\n"
        self.assertTrue(session_sources(lines))
        self.assertTrue(session_sources(lines.replace("\n", "\r\n")))
        self.assertTrue(session_sources(lines.replace("\n", "\r")))
        self.assertTrue(session_sources("  " + lines.replace("\n", "\r\n\t")))
        for bad in ("", lines.splitlines()[0], lines.replace("::101|64502", "::100|64501"), lines.replace("64502", "64500"), lines + "MDS6SOURCE=2001:db8::1|64503\n", '[admin] > :put "' + lines.replace("\n", " ") + '"\n', lines.replace("MDS6SOURCE=", "remote.address=")):
            self.assertFalse(session_sources(bad))

    def test_management_stays_separate_from_real_ipv6_stream(self):
        args = qemu_arguments(ssh=True, bgp_generated_ipv6=True)
        self.assertIn("stream,id=md_bgp6,addr.type=unix,addr.path=/scratch/passt-bgp6.sock", args)
        management = next(value for value in args if value.startswith("user,id=n0"))
        self.assertIn("restrict=on,ipv6=off", management)
        self.assertNotIn("guestfwd", management)
        self.assertEqual(sum(value.startswith("e1000,") for value in args), 2)
        self.assertFalse(any("tap," in value or "/dev/net/tun" in value or "vhost=on" in value for value in args))
        for kwargs in ({"bgp_generated_ipv6": True}, {"ssh": True, "bgp_generated_ipv6": True, "bgp_generated_ipv4": True}, {"ssh": True, "bgp_generated_ipv6": True, "bgp_cache": True}):
            with self.assertRaises(FixtureFailure):
                qemu_arguments(**kwargs)

    def test_passt_has_one_literal_ipv6_forward_no_discovery(self):
        args = passt_arguments()
        self.assertEqual(args[0], "/lab/passt")
        self.assertEqual(args[args.index("--tcp-ports") + 1], "::1/13790:179")
        self.assertEqual(args.count("--tcp-ports"), 1)
        for option in ("--ipv6-only", "--no-udp", "--no-ra", "--no-dhcp", "--no-dhcpv6", "--no-map-gw"):
            self.assertIn(option, args)
        self.assertNotIn("--no-icmp", args)
        self.assertNotIn("--no-ndp", args)
        self.assertEqual(args[args.index("--dns") + 1], "none")
        self.assertEqual(args[args.index("--search") + 1], "none")


if __name__ == "__main__":
    unittest.main()
