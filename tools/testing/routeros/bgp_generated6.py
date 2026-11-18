"""Bounded MP-BGP IPv6 codec for a fixed, separately isolated native fixture.

This module alone opens no socket and supplies no native/transport proof.
RFC 4760 MP_REACH/UNREACH and RFC 2545 next-hop forms remain explicit; a
32-byte next-hop never silently loses its link-local half. IPv4 fixture code
and its listener/transport defaults are unchanged.
"""
import copy
import ipaddress
import struct
import json
import os
import socket
import stat
import threading
import time

import bgp_generated as base
from console import FixtureFailure

CONTROL_SOCKET = "/scratch/bgp-generated6.sock"
CONTEXT_FILE = "/scratch/bgp-generated6-context.json"
ROUTER_ADDRESS = "2001:db8:ffff::15"
SENDER_ADDRESS = "2001:db8:ffff::100"
RECIPIENT_ADDRESS = "2001:db8:ffff::101"
PREFIXES = {"2001:db8:100::/48": "a", "2001:db8:200::/48": "b"}
PROFILES = {"sender": (64501, "192.0.2.2"), "recipient": (64502, "192.0.2.3")}
CONTEXT = {
    "schemaVersion": 1, "fixture": "generated-two-peer-ipv6",
    "controlSocket": CONTROL_SOCKET, "routerAddress": ROUTER_ADDRESS, "routerASN": 64500,
    "senderAddress": SENDER_ADDRESS, "senderASN": 64501,
    "recipientAddress": RECIPIENT_ADDRESS, "recipientASN": 64502,
    "peerConnectTarget": "[::1]:13790",
}
FAILURE_CODES = frozenset((
    "bgp-generated6-family bgp-generated6-prefix bgp-generated6-next-hop "
    "bgp-generated6-attribute bgp-generated6-classic-nlri bgp-generated6-capability "
    "bgp-generated6-frame bgp-generated6-state bgp-generated6-bound "
    "bgp-generated6-native-source bgp-generated6-connect bgp-generated6-socket"
).split()) | base.FAILURE_CODES


def fail(code):
    raise FixtureFailure(code)


def safe_failure_code(error):
    if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURE_CODES:
        return error.args[0]
    return "unclassified"


def nlri(labels):
    by_label = {label: ipaddress.IPv6Network(prefix) for prefix, label in PREFIXES.items()}
    if len(labels) > 2 or len(set(labels)) != len(labels) or any(label not in by_label for label in labels):
        fail("bgp-generated6-prefix")
    return b"".join(bytes((by_label[label].prefixlen,)) + by_label[label].network_address.packed[:6] for label in labels)


def parse_prefixes(raw):
    result, at = [], 0
    while at < len(raw):
        bits = raw[at]
        at += 1
        size = (bits + 7) // 8
        if bits > 128 or at + size > len(raw):
            fail("bgp-generated6-prefix")
        data = raw[at:at + size]
        at += size
        if bits % 8 and data[-1] & ((1 << (8 - bits % 8)) - 1):
            fail("bgp-generated6-prefix")
        prefix = str(ipaddress.IPv6Network((int.from_bytes(data.ljust(16, bytes((0,))), "big"), bits)))
        label = PREFIXES.get(prefix)
        if label is None or label in result or len(result) >= 2:
            fail("bgp-generated6-prefix")
        result.append(label)
    return result


def attribute(code, value):
    if code not in (14, 15) or len(value) > 255:
        fail("bgp-generated6-attribute")
    return bytes((0x80, code, len(value))) + value


def eor():
    attrs = attribute(15, bytes((0, 2, 1)))
    return base.packet(2, bytes((0, 0)) + struct.pack("!H", len(attrs)) + attrs)


def announcement():
    attrs = bytes.fromhex("400101004002040201") + struct.pack("!H", 64501)
    reach = bytes((0, 2, 1, 16)) + ipaddress.IPv6Address(SENDER_ADDRESS).packed + bytes((0,)) + nlri(("a", "b"))
    attrs += attribute(14, reach)
    return base.packet(2, bytes((0, 0)) + struct.pack("!H", len(attrs)) + attrs)


