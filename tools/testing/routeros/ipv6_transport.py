"""Fixed private IPv6 transport; never opens a host interface or default route."""
import ipaddress
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import threading
import time

from console import FixtureFailure

SOCKET = "/scratch/passt-bgp6.sock"
ADDRESSES = {"::1": 128, "fe80::1": 64, "2001:db8:ffff::100": 128, "2001:db8:ffff::101": 128}
EXPECTED_SOURCES = frozenset((("2001:db8:ffff::100", 64501), ("2001:db8:ffff::101", 64502)))
FAILURES = frozenset(("ipv6-address-boundary", "ipv6-default-route", "ipv6-passt-start", "ipv6-passt-privileges", "ipv6-passt-socket", "ipv6-guest-interface", "ipv6-native-session-sources"))


def capabilities_zero(status):
    for key in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"):
        values = re.findall(r"^" + key + r":\s*([0-9a-f]+)$", status, re.M)
        if len(values) != 1 or int(values[0], 16) != 0:
            return False
    return re.search(r"^NoNewPrivs:\s*1$", status, re.M) is not None


def verify_loopback():
    result = subprocess.run(["/usr/sbin/ip", "-j", "-6", "address", "show", "dev", "lo"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5, check=True)
    if len(result.stdout) > 8192:
        raise FixtureFailure("ipv6-address-boundary")
    values = json.loads(result.stdout)
    if len(values) != 1 or values[0].get("ifname") != "lo":
        raise FixtureFailure("ipv6-address-boundary")
    addresses = {}
    for address in values[0].get("addr_info", []):
        if address.get("family") != "inet6" or address.get("local") in addresses:
            raise FixtureFailure("ipv6-address-boundary")
        addresses[address.get("local")] = address.get("prefixlen")
    if addresses != ADDRESSES:
        raise FixtureFailure("ipv6-address-boundary")
    result = subprocess.run(["/usr/sbin/ip", "-6", "route", "show", "default"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5, check=True)
    if result.stdout.strip():
        raise FixtureFailure("ipv6-default-route")


def passt_arguments():
    # ICMPv6 must remain available for NDP. In this pinned passt version,
    # --no-icmp also disables NDP, even though --no-ndp is a separate option.
    return ["/lab/passt", "--foreground", "--quiet", "--runas", "0:0", "--ipv6-only", "--interface", "lo", "--address", "2001:db8:ffff::15/64", "--gateway", "2001:db8:ffff::1", "--no-map-gw", "--no-ra", "--no-dhcp", "--no-dhcpv6", "--no-udp", "--dns", "none", "--search", "none", "--tcp-ports", "::1/13790:179", "--socket-path", SOCKET, "--log-file", "/scratch/passt-bgp6.log", "--log-size", "1048576"]


class Passt:
    def __init__(self):
        self.process = None
        verify_loopback()
        if os.path.lexists(SOCKET):
            raise FixtureFailure("ipv6-passt-socket")
        try:
            self.process = subprocess.Popen(passt_arguments(), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, close_fds=True)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                if self.process.poll() is not None:
                    raise FixtureFailure("ipv6-passt-start")
                try:
                    info = os.lstat(SOCKET)
                    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
                        raise FixtureFailure("ipv6-passt-socket")
                    status = Path(f"/proc/{self.process.pid}/status").read_text()
                    if capabilities_zero(status) and re.search(r"^Seccomp:\s*2$", status, re.M):
                        self.socket_identity = (info.st_dev, info.st_ino)
                        return
                except FileNotFoundError:
                    pass
                time.sleep(0.05)
            raise FixtureFailure("ipv6-passt-privileges")
        except Exception:
            self.close()
            raise

    def verify(self):
        if self.process is None or self.process.poll() is not None:
            raise FixtureFailure("ipv6-passt-start")
        if not capabilities_zero(Path(f"/proc/{self.process.pid}/status").read_text()):
            raise FixtureFailure("ipv6-passt-privileges")
        info = os.lstat(SOCKET)
        if not stat.S_ISSOCK(info.st_mode) or (info.st_dev, info.st_ino) != self.socket_identity:
            raise FixtureFailure("ipv6-passt-socket")

    def close(self):
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)


def prepare_guest(console):
    console.command('/ipv6/address/add address=2001:db8:ffff::15/64 interface=ether2 advertise=no comment="mdlab-fixed-ipv6-topology"')
    reply = console.command(':put ("MDS6ADDR=" . [/ipv6/address/print count-only where address="2001:db8:ffff::15/64" && interface="ether2"])')
    if not re.search(r"(?:^|[\r\n])MDS6ADDR=1[\r\n]", reply):
        raise FixtureFailure("ipv6-guest-interface")


def source_rows(reply):
    # RouterOS terminals may delimit output with CR as well as CRLF. Consume
    # only the leading delimiter, so consecutive records cannot hide each other.
    return re.findall(r"(?:^|[\r\n])[ \t]*MDS6SOURCE=([^\r\n]+)(?=[\r\n]|$)", reply)


def session_sources(reply):
    rows = source_rows(reply)
    if len(rows) != 2:
        return False
    result = set()
    for row in rows:
        fields = row.split("|")
        if len(fields) != 2 or not re.fullmatch(r"[0-9]{1,10}", fields[1]):
            return False
        try:
            address = str(ipaddress.IPv6Address(fields[0]))
        except ValueError:
            return False
        result.add((address, int(fields[1])))
    return result == EXPECTED_SOURCES


class NativeSourceVerifier:
    def __init__(self, console, passt):
        self.console, self.passt = console, passt
        self.lock = threading.Lock()

    def __call__(self):
        try:
            return self._verify()
        except FixtureFailure as error:
            code = error.args[0] if len(error.args) == 1 else "unclassified"
            if code not in FAILURES | {"console-state-timeout", "console-read-limit", "console-write-limit", "console-write-timeout", "console-write-failed", "emulator-stopped"}:
                code = "unclassified"
            print("fixture_ipv6_source_failure=" + code, flush=True)
            raise

    def _verify(self):
        with self.lock:
            self.passt.verify()
            # Read actual runtime sessions. Neither intended connection records
            # nor host socket addresses can substitute for these two reads.
            command = ':foreach s in=[/routing/bgp/session/print as-value] do={ :put ("MDS6SOURCE=" . ($s->"remote.address") . "|" . ($s->"remote.as")) }'
            for _ in range(2):
                reply = self.console.command(command, 10)
                if not session_sources(reply):
                    # Only fixed predicates about this fixed read-only query.
                    # Never emit the console response, source addresses or IDs.
                    rows = source_rows(reply)
                    syntax = re.search(r"bad command name|syntax error|expected end|expected value|no such item|input does not match", reply, re.I) is not None
                    pairs = re.findall(r"MDS6SOURCE=([0-9a-fA-F:]+)\|([0-9]+)", reply)
                    expected = {(address, str(asn)) for address, asn in EXPECTED_SOURCES}
                    print("fixture_ipv6_session_query=as-value source_rows=" + str(min(len(rows), 64)) + " source_rows_overflow=" + str(len(rows) > 64).lower() + " query_error=" + str(syntax).lower() + " source_markers=" + str(min(reply.count("MDS6SOURCE="), 64)) + " complete_pairs=" + str(min(len(pairs), 64)) + " expected_pairs=" + str(min(sum(pair in expected for pair in pairs), 64)), flush=True)
                    raise FixtureFailure("ipv6-native-session-sources")
            self.passt.verify()
            return True


def safe_failure_code(error):
    if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURES:
        return error.args[0]
    return "unclassified"
