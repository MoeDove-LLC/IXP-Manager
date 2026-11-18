"""Fixed single-peer IPv4 cache probe, only inside the disposable namespace.

This is a bounded fixture speaker, not a configurable or production BGP service.
Only two documentation prefixes are ever announced; no raw routes are logged.
"""
import ipaddress
import re
import socket
import struct
import subprocess
import threading
import time

from console import Console, FixtureFailure
from ssh_fixture import SSH_ENV

SSH_OWNER_ENV = dict(SSH_ENV, TERM="dumb", SSH_ASKPASS_REQUIRE="never")
PREFIXES = ("203.0.113.0/24", "198.51.100.0/24")
FAILURE_CODES = frozenset((
    "bgp-frame-format", "bgp-byte-limit", "bgp-message-limit", "bgp-open-format", "bgp-unexpected-refresh",
    "bgp-notification", "bgp-unexpected-message", "bgp-peer-io", "bgp-peer-ended", "bgp-peer-deadline",
    "bgp-peer-not-established", "bgp-command-rejected", "bgp-route-projection", "bgp-route-readback",
    "bgp-owner-not-current", "bgp-version", "bgp-owner-release", "bgp-counter-evidence", "bgp-proof-schema",
))
ROUTE_QUERY = ':foreach r in=[/routing/route/print as-value where routing-table=mdlab_bgp] do={ :put ("prefix=" . ($r->"dst-address") . " filtered=" . ($r->"filtered") . " active=" . ($r->"active") . " unreachable=" . ($r->"unreachable")) }'
COMMANDS = (
    "/routing/table/add name=mdlab_bgp",
    '/routing/filter/rule/add chain=mdlab_a rule="if (dst == 203.0.113.0/24) { accept; } else { reject; }"',
    '/routing/filter/rule/add chain=mdlab_b rule="if (dst == 198.51.100.0/24) { accept; } else { reject; }"',
    '/routing/filter/rule/add chain=mdlab_in comment=mdlab_root rule="jump mdlab_a;"',
    '/routing/filter/rule/add chain=mdlab_out rule="reject;"',
    "/routing/bgp/instance/add name=mdlab_bgp as=64500 router-id=192.0.2.1 routing-table=mdlab_bgp",
    "/routing/bgp/connection/add name=mdlab_peer instance=mdlab_bgp remote.address=10.0.2.100 remote.as=64501 local.address=10.0.2.15 local.role=ebgp-rs routing-table=mdlab_bgp afi=ip input.filter=mdlab_in output.filter-chain=mdlab_out connect=yes listen=no nexthop-choice=propagate disabled=no",
)
PROOF = {
    "bgp_cache_fixture": "fixed-single-peer-ipv4",
    "bgp_baseline_verified": True,
    "bgp_switched_without_refresh": True,
    "bgp_rollback_readback_verified": True,
    "bgp_disabled_cache_empty": True,
    "bgp_open_once": True,
    "bgp_update_once": True,
    "bgp_no_refresh": True,
    "bgp_generated_policy_verified": False,
    "bgp_dual_stack_verified": False,
}


def safe_failure_code(error):
    if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURE_CODES:
        return error.args[0]
    return "unclassified"


def packet(kind, body=b""):
    if kind not in (1, 2, 4) or len(body) + 19 > 4096:
        raise FixtureFailure("bgp-frame-format")
    return b"\xff" * 16 + struct.pack("!HB", 19 + len(body), kind) + body


def announcement():
    attributes = b"\x40\x01\x01\x00\x40\x02\x04\x02\x01" + struct.pack("!H", 64501)
    attributes += b"\x40\x03\x04" + ipaddress.IPv4Address("10.0.2.100").packed
    prefixes = b"\x18" + ipaddress.IPv4Address("203.0.113.0").packed[:3]
    prefixes += b"\x18" + ipaddress.IPv4Address("198.51.100.0").packed[:3]
    return packet(2, b"\x00\x00" + struct.pack("!H", len(attributes)) + attributes + prefixes)