def parse_attributes(raw):
    at, ordinary = 0, b""
    seen, mp = set(), {}
    while at < len(raw):
        start = at
        if len(raw) - at < 3:
            fail("bgp-generated6-attribute")
        flags, code = raw[at], raw[at + 1]
        at += 2
        width = 2 if flags & 0x10 else 1
        if flags & 0x0f or not code or at + width > len(raw):
            fail("bgp-generated6-attribute")
        size = int.from_bytes(raw[at:at + width], "big")
        at += width
        if at + size > len(raw) or code in seen or len(seen) >= 64:
            fail("bgp-generated6-attribute")
        seen.add(code)
        value = raw[at:at + size]
        at += size
        if code == 3:
            fail("bgp-generated6-classic-nlri")
        if code not in (14, 15):
            ordinary += raw[start:at]
            continue
        if flags & 0xe0 != 0x80 or len(value) < 3 or value[:3] != bytes((0, 2, 1)):
            fail("bgp-generated6-family")
        if code == 15:
            mp["withdrawals"] = parse_prefixes(value[3:])
            continue
        if len(value) < 5 or value[3] not in (16, 32) or len(value) < value[3] + 5:
            fail("bgp-generated6-next-hop")
        length = value[3]
        global_hop = ipaddress.IPv6Address(value[4:20])
        if global_hop.is_unspecified or global_hop.is_multicast or global_hop.is_link_local or global_hop.ipv4_mapped is not None:
            fail("bgp-generated6-next-hop")
        mp["nextHop"] = str(global_hop)
        if length == 32:
            local_hop = ipaddress.IPv6Address(value[20:36])
            if not local_hop.is_link_local:
                fail("bgp-generated6-next-hop")
            mp["linkLocalNextHop"] = str(local_hop)
        # RFC 4760 requires the sender to zero this reserved octet and asks
        # receivers to ignore it; it carries no route or authorization data.
        mp["announced"] = parse_prefixes(value[5 + length:])
    values = base.parse_attributes(ordinary)
    for name in ("nextHop", "linkLocalNextHop"):
        if name in mp:
            values[name] = mp[name]
    withdrawn, announced = mp.get("withdrawals", []), mp.get("announced", [])
    if announced and any(name not in values for name in ("origin", "path", "nextHop")):
        fail("bgp-generated6-attribute")
    return withdrawn, announced, values, seen == {15} and not withdrawn


def parse_update(body):
    if len(body) < 4 or body[:2] != bytes((0, 0)):
        fail("bgp-generated6-classic-nlri")
    length = int.from_bytes(body[2:4], "big")
    if length != len(body) - 4:
        fail("bgp-generated6-classic-nlri")
    # A classic empty IPv4 UPDATE is not an IPv6 EOR.
    return parse_attributes(body[4:])


def local_open(role):
    if role not in PROFILES:
        fail("bgp-generated6-state")
    asn, router_id = PROFILES[role]
    # Only MP IPv6-unicast and route-refresh are offered. ADD-PATH and AS4
    # are not negotiated, so AS_PATH remains two octets per ASN in this case.
    capabilities = bytes((2, 8, 1, 4, 0, 2, 0, 1, 2, 0))
    return base.packet(1, struct.pack("!BHH4sB", 4, asn, 90, ipaddress.IPv4Address(router_id).packed, len(capabilities)) + capabilities)


def open_reply(role, body):
    if len(body) < 10 or body[0] != 4 or int.from_bytes(body[1:3], "big") != 64500 or body[5:9] != bytes((192, 0, 2, 1)) or len(body) != 10 + body[9]:
        fail("bgp-generated6-frame")
    hold = int.from_bytes(body[3:5], "big")
    if 0 < hold < 3:
        fail("bgp-generated6-capability")
    at, mp6 = 10, False
    while at < len(body):
        if at + 2 > len(body) or body[at] != 2:
            fail("bgp-generated6-capability")
        end = at + 2 + body[at + 1]
        at += 2
        if end > len(body):
            fail("bgp-generated6-capability")
        while at < end:
            if at + 2 > end or at + 2 + body[at + 1] > end:
                fail("bgp-generated6-capability")
            code, size = body[at], body[at + 1]
            value = body[at + 2:at + 2 + size]
            at += 2 + size
            if code == 1:
                if size != 4:
                    fail("bgp-generated6-capability")
                if value[:2] == bytes((0, 2)) and value[3] == 1:
                    if mp6:
                        fail("bgp-generated6-capability")
                    mp6 = True
    if not mp6:
        fail("bgp-generated6-capability")
    return local_open(role)


