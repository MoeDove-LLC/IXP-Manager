"""Bounded, fixed two-peer IPv4 wire fixture for generated RouterOS policy.

Only the isolated harness instantiates FixedPeers. There are no configurable
addresses, commands, routes or credentials. The private control socket is for a
reviewed native Go test; no route bytes or target data are printed.
"""
import copy
import ipaddress
import json
import os
import socket
import stat
import struct
import threading
import time

from console import FixtureFailure

CONTROL_SOCKET = "/scratch/bgp-generated.sock"
CONTEXT_FILE = "/scratch/bgp-generated-context.json"
CONTEXT = {
    "schemaVersion": 1, "fixture": "generated-two-peer-ipv4",
    "controlSocket": CONTROL_SOCKET, "routerAddress": "10.0.2.15", "routerASN": 64500,
    "senderAddress": "10.0.2.100", "senderASN": 64501, "senderListener": "127.0.0.1:11790",
    "recipientAddress": "10.0.2.101", "recipientASN": 64502, "recipientListener": "127.0.0.1:11791",
}
PREFIXES = {"203.0.113.0/24": "a", "198.51.100.0/24": "b"}
PROFILES = {
    "sender": (64501, "192.0.2.2", 11790),
    "recipient": (64502, "192.0.2.3", 11791),
}
FAILURE_CODES = frozenset("""
 bgp-generated-frame bgp-generated-bytes bgp-generated-messages bgp-generated-open
 bgp-generated-capability bgp-generated-refresh bgp-generated-notification
 bgp-generated-state bgp-generated-update-length bgp-generated-prefix
 bgp-generated-attribute-length bgp-generated-attribute-flags bgp-generated-attribute-duplicate
 bgp-generated-attribute-unknown bgp-generated-attribute-required bgp-generated-path
 bgp-generated-next-hop bgp-generated-origin bgp-generated-multiprotocol bgp-generated-events
 bgp-generated-io bgp-generated-ended bgp-generated-deadline bgp-generated-listener
 bgp-generated-control bgp-generated-not-ready bgp-generated-already-announced
 bgp-generated-private-path bgp-generated-private-file bgp-generated-context
""".split())


def safe_failure_code(error):
    if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURE_CODES:
        return error.args[0]
    return "unclassified"


def fail(code):
    raise FixtureFailure(code)


def packet(kind, body=b""):
    if kind not in (1, 2, 4) or len(body) + 19 > 4096:
        fail("bgp-generated-frame")
    return b"\xff" * 16 + struct.pack("!HB", len(body) + 19, kind) + body


def announcement():
    attrs = b"\x40\x01\x01\x00\x40\x02\x04\x02\x01" + struct.pack("!H", 64501)
    attrs += b"\x40\x03\x04" + ipaddress.IPv4Address("10.0.2.100").packed
    nlri = b"\x18\xcb\x00\x71\x18\xc6\x33\x64"
    return packet(2, b"\x00\x00" + struct.pack("!H", len(attrs)) + attrs + nlri)


def parse_prefixes(raw):
    result = []
    at = 0
    while at < len(raw):
        bits = raw[at]
        at += 1
        size = (bits + 7) // 8
        if bits > 32 or at + size > len(raw):
            fail("bgp-generated-prefix")
        data = raw[at:at + size]
        at += size
        if bits % 8 and data[-1] & ((1 << (8 - bits % 8)) - 1):
            fail("bgp-generated-prefix")
        prefix = str(ipaddress.IPv4Network((int.from_bytes(data.ljust(4, b"\x00"), "big"), bits)))
        label = PREFIXES.get(prefix)
        if label is None or label in result or len(result) >= 2:
            fail("bgp-generated-prefix")
        result.append(label)
    return result