class Protocol:
    """Pure, bounded codec/state machine; the socket adapter supplies bytes."""
    def __init__(self):
        self.buffer = b""
        self.total = self.messages = self.opens = self.updates = self.refreshes = 0
        self.sent = self.expected_close = self.ended = False

    def receive(self, data):
        self.total += len(data)
        self.buffer += data
        if self.total > 1 << 20 or len(self.buffer) > 16384:
            raise FixtureFailure("bgp-byte-limit")
        replies = []
        while len(self.buffer) >= 19:
            if self.buffer[:16] != b"\xff" * 16:
                raise FixtureFailure("bgp-frame-format")
            length, kind = struct.unpack("!HB", self.buffer[16:19])
            if length < 19 or length > 4096:
                raise FixtureFailure("bgp-frame-format")
            if len(self.buffer) < length:
                break
            body, self.buffer = self.buffer[19:length], self.buffer[length:]
            self.messages += 1
            if self.messages > 256:
                raise FixtureFailure("bgp-message-limit")
            if kind == 1:
                if self.opens or len(body) < 10 or body[0] != 4 or struct.unpack("!H", body[1:3])[0] != 64500 or len(body) != 10 + body[9]:
                    raise FixtureFailure("bgp-open-format")
                capabilities = b"\x02\x02\x02\x00"
                opened = struct.pack("!BHH4sB", 4, 64501, 90, ipaddress.IPv4Address("192.0.2.2").packed, len(capabilities)) + capabilities
                replies.append(packet(1, opened) + packet(4))
                self.opens += 1
            elif kind == 4:
                if body or not self.opens:
                    raise FixtureFailure("bgp-frame-format")
                if not self.sent:
                    replies.append(announcement() + packet(2, b"\x00\x00\x00\x00"))
                    self.sent = True
                    self.updates += 1
            elif kind == 5:
                self.refreshes += 1
                # A refresh would invalidate this precise cached-route case.
                # Do not reannounce and then label it a no-refresh success.
                raise FixtureFailure("bgp-unexpected-refresh")
            elif kind == 3:
                if len(body) < 2 or not self.expected_close:
                    raise FixtureFailure("bgp-notification")
                self.ended = True
            elif kind != 2 or not self.opens:
                raise FixtureFailure("bgp-unexpected-message")
        return replies


class FixedPeer:
    def __init__(self):
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            self.listener.bind(("127.0.0.1", 11790))
            self.listener.listen(1)
            self.listener.settimeout(0.5)
        except Exception:
            self.listener.close()
            raise FixtureFailure("bgp-peer-io") from None
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.established = threading.Event()
        self.protocol = Protocol()
        self.failure = None
        self.connection = None
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def _serve(self):
        connection = None
        deadline = time.monotonic() + 450
        try:
            while not self.stop.is_set() and time.monotonic() < deadline:
                try:
                    connection, address = self.listener.accept()
                    if address[0] != "127.0.0.1":
                        raise FixtureFailure("bgp-peer-io")
                    break
                except socket.timeout:
                    continue
            if connection is None:
                if not self.stop.is_set():
                    raise FixtureFailure("bgp-peer-deadline")
                return
            self.connection = connection
            connection.settimeout(0.2)
            keepalive = time.monotonic()
            while not self.stop.is_set() and time.monotonic() < deadline:
                try:
                    data = connection.recv(8192)
                    if not data:
                        with self.lock:
                            if not self.protocol.expected_close:
                                raise FixtureFailure("bgp-peer-ended")
                        return
                except socket.timeout:
                    data = b""
                with self.lock:
                    replies = self.protocol.receive(data)
                    sent, ended = self.protocol.sent, self.protocol.ended
                for reply in replies:
                    connection.sendall(reply)
                if sent:
                    self.established.set()
                if ended:
                    return
                if sent and time.monotonic() - keepalive >= 15:
                    connection.sendall(packet(4))
                    keepalive = time.monotonic()
            if not self.stop.is_set():
                raise FixtureFailure("bgp-peer-deadline")
        except Exception as error:
            if not self.stop.is_set():
                code = safe_failure_code(error)
                with self.lock:
                    expected_end = self.protocol.expected_close and isinstance(error, (ConnectionResetError, BrokenPipeError))
                    if not expected_end:
                        self.failure = code if code != "unclassified" else "bgp-peer-io"
        finally:
            if connection is not None:
                connection.close()

    def require_single_announcement(self):
        with self.lock:
            if self.failure:
                raise FixtureFailure(self.failure)
            if (self.protocol.opens, self.protocol.updates, self.protocol.refreshes) != (1, 1, 0):
                raise FixtureFailure("bgp-counter-evidence")

    def allow_shutdown(self):
        with self.lock:
            self.protocol.expected_close = True

    def close(self):
        self.stop.set()
        self.listener.close()
        if self.connection is not None:
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        self.thread.join(timeout=2)


def command(console, value):
    output = console.command(value)
    if re.search(r"(?:^|[\r\n])\s*(?:failure:|syntax error|expected end|expected command name|invalid value|bad command name|no such item|not enough permissions|input does not match)", output, re.I):
        raise FixtureFailure("bgp-command-rejected")
    return output


