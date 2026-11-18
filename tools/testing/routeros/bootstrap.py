"""Only trusted namespace setup runs before all capabilities are dropped."""
import argparse
import os
import subprocess
import sys

sys.path.insert(0, "/lab")
from experiments import CASES, LONG_CASES, large_cleanup


def cpu_limit(native_case):
    if native_case is not None and native_case not in CASES:
        raise ValueError("unreviewed-native-case")
    if native_case == large_cleanup.CASE:
        return large_cleanup.CPU_SECONDS
    # Long cases already have a 600-second Go budget and 900-second outer
    # wall bound. The former shared 360-second hard CPU limit killed QEMU
    # before those unchanged work/command budgets could finish.
    return 600 if native_case in LONG_CASES else 360


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--properties", action="store_true")
    parser.add_argument("--ssh", action="store_true")
    parser.add_argument("--live", action="store_true")
    parser.add_argument("--bgp-cache", action="store_true")
    parser.add_argument("--ssh-disconnect", action="store_true")
    parser.add_argument("--bgp-generated-ipv4", action="store_true")
    parser.add_argument("--bgp-generated-ipv6", action="store_true")
    parser.add_argument("--case", dest="native_case", choices=tuple(CASES))
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
    os.umask(0o077)
    completed = subprocess.run(
        ["/usr/sbin/ip", "link", "set", "lo", "up"],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        timeout=10, check=False,
    )
    if completed.returncode:
        raise RuntimeError("loopback_setup_failed")
    if options.bgp_generated_ipv6:
        # Fixed documentation addresses in this fresh network namespace only.
        # Runtime peers, passt, QEMU and Go tests start after the capability drop.
        for address in ("2001:db8:ffff::100/128", "2001:db8:ffff::101/128", "fe80::1/64"):
            completed = subprocess.run(["/usr/sbin/ip", "-6", "addr", "add", address, "dev", "lo", "nodad"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5, check=False)
            if completed.returncode:
                raise RuntimeError("ipv6_loopback_setup_failed")
    # This environment was constructed by the launcher, not copied from the
    # host. Explicitly retain only the runtime paths required by this fixture.
    keys = ("PATH", "HOME", "TMPDIR", "LD_LIBRARY_PATH", "QEMU_MODULE_DIR", "LANG", "TERM", "PYTHONDONTWRITEBYTECODE", "GOMAXPROCS", "GOMEMLIMIT")
    environment = {key: os.environ[key] for key in keys}
    args = [
        "/usr/bin/setpriv", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs",
        "/usr/bin/prlimit", "--as=4294967296", "--cpu=" + str(cpu_limit(options.native_case)), "--nproc=128", "--nofile=256", "--fsize=268435456", "--core=0",
        "/usr/bin/python3", "-I", "-S", "-B", "/lab/transport.py",
    ]
    if options.properties:
        args += ["--properties"]
    if options.ssh:
        args += ["--ssh"]
    if options.live:
        args += ["--live"]
    if options.bgp_cache:
        args += ["--bgp-cache"]
    if options.ssh_disconnect:
        args += ["--ssh-disconnect"]
    if options.bgp_generated_ipv4:
        args += ["--bgp-generated-ipv4"]
    if options.bgp_generated_ipv6:
        args += ["--bgp-generated-ipv6"]
    if options.native_case is not None:
        args += ["--case", options.native_case]
    os.execve(args[0], args, environment)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("fixture_privilege_setup=false", flush=True)
        raise SystemExit(1) from None