def parse_attributes(raw):
    at = 0
    seen = set()
    values = {}
    while at < len(raw):
        if len(raw) - at < 3:
            fail("bgp-generated-attribute-length")
        flags, code = raw[at], raw[at + 1]
        at += 2
        if flags & 0x0f or code == 0:
            fail("bgp-generated-attribute-flags")
        length_bytes = 2 if flags & 0x10 else 1
        if at + length_bytes > len(raw):
            fail("bgp-generated-attribute-length")
        length = int.from_bytes(raw[at:at + length_bytes], "big")
        at += length_bytes
        if at + length > len(raw) or code in seen or len(seen) >= 64:
            fail("bgp-generated-attribute-duplicate" if code in seen else "bgp-generated-attribute-length")
        seen.add(code)
        value = raw[at:at + length]
        at += length
        if code in (1, 2, 3, 5, 6) and flags & 0xe0 != 0x40:
            fail("bgp-generated-attribute-flags")
        if code == 1:
            if len(value) != 1 or value[0] > 2:
                fail("bgp-generated-origin")
            values["origin"] = value[0]
        elif code == 2:
            segments, offset, total = [], 0, 0
            while offset < len(value):
                if len(value) - offset < 2:
                    fail("bgp-generated-path")
                kind, count = value[offset], value[offset + 1]
                offset += 2
                if kind not in (1, 2, 3, 4) or count == 0 or offset + count * 2 > len(value):
                    fail("bgp-generated-path")
                total += count
                if total > 64 or len(segments) >= 64:
                    fail("bgp-generated-path")
                asns = [int.from_bytes(value[i:i + 2], "big") for i in range(offset, offset + count * 2, 2)]
                offset += count * 2
                segments.append({"type": kind, "asns": asns})
            values["path"] = segments
        elif code == 3:
            if len(value) != 4:
                fail("bgp-generated-next-hop")
            address = ipaddress.IPv4Address(value)
            if address.is_unspecified or address.is_multicast:
                fail("bgp-generated-next-hop")
            values["nextHop"] = str(address)
        elif code == 4:
            if flags & 0xe0 != 0x80 or len(value) != 4:
                fail("bgp-generated-attribute-flags")
        elif code == 5:
            if len(value) != 4:
                fail("bgp-generated-attribute-length")
        elif code == 6:
            if value:
                fail("bgp-generated-attribute-length")
        elif code in (14, 15):
            # No multiprotocol or add-path capability is negotiated here.
            fail("bgp-generated-multiprotocol")
        elif not flags & 0x80:
            fail("bgp-generated-attribute-unknown")
        # Other structurally bounded optional attributes are not a proof of
        # community, MED, AS4 or other transparency. This fixture asserts only
        # the explicitly parsed ORIGIN/AS_PATH/NEXT_HOP and classic IPv4 NLRI.
    return values


def parse_update(body):
    if len(body) < 4:
        fail("bgp-generated-update-length")
    withdrawn_size = int.from_bytes(body[:2], "big")
    if withdrawn_size + 4 > len(body):
        fail("bgp-generated-update-length")
    withdrawals = parse_prefixes(body[2:2 + withdrawn_size])
    at = 2 + withdrawn_size
    attrs_size = int.from_bytes(body[at:at + 2], "big")
    at += 2
    if at + attrs_size > len(body):
        fail("bgp-generated-update-length")
    attributes = parse_attributes(body[at:at + attrs_size])
    announced = parse_prefixes(body[at + attrs_size:])
    if announced and any(name not in attributes for name in ("origin", "path", "nextHop")):
        fail("bgp-generated-attribute-required")
    return withdrawals, announced, attributes, not withdrawals and not announced and not attrs_size


def parse_control_request(raw):
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                fail("bgp-generated-control")
            result[key] = value
        return result
    try:
        if len(raw) >= 1024:
            fail("bgp-generated-control")
        request = json.loads(raw, object_pairs_hook=unique_object)
    except (ValueError, UnicodeError):
        fail("bgp-generated-control")
    if type(request) is not dict or set(request) != {"action"} or type(request["action"]) is not str or request["action"] not in ("snapshot", "announce-once", "allow-shutdown"):
        fail("bgp-generated-control")
    return request["action"]


