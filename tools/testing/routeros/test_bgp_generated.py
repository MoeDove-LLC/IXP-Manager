"""Synthetic codec/coordination tests; no sockets or RouterOS are opened."""
import ipaddress
import json
import struct
import threading
import unittest

import bgp_generated as bgp
from console import FixtureFailure


def remote_open():
    return bgp.packet(1, struct.pack("!BHH4sB", 4, 64500, 90, bytes((192, 0, 2, 1)), 0))


def ready(role="recipient"):
    protocol = bgp.Protocol(role)
    protocol.receive(remote_open())
    protocol.receive(bgp.packet(4))
    return protocol


def update(prefixes=("a",), withdrawals=(), asns=(64501,), hop="10.0.2.100", extra=b""):
    nlri = {"a": b"\x18\xcb\x00\x71", "b": b"\x18\xc6\x33\x64"}
    gone = b"".join(nlri[v] for v in withdrawals)
    attrs = b""
    if prefixes:
        path = b"\x02" + bytes((len(asns),)) + b"".join(struct.pack("!H", n) for n in asns)
        attrs = b"\x40\x01\x01\x00\x40\x02" + bytes((len(path),)) + path
        attrs += b"\x40\x03\x04" + ipaddress.IPv4Address(hop).packed
    attrs += extra
    return bgp.packet(2, struct.pack("!H", len(gone)) + gone + struct.pack("!H", len(attrs)) + attrs + b"".join(nlri[v] for v in prefixes))


