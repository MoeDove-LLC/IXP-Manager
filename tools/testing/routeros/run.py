#!/usr/bin/python3
"""Launch only the pinned, disposable CHR transport fixture; never a host VM."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import subprocess
import sys
import sysconfig

# Only this launcher-owned source directory is added, including under -I -S.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from experiments import CASES, LIVE_CASES, LONG_CASES, large_cleanup

IMAGE_SHA256 = "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878"
IMAGE_SIZE = 128 << 20
QEMU_SUBTREES = (
    "usr/bin", "usr/lib/x86_64-linux-gnu", "usr/share/qemu", "usr/share/seabios",
)
SOURCE_FILES = ("bootstrap.py", "console.py", "transport.py", "result.py", "ssh_fixture.py", "live_schema.py", "bgp_cache.py", "experiments.py", "guest_resources.py", "device_reset.py", "large_cleanup.py")
SSH_TOOLS = ("/usr/bin/ssh", "/usr/bin/ssh-keygen", "/usr/bin/ssh-keyscan")
ETC_FILES = ("passwd", "group", "hosts", "nsswitch.conf")
SAFE_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}


class InvalidInput(Exception):
    pass


def regular(path, executable=False):
    try:
        path = Path(path).resolve(strict=True)
        if not path.is_file() or (executable and not os.access(path, os.X_OK)):
            raise InvalidInput("required_file_available=false")
        return path
    except (OSError, ValueError):
        raise InvalidInput("required_file_available=false") from None


def verify_image(path):
    path = regular(path)
    before = path.stat()
    if before.st_size != IMAGE_SIZE:
        raise InvalidInput("image_sha256_verified=false")
    checksum = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            checksum.update(chunk)
    after = path.stat()
    if identity(before) != identity(after) or checksum.hexdigest() != IMAGE_SHA256:
        raise InvalidInput("image_sha256_verified=false")
    return path


def identity(info):
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns


def qemu_subtrees(root):
    try:
        root = Path(root).resolve(strict=True)
        selected = []
        for relative in QEMU_SUBTREES:
            source = root / relative
            # In particular, do not let a tool-tree directory alias expose
            # /home, a host socket directory, or a different filesystem tree.
            for ancestor in (root / "usr", *source.parents):
                if ancestor == root or not ancestor.is_relative_to(root):
                    continue
                if ancestor.is_symlink():
                    raise InvalidInput("qemu_subtrees_valid=false")
            if source.is_symlink() or not source.is_dir():
                raise InvalidInput("qemu_subtrees_valid=false")
            selected.append((source, Path("/native") / relative))
        regular(root / "usr/bin/qemu-system-x86_64", executable=True)
        regular(root / "usr/share/seabios/bios-256k.bin")
        return selected
    except (OSError, ValueError):
        raise InvalidInput("qemu_subtrees_valid=false") from None


def build_command(image, qemu_root, test_binary, source_dir, properties=False, ssh=False, live=False, live_binary=None, bgp_cache=False, ssh_disconnect=False, bgp_generated_ipv4=False, native_case=None, terminal_binary=None, bgp_generated_ipv6=False, passt_binary=None):
    """Pure argv construction after caller validation; no shell or host root bind."""
    if native_case is not None and (native_case not in CASES or not ssh or properties or live or bgp_cache or ssh_disconnect or bgp_generated_ipv4 or bgp_generated_ipv6):
        raise InvalidInput("native_case_is_standalone_with_ssh=true")
    needs_live = live or bgp_generated_ipv4 or bgp_generated_ipv6 or native_case in LIVE_CASES
    if bgp_generated_ipv6 != (passt_binary is not None):
        raise InvalidInput("generated_ipv6_requires_passt_binary=true")
    if bgp_generated_ipv6 and (properties or live or bgp_cache or ssh_disconnect or bgp_generated_ipv4):
        raise InvalidInput("generated_bgp_is_standalone=true")
    if (native_case == "owned-delay") != (terminal_binary is not None):
        raise InvalidInput("owned_delay_requires_terminal_binary=true")
    if needs_live != (live_binary is not None) or needs_live and not ssh:
        raise InvalidInput("live_requires_ssh_and_binary=true")
    if bgp_generated_ipv4 and (properties or live or bgp_cache or ssh_disconnect):
        raise InvalidInput("generated_bgp_is_standalone=true")
    if bgp_cache and not ssh:
        raise InvalidInput("bgp_cache_requires_ssh=true")
    if ssh_disconnect and (properties or ssh or live or live_binary is not None or bgp_cache or bgp_generated_ipv4 or bgp_generated_ipv6):
        raise InvalidInput("ssh_disconnect_is_standalone=true")
    python = regular("/usr/bin/python3", executable=True)
    if Path(sys.executable).resolve() != python:
        raise InvalidInput("system_python_required=true")
    stdlib = Path(sysconfig.get_path("stdlib")).resolve(strict=True)
    if stdlib.parent != Path("/usr/lib") or stdlib.name != f"python{sys.version_info.major}.{sys.version_info.minor}":
        raise InvalidInput("python_runtime_layout_supported=false")
    for tool in ("/usr/bin/bwrap", "/usr/bin/timeout", "/usr/bin/prlimit", "/usr/bin/setpriv", "/usr/sbin/ip"):
        regular(tool, executable=True)
    loader = regular("/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2")
    wall = f"{large_cleanup.WALL_SECONDS}s" if native_case == large_cleanup.CASE else "900s" if ssh_disconnect or native_case in LONG_CASES else "480s"
    command = [
        "/usr/bin/timeout", "--kill-after=5s", wall, "/usr/bin/bwrap",
        "--unshare-all", "--uid", "0", "--gid", "0",
        "--cap-add", "CAP_NET_ADMIN", "--cap-add", "CAP_SETPCAP",
        "--new-session", "--die-with-parent", "--clearenv",
        "--dir", "/usr", "--dir", "/usr/bin", "--dir", "/usr/sbin", "--dir", "/usr/lib",
        "--ro-bind", str(python), "/usr/bin/python3",
        "--ro-bind", str(stdlib), str(stdlib),
        "--ro-bind", "/usr/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu",
        "--symlink", "usr/lib", "/lib", "--dir", "/lib64",
        "--ro-bind", str(loader), "/lib64/ld-linux-x86-64.so.2",
        "--symlink", "usr/bin", "/bin", "--symlink", "usr/sbin", "/sbin",
    ]
    for tool in ("/usr/bin/prlimit", "/usr/bin/setpriv", "/usr/sbin/ip"):
        command += ["--ro-bind", str(regular(tool, executable=True)), tool]
    if ssh or ssh_disconnect:
        for tool in SSH_TOOLS:
            command += ["--ro-bind", str(regular(tool, executable=True)), tool]
    command += ["--dir", "/etc"]
    for filename in ETC_FILES:
        command += ["--ro-bind", str(regular(source_dir / "etc" / filename)), "/etc/" + filename]
    for source, destination in qemu_subtrees(qemu_root):
        command += ["--ro-bind", str(source), str(destination)]
    command += ["--dir", "/lab"]
    for filename in SOURCE_FILES:
        command += ["--ro-bind", str(regular(source_dir / filename)), "/lab/" + filename]
    if bgp_generated_ipv4 or bgp_generated_ipv6:
        command += ["--ro-bind", str(regular(source_dir / "bgp_generated.py")), "/lab/bgp_generated.py"]
    if bgp_generated_ipv6:
        for name in ("bgp_generated6.py", "ipv6_transport.py"):
            command += ["--ro-bind", str(regular(source_dir / name)), "/lab/" + name]
        command += ["--ro-bind", str(regular(passt_binary, executable=True)), "/lab/passt"]
    if needs_live:
        command += ["--ro-bind", str(live_binary), "/lab/routeros-live.test"]
    if terminal_binary is not None:
        command += ["--ro-bind", str(terminal_binary), "/lab/routeros-terminal.test"]
    command += [
        "--ro-bind", str(test_binary), "/lab/routeros.test",
        "--dir", "/base", "--ro-bind", str(image), "/base/chr.img",
        "--size", "805306368", "--perms", "0700", "--tmpfs", "/scratch",
        "--dir", "/scratch/home", "--dir", "/scratch/tmp", "--dir", "/scratch/shm",
        "--symlink", "/scratch/tmp", "/tmp", "--proc", "/proc", "--dev", "/dev",
        "--chdir", "/scratch", "--remount-ro", "/dev", "--remount-ro", "/",
    ]
    environment = {
        "PATH": "/native/usr/bin:/usr/bin:/bin", "HOME": "/scratch/home", "TMPDIR": "/scratch/tmp",
        "LD_LIBRARY_PATH": "/native/usr/lib/x86_64-linux-gnu",
        "QEMU_MODULE_DIR": "/native/usr/lib/x86_64-linux-gnu/qemu",
        "LANG": "C.UTF-8", "TERM": "vt100", "PYTHONDONTWRITEBYTECODE": "1",
        "GOMAXPROCS": "2", "GOMEMLIMIT": "256MiB",
    }
    for key, value in environment.items():
        command += ["--setenv", key, value]
    command += ["/usr/bin/python3", "-I", "-S", "-B", "/lab/bootstrap.py"]
    if properties:
        command += ["--properties"]
    if ssh:
        command += ["--ssh"]
    if live:
        command += ["--live"]
    if bgp_cache:
        command += ["--bgp-cache"]
    if ssh_disconnect:
        command += ["--ssh-disconnect"]
    if bgp_generated_ipv4:
        command += ["--bgp-generated-ipv4"]
    if bgp_generated_ipv6:
        command += ["--bgp-generated-ipv6"]
    if native_case is not None:
        command += ["--case", native_case]
    return command


def main(argv=None):
    parser = argparse.ArgumentParser(description="Run the pinned CHR 7.24.6 serial/API-SSL cancellation fixture in an isolated disposable namespace.")
    parser.add_argument("--image", required=True, help="Existing raw chr-7.24.6.img with the documented SHA-256")
    parser.add_argument("--qemu-root", required=True, help="Existing system/private unpacked prefix containing the required usr subtrees")
    parser.add_argument("--test-binary", required=True, help="Already compiled internal/routeros Go test binary")
    parser.add_argument("--properties", action="store_true", help="After verified cancellation, run the separate API property/explicit-cleanup experiment")
    parser.add_argument("--ssh", action="store_true", help="After serial and optional property checks, run independent pinned-SSH cancellation and confirmation experiments")
    parser.add_argument("--live", action="store_true", help="Run only the reviewed live native test allowlist after SSH checks; requires --ssh and --live-test-binary")
    parser.add_argument("--live-test-binary", help="Already compiled internal/routeros/live Go test binary; required for live, generated BGP and live experiment cases including discard1000")
    parser.add_argument("--terminal-test-binary", help="Already compiled internal/routeros/terminal Go test binary; only for --case owned-delay")
    parser.add_argument("--bgp-cache", action="store_true", help="Run only the fixed single-peer IPv4 cached-filter probe after SSH checks; requires --ssh")
    parser.add_argument("--ssh-disconnect", action="store_true", help="Standalone fixed SSH transport-close test with a 900-second wall limit; cannot combine with other modes")
    parser.add_argument("--bgp-generated-ipv4", action="store_true", help="Standalone fixed two-peer generated-policy IPv4 experiment; requires --ssh and --live-test-binary")
    parser.add_argument("--bgp-generated-ipv6", action="store_true", help="Standalone fixed true-IPv6 peer experiment; requires --ssh, --live-test-binary and --passt-binary")
    parser.add_argument("--passt-binary", help="Private passt executable with verified runtime capability dropping; only for generated IPv6")
    parser.add_argument("--case", dest="native_case", choices=tuple(CASES), help="One explicit standalone native experiment; requires --ssh and the matching live/terminal binary")
    arguments = parser.parse_args(argv)
    needs_live = arguments.live or arguments.bgp_generated_ipv4 or arguments.bgp_generated_ipv6 or arguments.native_case in LIVE_CASES
    if arguments.bgp_generated_ipv6 != bool(arguments.passt_binary):
        parser.error("generated_ipv6_requires_passt_binary=true")
    if arguments.bgp_generated_ipv6 and (arguments.properties or arguments.live or arguments.bgp_cache or arguments.ssh_disconnect or arguments.bgp_generated_ipv4):
        parser.error("generated_bgp_is_standalone=true")
    if (arguments.native_case == "owned-delay") != bool(arguments.terminal_test_binary):
        parser.error("owned_delay_requires_terminal_test_binary=true")
    if arguments.native_case is not None and (not arguments.ssh or arguments.properties or arguments.live or arguments.bgp_cache or arguments.ssh_disconnect or arguments.bgp_generated_ipv4 or arguments.bgp_generated_ipv6):
        parser.error("native_case_is_standalone_with_ssh=true")
    if needs_live != bool(arguments.live_test_binary) or needs_live and not arguments.ssh:
        parser.error("live_requires_ssh_and_live_test_binary=true")
    if arguments.bgp_generated_ipv4 and (arguments.properties or arguments.live or arguments.bgp_cache or arguments.ssh_disconnect):
        parser.error("generated_bgp_is_standalone=true")
    if arguments.bgp_cache and not arguments.ssh:
        parser.error("bgp_cache_requires_ssh=true")
    if arguments.ssh_disconnect and (arguments.properties or arguments.ssh or arguments.live or arguments.live_test_binary or arguments.bgp_cache or arguments.bgp_generated_ipv4 or arguments.bgp_generated_ipv6):
        parser.error("ssh_disconnect_is_standalone=true")
    if sys.platform != "linux" or platform.machine() != "x86_64":
        parser.error("only_linux_x86_64_supported=true")
    try:
        image = verify_image(arguments.image)
        binary = regular(arguments.test_binary, executable=True)
        live_binary = regular(arguments.live_test_binary, executable=True) if needs_live else None
        terminal_binary = regular(arguments.terminal_test_binary, executable=True) if arguments.terminal_test_binary else None
        passt_binary = regular(arguments.passt_binary, executable=True) if arguments.passt_binary else None
        inputs = [image, binary] + ([live_binary] if live_binary is not None else []) + ([terminal_binary] if terminal_binary is not None else []) + ([passt_binary] if passt_binary is not None else [])
        original = tuple(identity(path.stat()) for path in inputs)
        command = build_command(image, arguments.qemu_root, binary, Path(__file__).resolve().parent, properties=arguments.properties, ssh=arguments.ssh, live=arguments.live, live_binary=live_binary, bgp_cache=arguments.bgp_cache, ssh_disconnect=arguments.ssh_disconnect, bgp_generated_ipv4=arguments.bgp_generated_ipv4, native_case=arguments.native_case, terminal_binary=terminal_binary, bgp_generated_ipv6=arguments.bgp_generated_ipv6, passt_binary=passt_binary)
    except (InvalidInput, OSError):
        print("fixture_inputs_valid=false", file=sys.stderr)
        return 2
    print("image_sha256_verified=true fixture_version=7.24.6", flush=True)
    try:
        result = subprocess.run(command, stdin=subprocess.DEVNULL, env=SAFE_ENV, check=False, close_fds=True)
    except OSError:
        print("fixture_sandbox_started=false", file=sys.stderr)
        return 2
    try:
        unchanged = original == tuple(identity(path.stat()) for path in inputs)
    except OSError:
        unchanged = False
    if not unchanged:
        print("fixture_inputs_unchanged=false", file=sys.stderr)
        return 2
    print("guest_state_discarded=true", flush=True)
    if result.returncode != 0:
        print("fixture_completed=false", file=sys.stderr)
        return 1
    print("fixture_completed=true", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