class Protocol:
    """One fixed peer's bounded wire state; callers serialize receive/snapshot."""
    def __init__(self, role, sequence=None):
        if role not in PROFILES:
            fail("bgp-generated-context")
        self.role = role
        self.sequence = sequence
        self.local_sequence = 0
        self.buffer = b""
        self.total = self.messages = self.opens = self.announcements_sent = 0
        self.refreshes = self.ends = self.updates_received = self.eors_received = 0
        self.established = self.expected_close = self.ended = False
        self.rib = {}
        self.events = []

    def event(self, kind, label, route=None, removed=False):
        if len(self.events) >= 128:
            fail("bgp-generated-events")
        self.local_sequence += 1
        number = self.sequence() if self.sequence else self.local_sequence
        value = {"sequence": number, "kind": kind, "prefixId": label, "removed": removed}
        if route is not None:
            value["route"] = copy.deepcopy(route)
        self.events.append(value)

    def receive(self, data):
        self.total += len(data)
        self.buffer += data
        if self.total > 1 << 20 or len(self.buffer) > 16384:
            fail("bgp-generated-bytes")
        replies = []
        while len(self.buffer) >= 19:
            if self.buffer[:16] != b"\xff" * 16:
                fail("bgp-generated-frame")
            length, kind = struct.unpack("!HB", self.buffer[16:19])
            if length < 19 or length > 4096:
                fail("bgp-generated-frame")
            if len(self.buffer) < length:
                break
            body, self.buffer = self.buffer[19:length], self.buffer[length:]
            self.messages += 1
            if self.messages > 1024:
                fail("bgp-generated-messages")
            if kind == 1:
                if self.opens or len(body) < 10 or body[0] != 4 or struct.unpack("!H", body[1:3])[0] != 64500 or body[5:9] != ipaddress.IPv4Address("192.0.2.1").packed or len(body) != 10 + body[9]:
                    fail("bgp-generated-open")
                hold = int.from_bytes(body[3:5], "big")
                if 0 < hold < 3:
                    fail("bgp-generated-open")
                offset = 10
                while offset < len(body):
                    if offset + 2 > len(body):
                        fail("bgp-generated-capability")
                    param, size = body[offset], body[offset + 1]
                    offset += 2
                    if param != 2 or offset + size > len(body):
                        fail("bgp-generated-capability")
                    end = offset + size
                    while offset < end:
                        if offset + 2 > end or offset + 2 + body[offset + 1] > end:
                            fail("bgp-generated-capability")
                        offset += 2 + body[offset + 1]
                asn, router_id, _ = PROFILES[self.role]
                # Only route-refresh is advertised. All fixture ASNs fit in
                # the classic 2-octet AS_PATH format; this is not AS4 coverage.
                caps = b"\x02\x02\x02\x00"
                opened = struct.pack("!BHH4sB", 4, asn, 90, ipaddress.IPv4Address(router_id).packed, len(caps)) + caps
                self.opens += 1
                replies.extend((packet(1, opened), packet(4)))
            elif kind == 4:
                if body or not self.opens or self.ended:
                    fail("bgp-generated-state")
                if not self.established:
                    self.established = True
                    # Empty UPDATE/EOR contains no advertised prefix.
                    replies.append(packet(2, b"\x00\x00\x00\x00"))
            elif kind == 2:
                if not self.established or self.ended:
                    fail("bgp-generated-state")
                withdrawals, announced, attributes, eor = parse_update(body)
                self.updates_received += 1
                if eor:
                    self.eors_received += 1
                for label in withdrawals:
                    removed = label in self.rib
                    self.rib.pop(label, None)
                    self.event("withdraw", label, removed=removed)
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
                fail("bgp-generated-state")
        return replies

    def announce_once(self):
        if self.role != "sender" or not self.established or self.ended:
            fail("bgp-generated-not-ready")
        if self.announcements_sent:
            fail("bgp-generated-already-announced")
        self.announcements_sent = 1
        return (announcement(), packet(2, b"\x00\x00\x00\x00"))

    def snapshot(self):
        return {"established": self.established, "opens": self.opens,
                "announcementsSent": self.announcements_sent, "refreshes": self.refreshes,
                "ends": self.ends, "updatesReceived": self.updates_received, "eorsReceived": self.eors_received,
                "rib": [copy.deepcopy(self.rib[k]) for k in sorted(self.rib)], "events": copy.deepcopy(self.events)}