class Protocol(base.Protocol):
    """Tracks actual IPv6 UPDATEs; inherits only bounded event/counter copying."""
    def __init__(self, role, sequence=None):
        super().__init__(role, sequence)
        self.ipv6_negotiated = False
        self.opens_sent = 0

    def start(self):
        """Send exactly one OPEN after the fixed active TCP connect succeeds."""
        if self.opens_sent or self.opens or self.established or self.ended:
            fail("bgp-generated6-state")
        self.opens_sent = 1
        return (local_open(self.role),)

    def receive(self, data):
        self.total += len(data)
        self.buffer += data
        if self.total > 1 << 20 or len(self.buffer) > 16384:
            fail("bgp-generated6-bound")
        replies = []
        while len(self.buffer) >= 19:
            if self.buffer[:16] != bytes((255,)) * 16:
                fail("bgp-generated6-frame")
            length, kind = struct.unpack("!HB", self.buffer[16:19])
            if not 19 <= length <= 4096:
                fail("bgp-generated6-frame")
            if len(self.buffer) < length:
                break
            body, self.buffer = self.buffer[19:length], self.buffer[length:]
            self.messages += 1
            if self.messages > 1024:
                fail("bgp-generated6-bound")
            if kind == 1:
                if self.opens:
                    fail("bgp-generated6-state")
                reply = open_reply(self.role, body)
                self.ipv6_negotiated = True
                self.opens += 1
                if not self.opens_sent:
                    self.opens_sent = 1
                    replies.append(reply)
                replies.append(base.packet(4))
            elif kind == 4:
                if body or not self.opens or not self.ipv6_negotiated or self.ended:
                    fail("bgp-generated6-state")
                if not self.established:
                    self.established = True
                    replies.append(eor())
            elif kind == 2:
                if not self.established or not self.ipv6_negotiated or self.ended:
                    fail("bgp-generated6-state")
                withdrawn, announced, attributes, end_of_rib = parse_update(body)
                self.updates_received += 1
                self.eors_received += int(end_of_rib)
                for label in withdrawn:
                    present = label in self.rib
                    self.rib.pop(label, None)
                    self.event("withdraw", label, removed=present)
                for label in announced:
                    route = {"prefixId": label, **copy.deepcopy(attributes)}
                    self.rib[label] = route
                    self.event("announce", label, route=route)
            elif kind == 5:
                self.refreshes += 1
                fail("bgp-generated-refresh")
            elif kind == 3:
                if len(body) < 2 or not self.expected_close:
                    fail("bgp-generated-notification")
                self.ended = True
                self.ends += 1
                self.established = False
            else:
                fail("bgp-generated6-state")
        return replies

    def announce_once(self):
        if self.role != "sender" or not self.established or self.ended or not self.ipv6_negotiated:
            fail("bgp-generated-not-ready")
        if self.announcements_sent:
            fail("bgp-generated-already-announced")
        self.announcements_sent = 1
        return (announcement(), eor())

    def snapshot(self):
        value = super().snapshot()
        value["ipv6Negotiated"] = self.ipv6_negotiated
        value["opensSent"] = self.opens_sent
        return value


