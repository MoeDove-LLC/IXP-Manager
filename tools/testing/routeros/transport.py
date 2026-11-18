"""Trusted CHR serial-owner + API-SSL cancellation fixture; guest is disposable."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import select
import socket
import subprocess
import sys
import threading
import time

# -I -S intentionally excludes cwd and site packages. Only this read-only
# trusted source directory is added for the two helper modules.
sys.path.insert(0, "/lab")
from console import Console, FixtureFailure, IMAGE_SHA256, VERSION, qemu_arguments
from result import TEST, PROPERTY_TEST, SSH_CANCEL_TEST, SSH_CONFIRM_TEST, SSH_DISCONNECT_TEST, LIVE_TESTS, GENERATED_BGP_TEST, GENERATED_BGP6_TEST, inspect_output
from ssh_fixture import prepare_ssh, safe_failure_code
from live_schema import SUPERVISOR_TESTS
import bgp_cache as bgp_fixture
import experiments
import guest_resources
import device_reset
from bootstrap import cpu_limit

CORE_FAILURE_CODES = frozenset((
    "capabilities", "network-evidence-limit", "network-namespace", "mounted-image", "test-selection", "test-timeout",
    "test-output-limit", "test-evidence", "console-proxy", "console-proxy-handoff", "fresh-image-password-state",
    "guest-version", "guest-certificate", "console-write-limit", "console-write-timeout", "console-write-failed",
    "emulator-stopped", "console-negotiation-limit", "console-read-limit", "console-state-timeout",
))


def emit(message):
    print(message, flush=True)


def verify_sandbox():
    status = Path("/proc/self/status").read_text()
    for key in ("CapEff", "CapBnd"):
        found = re.search(r"^" + key + r":\s*([0-9a-f]+)$", status, re.M)
        if not found or int(found[1], 16) != 0:
            raise FixtureFailure("capabilities")
    response = subprocess.run(["/usr/sbin/ip", "-j", "link", "show"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5, check=True)
    if len(response.stdout) > 8192:
        raise FixtureFailure("network-evidence-limit")
    links = json.loads(response.stdout)
    if [link["ifname"] for link in links] != ["lo"] or "UP" not in links[0]["flags"]:
        raise FixtureFailure("network-namespace")
    digest = hashlib.sha256()
    with open("/base/chr.img", "rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    if digest.hexdigest() != IMAGE_SHA256:
        raise FixtureFailure("mounted-image")
    emit("fixture_isolation_verified=true mounted_image_verified=true")


def private_file(path, value):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "w") as output:
        output.write(value)


def stop_process(process):
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def report_guest_resources(process, expected_cpu_limit=360):
    try:
        try:
            row = guest_resources.observe(process, expected_cpu_limit=expected_cpu_limit)
        except Exception:
            row = guest_resources.empty_observation(process is not None, expected_cpu_limit=expected_cpu_limit)
        emit(guest_resources.format_observation(row))
    except Exception:
        # Even a failure in the diagnostic fallback must allow guest cleanup.
        pass


def test_arguments(test):
    if test not in (TEST, PROPERTY_TEST, SSH_CANCEL_TEST, SSH_CONFIRM_TEST, SSH_DISCONNECT_TEST, GENERATED_BGP_TEST, GENERATED_BGP6_TEST, *LIVE_TESTS, *experiments.TESTS):
        raise FixtureFailure("test-selection")
    # The property experiment can spend its own 30-second cleanup budget after
    # its 120-second test context. It is a separate process, never a regex suite.
    timeout = "12m" if test == SSH_DISCONNECT_TEST else "300s" if test in SUPERVISOR_TESTS or test in (GENERATED_BGP_TEST, GENERATED_BGP6_TEST) else "240s" if test in LIVE_TESTS else "180s" if test == PROPERTY_TEST else "150s"
    binary = "/lab/routeros-live.test" if test in LIVE_TESTS or test in (GENERATED_BGP_TEST, GENERATED_BGP6_TEST) else "/lab/routeros.test"
    if test in experiments.TESTS:
        timeout = "600s" if test in (experiments.CASES[name] for name in experiments.LONG_CASES) else "240s"
        if test == experiments.large_cleanup.TEST:
            timeout = f"{experiments.large_cleanup.GO_SECONDS}s"
        if test in (experiments.CASES["read-scale"], experiments.CASES["read-scale-from"], experiments.CASES["presence-id-scale"], experiments.CASES["selection-scale"], experiments.CASES["transition-budget"], experiments.CASES["discard-bounded"], *device_reset.CASES.values()):
            timeout = "300s" # fixed 240-second work context plus bounded reporting/close
        binary = "/lab/routeros-live.test" if test in experiments.LIVE_TESTS else "/lab/routeros.test"
        if test in experiments.TERMINAL_TESTS:
            binary = "/lab/routeros-terminal.test"
    return [binary, "-test.run", "^" + test + "$", "-test.v", "-test.timeout", timeout]


def test_wall_seconds(test):
    # Every allowed Go timeout has exactly ten seconds of parent-side grace.
    value = test_arguments(test)[-1]
    return int(value[:-1]) * (60 if value.endswith("m") else 1) + 10


def run_test(environment, test=TEST, reset_controller=None):
    process = subprocess.Popen(test_arguments(test), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=environment, close_fds=True)
    captured = bytearray()
    deadline = time.monotonic() + test_wall_seconds(test)
    os.set_blocking(process.stdout.fileno(), False)
    try:
        if reset_controller is not None:
            if test not in device_reset.CASES.values():
                raise FixtureFailure("test-selection")
            reset_controller.start(process)
        while True:
            if time.monotonic() >= deadline:
                raise FixtureFailure("test-timeout")
            ready, _, _ = select.select([process.stdout], [], [], 0.2)
            if ready:
                chunk = os.read(process.stdout.fileno(), 8192)
                if not chunk:
                    break
                if len(captured) + len(chunk) > 65536:
                    raise FixtureFailure("test-output-limit")
                captured.extend(chunk)
        result = process.wait(timeout=5)
        if reset_controller is not None:
            reset_controller.verify()
        verified, safe_lines = inspect_output(captured, result, test=test)
        for line in safe_lines:
            emit(line)
        if not verified:
            raise FixtureFailure("test-evidence")
    finally:
        captured[:] = b"\x00" * len(captured)
        stop_process(process)
        process.stdout.close()


def relogin(console, password):
    # The cancellation test consumed the Login prompt and proved rollback by
    # API readback. This fresh login only provisions the read/API fixture user.
    console.pending = b""
    console.send(b"admin+ct200w60h\r")
    console.until(rb"password:", 10)
    console.send((password + "\r").encode())
    console.until(rb"\] >", 20)


def prepare_properties(console, password):
    relogin(console, password)
    limited_password = secrets.token_hex(24)
    try:
        console.command("/user/group/add name=mdlab_read policy=read,api")
        console.command('/user/add name=mdlab-read group=mdlab_read password="' + limited_password + '"')
        private_file("/scratch/limited-password", limited_password)
    finally:
        limited_password = ""
    emit("fixture_limited_api_account_configured=true")


def run_followups(console, password, environment, properties=False, ssh=False, live=False, bgp_cache=False, bgp_peer=None):
    # Called only after the independent serial rollback proof and proxy join.
    if live and not ssh:
        raise FixtureFailure("live-requires-ssh")
    if bgp_cache and (not ssh or bgp_peer is None):
        raise FixtureFailure("bgp-cache-requires-ssh-and-peer")
    if properties:
        prepare_properties(console, password)
        run_test(environment, PROPERTY_TEST)
        emit("native_properties_fixture_passed=true")
    elif ssh:
        relogin(console, password)
    if ssh:
        prepare_ssh(console, private_file)
        emit("fixture_ssh_host_key_serial_verified=true")
        run_test(environment, SSH_CANCEL_TEST)
        emit("native_ssh_cancellation_fixture_passed=true")
        run_test(environment, SSH_CONFIRM_TEST)
        emit("native_ssh_confirmation_fixture_passed=true")
    if live:
        for test in LIVE_TESTS:
            run_test(environment, test)
        emit("native_live_fixture_passed=true")
    if bgp_cache:
        emit(bgp_fixture.proof_line(bgp_fixture.run_probe(console, bgp_peer)))


def run_disconnect(console, environment):
    # This standalone branch starts from the authenticated serial bootstrap;
    # it runs no prior serial/SSH mutation tests and needs no proxy/relogin.
    prepare_ssh(console, private_file)
    emit("fixture_ssh_host_key_serial_verified=true")
    selected_environment = dict(environment)
    selected_environment["MOEDOVE_CHR_FAILURE_TEST"] = "1"
    run_test(selected_environment, SSH_DISCONNECT_TEST)
    emit("native_ssh_disconnect_fixture_passed=true")


def run_generated(console, environment):
    prepare_ssh(console, private_file)
    emit("fixture_ssh_host_key_serial_verified=true")
    generated_environment = dict(environment)
    generated_environment["MOEDOVE_CHR_GENERATED_BGP_TEST"] = "1"
    run_test(generated_environment, GENERATED_BGP_TEST)
    emit("native_generated_bgp_ipv4_fixture_passed=true")


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--properties", action="store_true")
    parser.add_argument("--ssh", action="store_true")
    parser.add_argument("--live", action="store_true")
    parser.add_argument("--bgp-cache", action="store_true")
    parser.add_argument("--ssh-disconnect", action="store_true")
    parser.add_argument("--bgp-generated-ipv4", action="store_true")
    parser.add_argument("--bgp-generated-ipv6", action="store_true")
    parser.add_argument("--case", dest="native_case", choices=tuple(experiments.CASES))
    options = parser.parse_args(argv)
    if options.bgp_generated_ipv6 and (not options.ssh or options.properties or options.live or options.bgp_cache or options.ssh_disconnect or options.bgp_generated_ipv4 or options.native_case is not None):
        parser.error("generated_bgp_is_standalone_with_ssh=true")
    if options.live and not options.ssh:
        parser.error("live_requires_ssh=true")
    if options.bgp_cache and not options.ssh:
        parser.error("bgp_cache_requires_ssh=true")
    if options.bgp_generated_ipv4 and (not options.ssh or options.properties or options.live or options.bgp_cache or options.ssh_disconnect):
        parser.error("generated_bgp_is_standalone_with_ssh=true")
    if options.ssh_disconnect and (options.properties or options.ssh or options.live or options.bgp_cache or options.bgp_generated_ipv4):
        parser.error("ssh_disconnect_is_standalone=true")
    if options.native_case is not None and (not options.ssh or options.properties or options.live or options.bgp_cache or options.ssh_disconnect or options.bgp_generated_ipv4):
        parser.error("native_case_is_standalone_with_ssh=true")
    verify_sandbox()
    process = listener = thread = bgp_peer = generated_peers = passt = reset_controller = None
    stopped = threading.Event()
    proxy_failed = threading.Event()
    password = secrets.token_hex(24)
    try:
        if options.bgp_cache:
            bgp_peer = bgp_fixture.FixedPeer()
        if options.bgp_generated_ipv4:
            import bgp_generated
            generated_peers = bgp_generated.FixedPeers()
        if options.bgp_generated_ipv6:
            import ipv6_transport
            passt = ipv6_transport.Passt()
            emit("fixture_ipv6_loopback_verified=true fixture_passt_capabilities_zero=true")
        process = subprocess.Popen(qemu_arguments(ssh=options.ssh or options.ssh_disconnect, bgp_cache=options.bgp_cache, bgp_generated_ipv4=options.bgp_generated_ipv4, bgp_generated_ipv6=options.bgp_generated_ipv6, device_reset=options.native_case in device_reset.CASES), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=0, close_fds=True)
        console = Console(process)
        console.until(rb"login:", 90)
        console.send(b"admin+ct200w60h\r")
        console.until(rb"password:", 10)
        console.send(b"\r")
        stage = console.until(rb"(?:license|new password|\] >)", 20)
        if b"license" in stage.lower():
            console.send(b"n\r")
            stage = console.until(rb"(?:new password|\] >)", 20)
        if b"new password" not in stage.lower():
            raise FixtureFailure("fresh-image-password-state")
        console.send((password + "\r").encode())
        console.until(rb"repeat.*password[>:]", 10)
        console.send((password + "\r").encode())
        console.until(rb"\] >", 15)
        version = console.command(":put [/system/resource/get version]")
        if not re.search(r"(?:^|[\r\n])7\.24\.6(?: \((?:stable|long-term)\))?[\r\n]", version):
            raise FixtureFailure("guest-version")
        emit("fixture_version=7.24.6")
        console.command("/certificate/add name=mdlab-api common-name=chr-fixture.invalid subject-alt-name=DNS:chr-fixture.invalid key-size=prime256v1 days-valid=1")
        console.command("/certificate/sign mdlab-api", 90)
        console.command("/certificate/export-certificate mdlab-api type=pem file-name=mdlab-api")
        certificate = console.command(':put [/file/get [/file/find where name="mdlab-api.crt"] contents]')
        match = re.search(r"-----BEGIN CERTIFICATE-----[\sA-Za-z0-9+/=]+-----END CERTIFICATE-----", certificate)
        if not match:
            raise FixtureFailure("guest-certificate")
        private_file("/scratch/api.crt", match[0].replace("\r", "") + "\n")
        private_file("/scratch/api-password", password)
        console.command("/ip/service/set api-ssl certificate=mdlab-api disabled=no")
        emit("fixture_api_ssl_configured=true")
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind("/scratch/console.sock")
        os.chmod("/scratch/console.sock", 0o600)
        listener.listen(1)
        private_file("/scratch/native-context.json", json.dumps({
            "version": VERSION, "imageSha256": IMAGE_SHA256,
            "apiAddress": "127.0.0.1:18729", "serverName": "chr-fixture.invalid",
            "caFile": "/scratch/api.crt", "passwordFile": "/scratch/api-password", "consoleSocket": "/scratch/console.sock",
        }))
        keys = ("PATH", "HOME", "TMPDIR", "LD_LIBRARY_PATH", "QEMU_MODULE_DIR", "LANG", "TERM", "GOMAXPROCS", "GOMEMLIMIT")
        environment = {key: os.environ[key] for key in keys}
        environment["MOEDOVE_CHR_NATIVE_TEST"] = "1"
        if options.ssh_disconnect:
            run_disconnect(console, environment)
            return
        if options.bgp_generated_ipv4:
            run_generated(console, environment)
            return
        if options.bgp_generated_ipv6:
            import bgp_generated6
            ipv6_transport.prepare_guest(console)
            emit("fixture_ipv6_interface_configured=true")
            generated_peers = bgp_generated6.FixedPeers6(ipv6_transport.NativeSourceVerifier(console, passt))
            prepare_ssh(console, private_file)
            emit("fixture_ssh_host_key_serial_verified=true")
            selected_environment = dict(environment)
            selected_environment["MOEDOVE_CHR_GENERATED_BGP6_TEST"] = "1"
            run_test(selected_environment, GENERATED_BGP6_TEST)
            passt.verify()
            emit("native_generated_bgp_ipv6_fixture_passed=true")
            return
        if options.native_case is not None:
            prepare_ssh(console, private_file)
            emit("fixture_ssh_host_key_serial_verified=true")
            if options.native_case in device_reset.CASES:
                reset_controller = device_reset.DeviceReset(process)
                enable = "MOEDOVE_CHR_DEVICE_RESTORE_TEST" if options.native_case == "device-restore" else "MOEDOVE_CHR_DEVICE_RESET_TEST"
                reset_environment = dict(environment, **{enable: "1"})
                run_test(reset_environment, device_reset.CASES[options.native_case], reset_controller=reset_controller)
                emit("fixture_qmp_reset_verified=true fixture_same_emulator_retained=true")
            else:
                run_test(environment, experiments.CASES[options.native_case])
            emit("native_explicit_case_passed=true")
            return

        def proxy():
            client = None
            total = 0
            try:
                listener.settimeout(130)
                client, _ = listener.accept()
                client.settimeout(5)
                console.pending = b""
                console.send(b"\r")
                while not stopped.is_set():
                    ready, _, _ = select.select([client, process.stdout], [], [], 0.5)
                    for stream in ready:
                        data = client.recv(4096) if stream is client else os.read(process.stdout.fileno(), 4096)
                        if not data:
                            return
                        total += len(data)
                        if total > 1 << 20:
                            raise FixtureFailure("proxy-byte-limit")
                        if stream is client:
                            console.send(data)
                        else:
                            client.sendall(data)
            except Exception:
                if not stopped.is_set():
                    proxy_failed.set()
            finally:
                if client is not None:
                    client.close()

        thread = threading.Thread(target=proxy, daemon=True)
        thread.start()
        run_test(environment)
        if proxy_failed.is_set():
            raise FixtureFailure("console-proxy")
        emit("native_transport_fixture_passed=true")
        if options.properties or options.ssh:
            # One reader owns the persistent console at a time. Never race
            # bootstrap login against the completed terminal test's proxy.
            stopped.set()
            listener.close()
            thread.join(timeout=2)
            if thread.is_alive() or proxy_failed.is_set():
                raise FixtureFailure("console-proxy-handoff")
        run_followups(console, password, environment, properties=options.properties, ssh=options.ssh, live=options.live, bgp_cache=options.bgp_cache, bgp_peer=bgp_peer)
    finally:
        # Capture zombie CPU ticks before any cleanup/poll can reap QEMU.
        # This is its state before harness termination, not our later SIGTERM.
        report_guest_resources(process, cpu_limit(options.native_case))
        stopped.set()
        if reset_controller is not None:
            reset_controller.close()
        if listener is not None:
            listener.close()
        if bgp_peer is not None:
            bgp_peer.close()
        if generated_peers is not None:
            generated_peers.close()
        stop_process(process)
        if passt is not None:
            passt.close()
        if thread is not None:
            thread.join(timeout=2)
        if process is not None:
            process.stdin.close()
            process.stdout.close()
        password = ""
        # Files live only on the namespace's bounded tmpfs; do not export them.
        emit("guest_process_stopped=true")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Even unexpected errors must not print a traceback/console tail that
        # can contain the generated credential or target-controlled output.
        emit("native_fixture_tests_passed=false")
        code = safe_failure_code(error)
        if code == "unclassified":
            code = device_reset.safe_failure_code(error)
        if code == "unclassified":
            code = bgp_fixture.safe_failure_code(error)
        if code == "unclassified" and "bgp_generated" in sys.modules:
            code = sys.modules["bgp_generated"].safe_failure_code(error)
        if code == "unclassified" and "bgp_generated6" in sys.modules:
            code = sys.modules["bgp_generated6"].safe_failure_code(error)
        if code == "unclassified" and "ipv6_transport" in sys.modules:
            code = sys.modules["ipv6_transport"].safe_failure_code(error)
        if code == "unclassified" and isinstance(error, FixtureFailure) and len(error.args) == 1 and error.args[0] in CORE_FAILURE_CODES:
            code = error.args[0]
        emit("fixture_failure_code=" + code)
        if code == "unclassified":
            name = type(error).__name__
            if name not in ("TypeError", "ValueError", "AttributeError", "NameError", "ImportError", "ModuleNotFoundError", "RuntimeError", "OSError", "FileNotFoundError", "PermissionError"):
                name = "unclassified"
            emit("fixture_failure_type=" + name)
        raise SystemExit(1) from None