def route_projection(output):
    rows = {}
    for line in output.splitlines():
        if not line.startswith("prefix="):
            continue
        match = re.fullmatch(r"prefix=(203\.0\.113\.0/24|198\.51\.100\.0/24) filtered=(true|false|) active=(true|false|) unreachable=(true|false|)", line)
        if not match or match[1] in rows or len(rows) >= 2:
            raise FixtureFailure("bgp-route-projection")
        rows[match[1]] = (match[2] == "true", match[3] == "true", match[4] == "true")
    return rows


def expected_routes(active):
    return {prefix: (prefix != active, prefix == active, False) for prefix in PREFIXES}


def wait_routes(console, peer, expected, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        peer.require_single_announcement()
        if route_projection(command(console, ROUTE_QUERY)) == expected:
            return
        time.sleep(0.5)
    raise FixtureFailure("bgp-route-readback")


def owner_arguments():
    args = ["/usr/bin/ssh", "-F", "/dev/null", "-tt", "-e", "none"]
    for option in (
        "BatchMode=yes", "RequestTTY=force", "StrictHostKeyChecking=yes", "UserKnownHostsFile=/scratch/known_hosts",
        "GlobalKnownHostsFile=/dev/null", "UpdateHostKeys=no", "IdentitiesOnly=yes", "IdentityAgent=none",
        "AddKeysToAgent=no", "ForwardAgent=no", "ClearAllForwardings=yes", "PermitLocalCommand=no", "ProxyCommand=none",
        "ProxyJump=none", "CanonicalizeHostname=no", "ControlMaster=no", "ControlPath=none", "ControlPersist=no",
        "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "PreferredAuthentications=publickey",
        "PubkeyAuthentication=yes", "NumberOfPasswordPrompts=0", "ConnectTimeout=10", "ConnectionAttempts=1",
        "ServerAliveInterval=5", "ServerAliveCountMax=2", "LogLevel=ERROR",
    ):
        args += ["-o", option]
    return args + ["-p", "12222", "-i", "/scratch/ssh_identity", "-l", "admin+ct200w60h", "--", "127.0.0.1"]


def owner_state(output, enabled, current):
    for field, expected in (("enabled", enabled), ("current", current)):
        matches = re.findall(r"(?:^|[\r\n])\s*" + field + r":\s*(yes|no)\s*(?=[\r\n]|$)", output)
        if matches != ["yes" if expected else "no"]:
            return False
    return True


def run_probe(console, peer):
    owner_process = None
    try:
        for value in COMMANDS:
            command(console, value)
        if not peer.established.wait(35):
            raise FixtureFailure("bgp-peer-not-established")
        baseline = expected_routes(PREFIXES[0])
        wait_routes(console, peer, baseline)
        owner_process = subprocess.Popen(owner_arguments(), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=SSH_OWNER_ENV, bufsize=0, close_fds=True)
        owner = Console(owner_process)
        owner.until(rb"\[admin@CHR\] >", 15)
        version = command(owner, ":put [/system/resource/get version]")
        if not re.search(r"(?:^|[\r\n])7\.24\.6(?: \((?:stable|long-term)\))?[\r\n]", version):
            raise FixtureFailure("bgp-version")
        owner.send(b"\x18")
        owner.until(rb"Taking Safe Mode session\.\.\. Success![ \t]*[\r\n]+\[admin@CHR\] <SAFE> ", 10)
        if not owner_state(command(owner, "/safe-mode/print"), True, True):
            raise FixtureFailure("bgp-owner-not-current")
        command(console, '/routing/filter/rule/set [/routing/filter/rule/find where comment=mdlab_root] rule="jump mdlab_b;"')
        wait_routes(console, peer, expected_routes(PREFIXES[1]))
        peer.require_single_announcement()
        owner.send(b"\x04")
        owner_process.wait(timeout=15)
        wait_routes(console, peer, baseline)
        if not owner_state(command(console, "/safe-mode/print"), False, False):
            raise FixtureFailure("bgp-owner-release")
        peer.allow_shutdown()
        command(console, "/routing/bgp/connection/disable [/routing/bgp/connection/find where name=mdlab_peer]")
        # Reproduce the independently observed three-second post-disable
        # check, rather than interpreting SSH/BGP close alone as cache loss.
        time.sleep(3)
        if route_projection(command(console, ROUTE_QUERY)):
            raise FixtureFailure("bgp-route-readback")
        peer.require_single_announcement()
        return dict(PROOF)
    finally:
        if owner_process is not None:
            if owner_process.poll() is None:
                owner_process.kill()
                owner_process.wait(timeout=5)
            owner_process.stdin.close()
            owner_process.stdout.close()


def proof_line(proof):
    if proof != PROOF or any(type(proof[key]) is not type(value) for key, value in PROOF.items()):
        raise FixtureFailure("bgp-proof-schema")
    return " ".join(key + "=" + (str(value).lower() if isinstance(value, bool) else value) for key, value in PROOF.items())
