"""Bounded pure MP-BGP codec fixtures; no sockets, helper or router is started."""
import ipaddress
import struct
import threading
import unittest

import bgp_generated6 as bgp
from console import FixtureFailure


def remote_open(capability=True, duplicate=False):
    cap = bytes((1, 4, 0, 2, 0, 1)) if capability else bytes((1, 4, 0, 1, 0, 1))
    if duplicate:
        cap += cap
    params = bytes((2, len(cap))) + cap
    body = struct.pack("!BHH4sB", 4, 64500, 90, bytes((192, 0, 2, 1)), len(params)) + params
    return bgp.base.packet(1, body)


def ready(role="recipient"):
    protocol = bgp.Protocol(role)
    protocol.receive(remote_open())
    protocol.receive(bgp.base.packet(4))
    return protocol


def update(announced=("a",), withdrawn=(), hop=bgp.SENDER_ADDRESS, link_local="", path_kind=2, path=(64501,), extra=b"", reserved=0):
    attrs = b""
    if announced:
        raw_path = bytes((path_kind, len(path))) + b"".join(struct.pack("!H", asn) for asn in path)
        attrs = bytes.fromhex("40010100") + bytes((0x40, 2, len(raw_path))) + raw_path
        next_hop = ipaddress.IPv6Address(hop).packed
        if link_local:
            next_hop += ipaddress.IPv6Address(link_local).packed
        attrs += bgp.attribute(14, bytes((0, 2, 1, len(next_hop))) + next_hop + bytes((reserved,)) + bgp.nlri(announced))
    if withdrawn:
        attrs += bgp.attribute(15, bytes((0, 2, 1)) + bgp.nlri(withdrawn))
    attrs += extra
    return bgp.base.packet(2, bytes((0, 0)) + struct.pack("!H", len(attrs)) + attrs)


