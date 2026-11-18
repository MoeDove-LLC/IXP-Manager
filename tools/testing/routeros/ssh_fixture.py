"""Fixed disposable SSH bootstrap. No host target or private host key input."""
import base64
import binascii
import os
from pathlib import Path
import re
import select
import stat
import subprocess
import time

from console import FixtureFailure

SSH_ENV = {"PATH": "/usr/bin:/bin", "HOME": "/scratch/home", "TMPDIR": "/scratch/tmp", "LANG": "C", "LC_ALL": "C"}
KEY_TYPES = frozenset(("ssh-ed25519", "ssh-rsa"))
PUBLIC_NAMES = frozenset(("mdlab_host_rsa_pub.pem",))
HOST_KEY_ALGORITHM = "ssh-rsa"  # Observed on this exact raw 7.24.6 fixture.
FAILURE_CODES = frozenset((
    "ssh-tool-selection", "ssh-tool-timeout", "ssh-tool-output-limit", "ssh-tool-failed", "ssh-tool-unavailable",
    "ssh-public-key-format", "ssh-public-export-not-unique", "ssh-public-export-format",
    "ssh-host-key-output-limit", "ssh-host-key-format", "ssh-host-key-mismatch", "ssh-private-key-file",
    "ssh-public-key-file", "ssh-user-key-format",
))


def safe_failure_code(error):
    if isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in FAILURE_CODES:
        return error.args[0]
    return "unclassified"


def tool_output(arguments, limit, timeout):
    """Bound output while reading; communicate() would buffer before limiting."""
    if arguments[0] not in ("/usr/bin/ssh-keygen", "/usr/bin/ssh-keyscan"):
        raise FixtureFailure("ssh-tool-selection")
    try:
        process = subprocess.Popen(arguments, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=SSH_ENV, close_fds=True)
    except OSError:
        raise FixtureFailure("ssh-tool-unavailable") from None
    captured = bytearray()
    deadline = time.monotonic() + timeout
    os.set_blocking(process.stdout.fileno(), False)
    try:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise FixtureFailure("ssh-tool-timeout")
            ready, _, _ = select.select([process.stdout], [], [], min(0.2, remaining))
            if not ready:
                continue
            chunk = os.read(process.stdout.fileno(), 4096)
            if not chunk:
                break
            if len(captured) + len(chunk) > limit:
                raise FixtureFailure("ssh-tool-output-limit")
            captured.extend(chunk)
        if process.wait(timeout=max(0.001, deadline - time.monotonic())) != 0:
            raise FixtureFailure("ssh-tool-failed")
        return bytes(captured)
    finally:
        captured[:] = b"\x00" * len(captured)
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        process.stdout.close()


def key_wire(algorithm, encoded):
    if algorithm not in KEY_TYPES or len(encoded) > 8192:
        raise FixtureFailure("ssh-public-key-format")
    try:
        wire = base64.b64decode(encoded, validate=True)
    except (binascii.Error, ValueError):
        raise FixtureFailure("ssh-public-key-format") from None
    if len(wire) < 4 or len(wire) > 6144:
        raise FixtureFailure("ssh-public-key-format")
    name_length = int.from_bytes(wire[:4], "big")
    if wire[4:4 + name_length] != algorithm.encode("ascii") or name_length != len(algorithm) or len(wire) <= 4 + name_length:
        raise FixtureFailure("ssh-public-key-format")
    return wire


def public_export_name(output):
    # RouterOS exports both halves in the guest. Only a reviewed PUBLIC name
    # may become the argument of the subsequent fixed /file/get expression.
    names = [line.strip() for line in output.splitlines() if re.fullmatch(r"mdlab_host[A-Za-z0-9_.-]*_pub\.pem", line.strip())]
    if len(names) != 1 or names[0] not in PUBLIC_NAMES:
        raise FixtureFailure("ssh-public-export-not-unique")
    return names[0]


