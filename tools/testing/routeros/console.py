"""Trusted, bounded bootstrap for the disposable raw CHR image only."""
import os
import re
import select
import time

IMAGE_SHA256 = "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878"
VERSION = "7.24.6"


class FixtureFailure(Exception):
    """Only a fixed local category, never a guest transcript or credential."""


def qemu_arguments(ssh=False, bgp_cache=False, bgp_generated_ipv4=False, bgp_generated_ipv6=False, device_reset=False):
    if device_reset and (not ssh or bgp_cache or bgp_generated_ipv4 or bgp_generated_ipv6):
        raise FixtureFailure("device-reset-requires-exclusive-ssh")
    if bgp_cache and not ssh:
        raise FixtureFailure("bgp-cache-requires-ssh")
    if bgp_generated_ipv4 and (not ssh or bgp_cache):
        raise FixtureFailure("generated-bgp-requires-exclusive-ssh-mode")
    if bgp_generated_ipv6 and (not ssh or bgp_cache or bgp_generated_ipv4):
        raise FixtureFailure("generated-bgp-requires-exclusive-ssh-mode")
    network = "user,id=n0,restrict=on,ipv6=off,hostfwd=tcp:127.0.0.1:18729-:8729"
    if ssh:
        network += ",hostfwd=tcp:127.0.0.1:12222-:22"
    if bgp_cache:
        network += ",guestfwd=tcp:10.0.2.100:179-tcp:127.0.0.1:11790"
    if bgp_generated_ipv4:
        # QEMU's legacy guestfwd auto-label uses only the guest port, so two
        # port-179 endpoints collide. Explicit distinct chardevs keep normal
        # BGP ports on both peers without NAT or guest routing changes.
        network += ",guestfwd=tcp:10.0.2.100:179-chardev:md_bgp_sender,guestfwd=tcp:10.0.2.101:179-chardev:md_bgp_recipient"
    channels = []
    if bgp_generated_ipv4:
        channels = ["-chardev", "socket,id=md_bgp_sender,host=127.0.0.1,port=11790", "-chardev", "socket,id=md_bgp_recipient,host=127.0.0.1,port=11791"]
    extra_network = []
    if bgp_generated_ipv6:
        extra_network = ["-netdev", "stream,id=md_bgp6,addr.type=unix,addr.path=/scratch/passt-bgp6.sock", "-device", "e1000,netdev=md_bgp6,romfile=,mac=52:54:00:12:34:66"]
    reset_monitor = ["-qmp", "unix:/scratch/device-reset-qmp.sock,server=on,wait=off"] if device_reset else ["-no-reboot"]
    return [
        "/native/usr/bin/qemu-system-x86_64", "-nodefaults", "-machine", "pc,accel=tcg",
        "-cpu", "qemu64", "-smp", "1", "-m", "768", "-display", "none", "-monitor", "none",
        "-serial", "stdio", "-bios", "/native/usr/share/seabios/bios-256k.bin", "-L", "/native/usr/share/qemu",
        "-drive", "file=/base/chr.img,format=raw,if=virtio,snapshot=on",
        *channels, "-netdev", network,
        "-device", "e1000,netdev=n0,romfile=",
        *extra_network,
        "-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
        "-rtc", "base=utc,clock=host", *reset_monitor,
    ]


class Console:
    def __init__(self, process):
        self.process = process
        self.pending = b""
        self.total = 0
        self.responses = 0
        self.responded = {}
        os.set_blocking(process.stdout.fileno(), False)
        os.set_blocking(process.stdin.fileno(), False)

    def send(self, value):
        if len(value) > 4096:
            raise FixtureFailure("console-write-limit")
        deadline = time.monotonic() + 5
        while value:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise FixtureFailure("console-write-timeout")
            _, writable, _ = select.select([], [self.process.stdin], [], remaining)
            if writable:
                try:
                    count = os.write(self.process.stdin.fileno(), value)
                except BlockingIOError:
                    continue
                if not count:
                    raise FixtureFailure("console-write-failed")
                value = value[count:]

    def read(self, timeout=1):
        ready, _, _ = select.select([self.process.stdout], [], [], timeout)
        if not ready:
            return b""
        chunk = os.read(self.process.stdout.fileno(), 8192)
        if not chunk:
            raise FixtureFailure("emulator-stopped")
        tail = self.pending[-8:]
        controls = tail + chunk
        offset = self.total - len(tail)
        for request, response in ((b"\x1bZ", b"\x1b[?1;2c"), (b"\x1b[c", b"\x1b[?1;2c"), (b"\x1b[0c", b"\x1b[?1;2c"), (b"\x1b[6n", b"\x1b[24;80R")):
            for match in re.finditer(re.escape(request), controls):
                end = offset + match.end()
                if end > self.responded.get(request, -1):
                    self.responses += 1
                    if self.responses > 64:
                        raise FixtureFailure("console-negotiation-limit")
                    self.send(response)
                    self.responded[request] = end
        self.total += len(chunk)
        if self.total > 1 << 20:
            raise FixtureFailure("console-read-limit")
        self.pending = (self.pending + chunk)[-65536:]
        return chunk

    def plain(self):
        return re.sub(rb"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[78cZ])", b"", self.pending)

    def until(self, pattern, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if re.search(pattern, self.plain(), re.I):
                result = self.plain()
                self.pending = b""
                return result
            self.read(min(1, max(0, deadline - time.monotonic())))
        raise FixtureFailure("console-state-timeout")

    def command(self, command, timeout=15):
        # Internal bootstrap code calls this with fixed scripts and generated
        # hexadecimal fixture data. No CLI argument becomes a guest command.
        self.pending = b""
        marker = "MDLAB_" + os.urandom(8).hex()
        self.send((command + '; :put "' + marker + '"\r').encode())
        result = self.until(rb"[\r\n]+" + marker.encode() + rb"[\r\n]+", timeout)
        return result.decode("utf-8", "replace")