class FixedPeers:
    """Two immutable listeners plus a private closed-vocabulary test socket."""
    def __init__(self):
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.sequence = 0
        self.failure = ""
        self.announce_requested = False
        self.listeners = {}
        self.connections = {}
        self.threads = []
        self.control = None
        self.created_paths = {}
        self.protocols = {role: Protocol(role, self._next_sequence) for role in PROFILES}
        self.deadline = time.monotonic() + 900
        try:
            root = os.lstat("/scratch")
            if not stat.S_ISDIR(root.st_mode) or root.st_uid != os.geteuid() or root.st_mode & 0o077:
                fail("bgp-generated-private-path")
            for role, (_, _, port) in PROFILES.items():
                listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
                self.listeners[role] = listener
                listener.bind(("127.0.0.1", port))
                listener.listen(1)
                listener.settimeout(0.2)
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
                data = json.dumps(CONTEXT, separators=(",", ":")).encode()
                if os.write(descriptor, data) != len(data):
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
            raise FixtureFailure(code if code != "unclassified" else "bgp-generated-listener") from None

    def _start(self, function, *args):
        thread = threading.Thread(target=function, args=args, daemon=True)
        thread.start()
        self.threads.append(thread)

    def _next_sequence(self):
        self.sequence += 1
        return self.sequence

    def _failed(self, error):
        code = safe_failure_code(error)
        with self.lock:
            if not self.failure and not self.stop.is_set():
                self.failure = code if code != "unclassified" else "bgp-generated-io"

    def _serve_peer(self, role):
        connection = None
        try:
            while not self.stop.is_set() and time.monotonic() < self.deadline:
                try:
                    connection, address = self.listeners[role].accept()
                    if address[0] != "127.0.0.1":
                        fail("bgp-generated-io")
                    break
                except socket.timeout:
                    continue
            if connection is None:
                if not self.stop.is_set():
                    fail("bgp-generated-deadline")
                return
            self.connections[role] = connection
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
                    connection.sendall(packet(4))
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
        with self.lock:
            if action == "announce-once":
                if self.announce_requested:
                    fail("bgp-generated-already-announced")
                if self.failure or not all(p.established for p in self.protocols.values()):
                    fail("bgp-generated-not-ready")
                self.announce_requested = True
            elif action == "allow-shutdown":
                for protocol in self.protocols.values():
                    protocol.expected_close = True
            elif action != "snapshot":
                fail("bgp-generated-control")
            return {"schemaVersion": 1, "ok": not self.failure, "failureCode": self.failure,
                    "sequence": self.sequence, "peers": {role: p.snapshot() for role, p in self.protocols.items()}}

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
                while not raw.endswith(b"\n"):
                    data = client.recv(1024 - len(raw))
                    if not data or len(raw) + len(data) >= 1024:
                        fail("bgp-generated-control")
                    raw += data
                response = self._response(parse_control_request(raw))
                encoded = json.dumps(response, separators=(",", ":")).encode() + b"\n"
                if len(encoded) > 65536:
                    fail("bgp-generated-events")
                client.sendall(encoded)
            except Exception as error:
                if not self.stop.is_set():
                    self._failed(error)
            finally:
                if client is not None:
                    client.close()

    def close(self):
        self.stop.set()
        if self.control is not None:
            self.control.close()
        for listener in self.listeners.values():
            listener.close()
        for connection in list(self.connections.values()):
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        for thread in self.threads:
            if thread.ident is not None:
                thread.join(timeout=3)
        for path, inode in self.created_paths.items():
            try:
                if os.lstat(path).st_ino == inode:
                    os.unlink(path)
            except FileNotFoundError:
                pass