def public_pem(output):
    if len(output) > 32768 or "PRIVATE KEY" in output:
        raise FixtureFailure("ssh-public-export-format")
    matches = re.findall(r"-----BEGIN PUBLIC KEY-----[\sA-Za-z0-9+/=]+-----END PUBLIC KEY-----", output)
    if len(matches) != 1:
        raise FixtureFailure("ssh-public-export-format")
    return matches[0].replace("\r", "") + "\n"


def pinned_host(converted, scanned):
    if len(converted) > 8192 or len(scanned) > 16384:
        raise FixtureFailure("ssh-host-key-output-limit")
    try:
        public_lines = converted.decode("ascii").splitlines()
        scan_lines = scanned.decode("ascii").splitlines()
    except UnicodeDecodeError:
        raise FixtureFailure("ssh-host-key-format") from None
    if len(public_lines) != 1:
        raise FixtureFailure("ssh-host-key-format")
    public_parts = public_lines[0].split()
    if len(public_parts) < 2:
        raise FixtureFailure("ssh-host-key-format")
    # ssh-keygen may append a local conversion comment. It is never written
    # to known_hosts, interpreted as a key field or exposed in diagnostics.
    algorithm, encoded = public_parts[:2]
    if algorithm != HOST_KEY_ALGORITHM:
        raise FixtureFailure("ssh-host-key-format")
    trusted = key_wire(algorithm, encoded)
    matches = []
    for line in scan_lines:
        if not line.strip() or line.startswith("#"):
            continue
        parts = line.split()
        if len(parts) != 3 or parts[0] != "[127.0.0.1]:12222" or parts[1] != HOST_KEY_ALGORITHM:
            raise FixtureFailure("ssh-host-key-format")
        wire = key_wire(parts[1], parts[2])
        if parts[1] == algorithm and wire == trusted:
            matches.append("[127.0.0.1]:12222 " + algorithm + " " + encoded + "\n")
    if len(matches) != 1:
        raise FixtureFailure("ssh-host-key-mismatch")
    return matches[0]


def generated_public_key():
    key = Path("/scratch/ssh_identity")
    info = key.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o777 != 0o600 or info.st_size < 1 or info.st_size > 32768:
        raise FixtureFailure("ssh-private-key-file")
    public_path = Path("/scratch/ssh_identity.pub")
    info = public_path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022 or info.st_size < 1 or info.st_size > 16384:
        raise FixtureFailure("ssh-public-key-file")
    with public_path.open("rb") as source:
        data = source.read(16385)
    if len(data) > 16384:
        raise FixtureFailure("ssh-public-key-file")
    try:
        value = data.decode("ascii").strip()
    except UnicodeDecodeError:
        raise FixtureFailure("ssh-user-key-format") from None
    match = re.fullmatch(r"ssh-ed25519 ([A-Za-z0-9+/=]+) native-fixture", value)
    if not match:
        raise FixtureFailure("ssh-user-key-format")
    key_wire("ssh-ed25519", match[1])
    return value


def prepare_ssh(console, private_file):
    tool_output(["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "native-fixture", "-f", "/scratch/ssh_identity"], 1024, 10)
    public = generated_public_key()
    console.command('/user/ssh-keys/add user=admin key="' + public + '"')
    console.command("/ip/ssh/export-host-key key-file-prefix=mdlab_host")
    listing = console.command(':foreach f in=[/file/find where name~"^mdlab_host"] do={ :put [/file/get $f name] }')
    name = public_export_name(listing)
    pem = public_pem(console.command(':put [/file/get [/file/find where name="' + name + '"] contents]'))
    private_file("/scratch/host-public.pem", pem)
    converted = tool_output(["/usr/bin/ssh-keygen", "-i", "-m", "PKCS8", "-f", "/scratch/host-public.pem"], 8192, 5)
    scanned = tool_output(["/usr/bin/ssh-keyscan", "-T", "8", "-p", "12222", "-t", "rsa", "127.0.0.1"], 16384, 12)
    private_file("/scratch/known_hosts", pinned_host(converted, scanned))