class GeneratedBGPTests(unittest.TestCase):
    def test_fragmented_open_has_distinct_fixed_asns_and_no_auto_announcement(self):
        for role, expected_asn in (("sender", 64501), ("recipient", 64502)):
            protocol = bgp.Protocol(role)
            replies = []
            for octet in remote_open():
                replies.extend(protocol.receive(bytes((octet,))))
            self.assertEqual(len(replies), 2)
            self.assertEqual(int.from_bytes(replies[0][20:22], "big"), expected_asn)
            self.assertFalse(protocol.established)
            replies = protocol.receive(bgp.packet(4))
            self.assertTrue(protocol.established)
            self.assertEqual(replies, [bgp.packet(2, b"\x00\x00\x00\x00")])
            self.assertEqual(protocol.announcements_sent, 0)
            if role == "sender":
                self.assertEqual(protocol.announce_once()[0], bgp.announcement())
                self.assertEqual(protocol.announcements_sent, 1)
            with self.assertRaises(FixtureFailure):
                protocol.announce_once()

    def test_true_adjrib_tracks_announcements_withdrawals_and_attributes(self):
        protocol = ready()
        wire = update()
        for at in range(0, len(wire), 7):
            protocol.receive(wire[at:at + 7])
        self.assertEqual(protocol.rib["a"], {"prefixId": "a", "origin": 0, "path": [{"type": 2, "asns": [64501]}], "nextHop": "10.0.2.100"})
        protocol.receive(update(("b",), ("a",)))
        self.assertEqual(set(protocol.rib), {"b"})
        self.assertTrue(protocol.events[-2]["removed"])
        self.assertEqual(protocol.events[-2]["prefixId"], "a")
        protocol.receive(update(("a",), ("b",)))
        self.assertEqual(set(protocol.rib), {"a"})
        self.assertEqual([event["kind"] for event in protocol.events], ["announce", "withdraw", "announce", "withdraw", "announce"])
        self.assertEqual([event["sequence"] for event in protocol.events], [1, 2, 3, 4, 5])
        self.assertEqual(protocol.announcements_sent, 0)

    def test_modified_path_and_next_hop_are_observed_not_replaced_by_expected(self):
        protocol = ready()
        protocol.receive(update(asns=(64500, 64501), hop="10.0.2.15"))
        self.assertEqual(protocol.rib["a"]["path"][0]["asns"], [64500, 64501])
        self.assertEqual(protocol.rib["a"]["nextHop"], "10.0.2.15")
        exposed = protocol.snapshot()
        exposed["rib"][0]["path"][0]["asns"][0] = 1
        self.assertEqual(protocol.rib["a"]["path"][0]["asns"][0], 64500)

    def test_eor_and_duplicate_withdraw_are_not_fake_advertisements(self):
        protocol = ready()
        protocol.receive(bgp.packet(2, b"\x00\x00\x00\x00"))
        self.assertEqual(protocol.eors_received, 1)
        self.assertFalse(protocol.rib)
        protocol.receive(update((), ("a",)))
        self.assertFalse(protocol.events[-1]["removed"])
        self.assertFalse(protocol.rib)

    def test_malformed_updates_fail_closed(self):
        malformed = [b"", b"\x00\x04\x18", b"\x00\x00\x01\x00", b"\x00\x00\x00\x00\x21\x01\x02\x03\x04\x05"]
        for body in malformed:
            with self.assertRaises(FixtureFailure):
                bgp.parse_update(body)
        for attrs in (b"\x40\x01\x01\x00\x40\x01\x01\x00", b"\x41\x01\x01\x00", b"\x40\x01\x02\x00", b"\x40\x02\x04\x02\x02\xfc\x01", b"\x40\x03\x03\x01\x02\x03", b"\x40\x63\x00", b"\x80\x0e\x00"):
            with self.assertRaises(FixtureFailure):
                bgp.parse_attributes(attrs)
        with self.assertRaises(FixtureFailure):
            bgp.parse_update(b"\x00\x00\x00\x00\x18\xcb\x00\x71")
        for prefix in (b"\x18\xc0\x00\x02", b"\x17\xcb\x00\x71", b"\x18\xcb\x00\x71\x18\xcb\x00\x71"):
            with self.assertRaises(FixtureFailure):
                bgp.parse_prefixes(prefix)

    def test_extended_attribute_length_is_supported_and_bounded(self):
        attrs = b"\x50\x01\x00\x01\x00\x50\x02\x00\x04\x02\x01\xfc\x01\x50\x03\x00\x04\x0a\x00\x02\x64"
        parsed = bgp.parse_attributes(attrs)
        self.assertEqual(parsed["path"], [{"type": 2, "asns": [64513]}])
        self.assertEqual(parsed["nextHop"], "10.0.2.100")
        self.assertEqual(bgp.parse_attributes(b"\x80\x63\x02ab"), {})

    def test_no_refresh_reconnect_or_unbounded_stream_is_success(self):
        protocol = ready("sender")
        refresh = b"\xff" * 16 + struct.pack("!HB", 23, 5) + b"\x00\x01\x00\x01"
        with self.assertRaises(FixtureFailure):
            protocol.receive(refresh)
        self.assertEqual(protocol.refreshes, 1)
        for wire in (remote_open(), b"x" * 19, b"\xff" * 16 + struct.pack("!HB", 4097, 2), b"x" * 16385):
            with self.assertRaises(FixtureFailure):
                ready().receive(wire)
        protocol = ready()
        for _ in range(128):
            protocol.receive(update())
        with self.assertRaises(FixtureFailure):
            protocol.receive(update())

    def test_control_is_closed_and_announcement_requires_both_actual_handshakes(self):
        for raw in (b'{"action":"snapshot","path":"/system/reboot"}', b'{"action":"snapshot","action":"announce-once"}', b'{"action":true}', b'{"action":"set"}', b'{"action":"snapshot"}{}', b"x" * 1024):
            with self.assertRaises(FixtureFailure):
                bgp.parse_control_request(raw)
        self.assertEqual(bgp.parse_control_request(b'{"action":"snapshot"}\n'), "snapshot")
        fixture = bgp.FixedPeers.__new__(bgp.FixedPeers)
        fixture.lock = threading.Lock()
        fixture.sequence = 0
        fixture.failure = ""
        fixture.announce_requested = False
        fixture.protocols = {role: bgp.Protocol(role) for role in bgp.PROFILES}
        with self.assertRaises(FixtureFailure):
            fixture._response("announce-once")
        for protocol in fixture.protocols.values():
            protocol.receive(remote_open())
            protocol.receive(bgp.packet(4))
        response = fixture._response("announce-once")
        self.assertTrue(response["ok"])
        self.assertTrue(fixture.announce_requested)
        self.assertEqual(response["peers"]["sender"]["announcementsSent"], 0)
        with self.assertRaises(FixtureFailure):
            fixture._response("announce-once")
        self.assertNotIn("password", json.dumps(response))


if __name__ == "__main__":
    unittest.main()