def parse_control_request(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                fail("bgp-generated-control")
            value[key] = item
        return value
    try:
        if len(raw) >= 1024:
            fail("bgp-generated-control")
        value = json.loads(raw, object_pairs_hook=unique)
    except (ValueError, UnicodeError):
        fail("bgp-generated-control")
    if type(value) is not dict or set(value) != {"action"} or type(value["action"]) is not str or value["action"] not in ("snapshot", "connect-once", "announce-once", "verify-native-sources", "allow-shutdown"):
        fail("bgp-generated-control")
    return value["action"]


class FixedPeers6(base.FixedPeers):
    """Two fixed IPv6 active clients and a private bounded control socket.

    The source verifier is a trusted harness callback with no command argument.
    Host socket addresses alone never set nativeSourcesVerified.
    """
    def __init__(self, verify_native_sources):
        if not callable(verify_native_sources):
            fail("bgp-generated6-native-source")
        self.lock = threading.RLock()
        self.stop, self.connect_requested = threading.Event(), threading.Event()
        self.sequence, self.failure = 0, ""
        self.announce_requested, self.native_sources_verified = False, False
        self.listeners, self.connections, self.created_paths = {}, {}, {}
        self.threads, self.control = [], None
        self.protocols = {role: Protocol(role, self._next_sequence) for role in PROFILES}
        self.connect_attempts = {role: 0 for role in PROFILES}
        self.socket_verified = {role: False for role in PROFILES}
        self.verify_native_sources = verify_native_sources
        self.deadline = time.monotonic() + 900
        try:
            root = os.lstat("/scratch")
            if not stat.S_ISDIR(root.st_mode) or root.st_uid != os.geteuid() or root.st_mode & 0o077:
                fail("bgp-generated-private-path")
            if os.path.lexists(CONTROL_SOCKET) or os.path.lexists(CONTEXT_FILE):
                fail("bgp-generated-private-path")
            self.control = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.control.bind(CONTROL_SOCKET)
            os.chmod(CONTROL_SOCKET, 0o600)
            self.created_paths[CONTROL_SOCKET] = os.lstat(CONTROL_SOCKET).st_ino
            self.control.listen(2)
            self.control.settimeout(0.2)
            descriptor = os.open(CONTEXT_FILE, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            try:
                encoded = json.dumps(CONTEXT, separators=(",", ":")).encode()
                if os.write(descriptor, encoded) != len(encoded):
                    fail("bgp-generated-private-file")
                os.fsync(descriptor)
                self.created_paths[CONTEXT_FILE] = os.fstat(descriptor).st_ino
            finally:
                os.close(descriptor)
            for role in PROFILES:
                self._start(self._serve_peer, role)
            self._start(self._serve_control)
        except Exception as error:
            self.close()
            code = safe_failure_code(error)
            raise FixtureFailure(code if code != "unclassified" else "bgp-generated6-socket") from None

    def _failed(self, error):
        code = safe_failure_code(error)
        with self.lock:
            if not self.failure and not self.stop.is_set():
                self.failure = code if code != "unclassified" else "bgp-generated-io"

    def _serve_peer(self, role):
        connection = None
        try:
            while not self.connect_requested.wait(0.2):
                if self.stop.is_set():
                    return
                if time.monotonic() >= self.deadline:
                    fail("bgp-generated-deadline")
            if self.stop.is_set():
                return
            address = SENDER_ADDRESS if role == "sender" else RECIPIENT_ADDRESS
            connection = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
            connection.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
            connection.settimeout(5)
            connection.bind((address, 0, 0, 0))
            with self.lock:
                self.connect_attempts[role] += 1
            connection.connect(("::1", 13790, 0, 0))
            if connection.getsockname()[0] != address or connection.getpeername()[:2] != ("::1", 13790):
                fail("bgp-generated6-connect")
            with self.lock:
                self.connections[role] = connection
                self.socket_verified[role] = True
                opened = self.protocols[role].start()
            for wire in opened:
                connection.sendall(wire)
            connection.settimeout(0.2)
            last_keepalive = time.monotonic()
            protocol = self.protocols[role]
            while not self.stop.is_set() and time.monotonic() < self.deadline:
                try:
                    data = connection.recv(8192)
                    if not data:
                        with self.lock:
                            protocol.ends += 1
                            protocol.established = False
                            if not protocol.expected_close:
                                fail("bgp-generated-ended")
                        return
                except socket.timeout:
                    data = b""
                with self.lock:
                    replies = protocol.receive(data)
                    if role == "sender" and self.announce_requested and not protocol.announcements_sent:
                        replies.extend(protocol.announce_once())
                    established, ended = protocol.established, protocol.ended
                for reply in replies:
                    connection.sendall(reply)
                if ended:
                    return
                if established and time.monotonic() - last_keepalive >= 15:
                    connection.sendall(base.packet(4))
                    last_keepalive = time.monotonic()
            if not self.stop.is_set():
                fail("bgp-generated-deadline")
        except Exception as error:
            if not self.stop.is_set():
                self._failed(error)
        finally:
            if connection is not None:
                connection.close()

    def _response(self, action):
        if action == "verify-native-sources":
            with self.lock:
                if self.failure or not all(p.established and p.ipv6_negotiated and p.opens == 1 and p.opens_sent == 1 and not p.ends and self.connect_attempts[role] == 1 and self.socket_verified[role] for role, p in self.protocols.items()):
                    fail("bgp-generated-not-ready")
            # Never hold the wire-reader lock during bounded serial readback.
            if self.verify_native_sources() is not True:
                fail("bgp-generated6-native-source")
            with self.lock:
                if self.failure or not all(p.established and not p.ends for p in self.protocols.values()):
                    fail("bgp-generated6-native-source")
                self.native_sources_verified = True
            action = "snapshot"
        with self.lock:
            if action == "connect-once":
                if self.connect_requested.is_set() or self.failure or any(p.opens for p in self.protocols.values()) or any(self.connect_attempts.values()):
                    fail("bgp-generated6-connect")
                self.connect_requested.set()
                action = "snapshot"
            if action == "announce-once" and not self.native_sources_verified:
                fail("bgp-generated6-native-source")
            value = super()._response(action)
            value["nativeSourcesVerified"] = self.native_sources_verified
            for role, peer in value["peers"].items():
                peer["connectAttempts"] = self.connect_attempts[role]
                peer["ipv6SocketVerified"] = self.socket_verified[role]
            return value

    def _serve_control(self):
        while not self.stop.is_set() and time.monotonic() < self.deadline:
            client = None
            try:
                try:
                    client, _ = self.control.accept()
                except socket.timeout:
                    continue
                client.settimeout(2)
                credentials = client.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i"))
                if struct.unpack("3i", credentials)[1] != os.geteuid():
                    fail("bgp-generated-control")
                raw = b""
                while not raw.endswith(bytes((10,))):
                    data = client.recv(1024 - len(raw))
                    if not data or len(raw) + len(data) >= 1024:
                        fail("bgp-generated-control")
                    raw += data
                response = self._response(parse_control_request(raw))
                encoded = json.dumps(response, separators=(",", ":")).encode() + bytes((10,))
                if len(encoded) > 65536:
                    fail("bgp-generated-events")
                client.sendall(encoded)
            except Exception as error:
                if not self.stop.is_set():
                    self._failed(error)
            finally:
                if client is not None:
                    client.close()