class GeneratedIPv6Tests(unittest.TestCase):
    def test_control_requires_actual_handshakes_and_explicit_native_source_callback(self):
        for raw in (b'{"action":"snapshot","action":"connect-once"}', b'{"action":"arbitrary"}', b'{"action":"snapshot","command":"get"}', b'{"action":1}', b'x' * 1024):
            with self.assertRaises(FixtureFailure):
                bgp.parse_control_request(raw)
        fixture = bgp.FixedPeers6.__new__(bgp.FixedPeers6)
        fixture.lock = threading.RLock()
        fixture.connect_requested = threading.Event()
        fixture.sequence, fixture.failure, fixture.announce_requested = 0, "", False
        fixture.native_sources_verified = False
        fixture.protocols = {role: bgp.Protocol(role) for role in bgp.PROFILES}
        fixture.connect_attempts = {role: 0 for role in bgp.PROFILES}
        fixture.socket_verified = {role: False for role in bgp.PROFILES}
        calls = []
        fixture.verify_native_sources = lambda: calls.append(True) or True
        self.assertFalse(fixture._response("connect-once")["nativeSourcesVerified"])
        with self.assertRaises(FixtureFailure):
            fixture._response("connect-once")
        with self.assertRaises(FixtureFailure):
            fixture._response("verify-native-sources")
        self.assertFalse(calls)
        fixture.protocols = {role: ready(role) for role in bgp.PROFILES}
        with self.assertRaises(FixtureFailure):
            fixture._response("verify-native-sources")
        fixture.connect_attempts = {role: 1 for role in bgp.PROFILES}
        fixture.socket_verified = {role: True for role in bgp.PROFILES}
        with self.assertRaises(FixtureFailure):
            fixture._response("announce-once")
        self.assertTrue(fixture._response("verify-native-sources")["nativeSourcesVerified"])
        self.assertEqual(calls, [True])
        self.assertTrue(fixture._response("announce-once")["ok"])
        with self.assertRaises(FixtureFailure):
            fixture._response("announce-once")

    def test_active_connect_sends_only_one_local_open(self):
        protocol = bgp.Protocol("sender")
        self.assertEqual(protocol.start(), (bgp.local_open("sender"),))
        self.assertEqual(protocol.receive(remote_open()), [bgp.base.packet(4)])
        self.assertEqual(protocol.receive(bgp.base.packet(4)), [bgp.eor()])
        self.assertEqual(protocol.snapshot()["opensSent"], 1)
        self.assertEqual(protocol.opens, 1)
        with self.assertRaises(FixtureFailure):
            protocol.start()

    def test_native_source_failure_or_lost_handshake_never_sets_proof(self):
        for mode in ("false", "exception", "disconnect"):
            fixture = bgp.FixedPeers6.__new__(bgp.FixedPeers6)
            fixture.lock = threading.RLock()
            fixture.sequence, fixture.failure, fixture.announce_requested = 0, "", False
            fixture.native_sources_verified = False
            fixture.protocols = {role: ready(role) for role in bgp.PROFILES}
            fixture.connect_attempts = {role: 1 for role in bgp.PROFILES}
            fixture.socket_verified = {role: True for role in bgp.PROFILES}
            def verify():
                if mode == "false":
                    return False
                if mode == "exception":
                    raise FixtureFailure("ipv6-native-session-sources")
                fixture.protocols["sender"].established = False
                return True
            fixture.verify_native_sources = verify
            with self.assertRaises(FixtureFailure):
                fixture._response("verify-native-sources")
            self.assertFalse(fixture.native_sources_verified)
            with self.assertRaises(FixtureFailure):
                fixture._response("announce-once")

    def test_fragmented_open_requires_mutual_mp6_and_distinct_peer_asns(self):
        for role, asn in (("sender", 64501), ("recipient", 64502)):
            protocol, replies = bgp.Protocol(role), []
            for byte in remote_open():
                replies.extend(protocol.receive(bytes((byte,))))
            self.assertEqual(len(replies), 2)
            self.assertEqual(int.from_bytes(replies[0][20:22], "big"), asn)
            self.assertTrue(protocol.ipv6_negotiated)
            self.assertFalse(protocol.established)
            self.assertEqual(protocol.receive(bgp.base.packet(4)), [bgp.eor()])
            self.assertTrue(protocol.snapshot()["ipv6Negotiated"])
        for wire in (remote_open(False), remote_open(duplicate=True)):
            with self.assertRaises(FixtureFailure):
                bgp.Protocol("sender").receive(wire)

    def test_ipv6_eor_is_not_classic_ipv4_empty_update(self):
        protocol = ready()
        protocol.receive(bgp.base.packet(2, bytes(4)))
        self.assertEqual(protocol.eors_received, 0)
        protocol.receive(bgp.eor())
        self.assertEqual(protocol.eors_received, 1)
        self.assertFalse(protocol.rib)
        self.assertFalse(protocol.events)

    def test_actual_announcements_withdrawals_and_cache_transition(self):
        protocol = ready()
        wire = update()
        for at in range(0, len(wire), 3):
            protocol.receive(wire[at:at + 3])
        self.assertEqual(protocol.rib["a"], {"prefixId": "a", "origin": 0, "path": [{"type": 2, "asns": [64501]}], "nextHop": bgp.SENDER_ADDRESS})
        protocol.receive(update(("b",), ("a",)))
        self.assertEqual(set(protocol.rib), {"b"})
        self.assertTrue(protocol.events[-2]["removed"])
        protocol.receive(update(("a",), ("b",)))
        self.assertEqual(set(protocol.rib), {"a"})
        self.assertEqual([event["kind"] for event in protocol.events], ["announce", "withdraw", "announce", "withdraw", "announce"])

    def test_as_set_and_changed_next_hops_are_observed_not_replaced(self):
        protocol = ready()
        protocol.receive(update(path_kind=1, path=(64500, 64501), hop=bgp.ROUTER_ADDRESS, link_local="fe80::15"))
        row = protocol.snapshot()["rib"][0]
        self.assertEqual(row["path"], [{"type": 1, "asns": [64500, 64501]}])
        self.assertEqual(row["nextHop"], bgp.ROUTER_ADDRESS)
        self.assertEqual(row["linkLocalNextHop"], "fe80::15")
        row["path"][0]["asns"][0] = 1
        self.assertEqual(protocol.rib["a"]["path"][0]["asns"][0], 64500)

    def test_global_and_link_local_halves_are_both_preserved(self):
        protocol = ready()
        protocol.receive(update(link_local="fe80::100"))
        self.assertEqual(protocol.rib["a"]["nextHop"], bgp.SENDER_ADDRESS)
        self.assertEqual(protocol.rib["a"]["linkLocalNextHop"], "fe80::100")
        protocol.receive(update())
        self.assertNotIn("linkLocalNextHop", protocol.rib["a"])
        for hop, local in (("::", ""), ("ff02::1", ""), ("fe80::1", ""), ("::ffff:192.0.2.1", ""), (bgp.SENDER_ADDRESS, bgp.RECIPIENT_ADDRESS)):
            with self.assertRaises(FixtureFailure):
                ready().receive(update(hop=hop, link_local=local))

    def test_reserved_octet_is_ignored_without_inventing_data(self):
        one, two = ready(), ready()
        one.receive(update(reserved=0))
        two.receive(update(reserved=255))
        self.assertEqual(one.rib, two.rib)

    def test_sender_announces_once_with_mp6_and_recipient_never_announces(self):
        sender = ready("sender")
        self.assertEqual(sender.announce_once(), (bgp.announcement(), bgp.eor()))
        self.assertEqual(sender.announcements_sent, 1)
        reader = ready()
        reader.receive(bgp.announcement())
        self.assertEqual(set(reader.rib), {"a", "b"})
        for protocol in (sender, ready("recipient")):
            with self.assertRaises(FixtureFailure):
                protocol.announce_once()

    def test_mp_family_truncation_duplicates_and_missing_attributes_reject(self):
        invalid = [bytes((0, 1, 1)), bytes((0, 2, 2)), bytes((0, 2, 1, 15)) + bytes(16), bytes((0, 2, 1, 32)) + bytes(17)]
        for value in invalid:
            with self.assertRaises(FixtureFailure):
                bgp.parse_attributes(bgp.attribute(14, value))
        reach = bytes((0, 2, 1, 16)) + ipaddress.IPv6Address(bgp.SENDER_ADDRESS).packed + bytes((0,)) + bgp.nlri(("a",))
        for attrs in (bgp.attribute(14, reach), bgp.attribute(14, reach) * 2, bytes((0x40, 14, 3, 0, 2, 1))):
            with self.assertRaises(FixtureFailure):
                bgp.parse_attributes(attrs)

    def test_no_ipv4_nlri_or_legacy_next_hop_is_treated_as_ipv6(self):
        for body in (bytes((0, 1, 0, 0)), bytes((0, 0, 0, 0, 24, 203, 0, 113)), bytes((0, 0, 1, 0))):
            with self.assertRaises(FixtureFailure):
                bgp.parse_update(body)
        with self.assertRaises(FixtureFailure):
            ready().receive(update(extra=bytes.fromhex("400304c0000201")))

    def test_prefix_and_stream_bounds_keep_unknown_results_out(self):
        for raw in (bytes((129,)), bytes((48, 32, 1)), bgp.nlri(("a",)) * 2, bytes((0,)), bytes((47, 32, 1, 13, 184, 1, 1))):
            with self.assertRaises(FixtureFailure):
                bgp.parse_prefixes(raw)
        for wire in (bytes(19), bytes((255,)) * 16 + struct.pack("!HB", 4097, 2), b"x" * 16385, remote_open()):
            with self.assertRaises(FixtureFailure):
                ready().receive(wire)
        protocol = ready()
        for _ in range(128):
            protocol.receive(update())
        with self.assertRaises(FixtureFailure):
            protocol.receive(update())

    def test_refresh_is_observed_and_never_hidden_as_a_cached_transition(self):
        protocol = ready()
        wire = bytes((255,)) * 16 + struct.pack("!HB", 23, 5) + bytes((0, 2, 0, 1))
        with self.assertRaises(FixtureFailure):
            protocol.receive(wire)
        self.assertEqual(protocol.refreshes, 1)


if __name__ == "__main__":
    unittest.main()
