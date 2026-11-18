package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBIRD1V1HistoryAndV2RuntimeDigestPrivacy(t *testing.T) {
	router, peers := bird1Input(4)
	old, e := renderBIRD1Version(router, peers, 1)
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := parseManagedBIRD1(old)
	if e != nil || parsed.Version != 1 {
		t.Fatal("v1 exact historical decoder changed", e)
	}
	if _, e = bird1RuntimeFingerprint(old); e == nil {
		t.Fatal("v1 was treated as runtime evidence")
	}
	redacted, e := RedactBIRD1(old)
	if e != nil || strings.Contains(redacted, peers[0].Password) {
		t.Fatal("v1 preview broke", e)
	}
	next, e := RenderBIRD1(router, peers)
	if e != nil {
		t.Fatal(e)
	}
	fingerprint, e := bird1RuntimeFingerprint(next)
	if e != nil || !bird1HashOK(fingerprint) {
		t.Fatal(e)
	}
	changed := append([]Peer{}, peers...)
	changed[0].Password = "Different_Synthetic_Key"
	other, e := RenderBIRD1(router, changed)
	if e != nil {
		t.Fatal(e)
	}
	otherFingerprint, _ := bird1RuntimeFingerprint(other)
	if fingerprint == otherFingerprint {
		t.Fatal("runtime fingerprint omitted authentication bytes")
	}
	redacted, e = RedactConfig("bird1", next)
	if e != nil || strings.Contains(redacted, fingerprint) || strings.Contains(redacted, peers[0].Password) || !strings.Contains(redacted, "[REDACTED-RUNTIME-DIGEST]") {
		t.Fatal("private digest escaped public preview", e)
	}
	if e = ValidateManagedBIRD1(redacted); e == nil {
		t.Fatal("redacted digest authorized execution")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "current.conf")
	if e = os.WriteFile(target, []byte(old), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Apply(context.Background(), "router id 192.0.2.1;\n", ApplyOptions{Target: target, Checksum: Checksum("router id 192.0.2.1;\n")}); e == nil || !strings.Contains(e.Error(), "dedicated") {
		t.Fatal("newer generic apply took over BIRD1 baseline", e)
	}
}
func TestBIRD1ReadyTextAndResponseCodes(t *testing.T) {
	for _, test := range []struct {
		line string
		want error
	}{{"0013 Reconfiguration in progress", errReconfiguring}, {"0013 Shutdown in progress", errors.New("not-ready")}, {"0013 Daemon is up and running", nil}} {
		h := &bird1Host{invoke: func(context.Context, string) (bird1Reply, error) {
			return bird1Reply{code: 13, lines: []string{"1000-BIRD 1.6.8", test.line}}, nil
		}}
		e := h.ready(context.Background())
		if test.want == nil && e != nil || test.want == errReconfiguring && !errors.Is(e, errReconfiguring) || test.want != nil && test.want != errReconfiguring && e == nil {
			t.Fatal("readiness text ignored", test.line, e)
		}
	}
	for _, code := range []int{0, 5, 6, 17, 19, 8002, 9001} {
		h := &bird1Host{opts: BIRD1ApplyOptions{Target: "/private/current.conf", TimeoutSeconds: 2}, invoke: func(context.Context, string) (bird1Reply, error) { return bird1Reply{code: code}, nil }}
		if e := h.configure(context.Background(), true); e == nil {
			t.Fatal("ambiguous configure reply accepted", code)
		}
		if e := h.confirm(context.Background()); e == nil {
			t.Fatal("nonconfirmation accepted", code)
		}
	}
}

type bird1NativeFixture struct {
	root       string
	opts       BIRD1ApplyOptions
	base, next string
	daemon     *exec.Cmd
}

func bird1RequireApplyNative(t *testing.T, family int) string {
	t.Helper()
	if os.Getenv("BIRD1_APPLY_NATIVE") != "1" {
		t.Skip("explicit isolated BIRD1 live-control fixture required")
	}
	raw, e := os.ReadFile("/proc/self/status")
	if e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs"} {
		want := "0000000000000000"
		if field == "NoNewPrivs" {
			want = "1"
		}
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field+":" {
				found = parts[1] == want
			}
		}
		if !found {
			t.Fatal("native apply fixture requires zero capabilities and no_new_privs")
		}
	}
	interfaces, e := net.Interfaces()
	if e != nil {
		t.Fatal(e)
	}
	for _, iface := range interfaces {
		if iface.Name != "lo" {
			t.Fatal("native apply fixture may not expose external interfaces")
		}
	}
	key := "BIRD1_BIN"
	if family == 6 {
		key = "BIRD1_IPV6_BIN"
	}
	binary := os.Getenv(key)
	if binary == "" {
		t.Fatal("actual BIRD1 family binary missing")
	}
	return binary
}
func newBird1NativeFixture(t *testing.T, family int) *bird1NativeFixture {
	t.Helper()
	binary := bird1RequireApplyNative(t, family)
	root, e := os.MkdirTemp("", "bird1-apply-")
	if e != nil {
		t.Fatal(e)
	}
	router, peers := bird1Input(family)
	peers = peers[:1]
	base, e := RenderBIRD1(router, peers)
	if e != nil {
		t.Fatal(e)
	}
	nextPeer := append([]Peer{}, peers...)
	nextPeer[0].MaxPrefixes++
	prefix := "192.0.2.0/24"
	if family == 6 {
		prefix = "2001:db8:3::/48"
	}
	nextPeer[0].AllowedPrefixes = append(nextPeer[0].AllowedPrefixes, prefix)
	nextPeer[0].AllowedRoutes = append(nextPeer[0].AllowedRoutes, PrefixOrigin{Prefix: prefix, OriginASN: 64513})
	next, e := RenderBIRD1(router, nextPeer)
	if e != nil {
		t.Fatal(e)
	}
	fixture := &bird1NativeFixture{root: root, base: base, next: next, opts: BIRD1ApplyOptions{BirdBinary: binary, Target: filepath.Join(root, "current.conf"), Socket: filepath.Join(root, "control.sock"), BaseChecksum: Checksum(base), Checksum: Checksum(next), ExclusiveInstanceAcknowledged: true, TimeoutSeconds: 2}}
	if e = os.WriteFile(fixture.opts.Target, []byte(base), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { fixture.stop(); os.RemoveAll(root) })
	fixture.start(t)
	return fixture
}
func (f *bird1NativeFixture) start(t *testing.T) {
	t.Helper()
	command := exec.Command(f.opts.BirdBinary, "-f", "-c", f.opts.Target, "-s", f.opts.Socket, "-P", filepath.Join(f.root, "bird.pid"))
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if e := command.Start(); e != nil {
		t.Fatal(e)
	}
	f.daemon = command
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		host, e := newBird1Host(f.opts)
		if e == nil {
			_, e = host.readback(context.Background())
		}
		if e == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("real BIRD1 daemon did not expose ready v2 readback")
}
func (f *bird1NativeFixture) stop() {
	if f.daemon == nil {
		return
	}
	f.daemon.Process.Kill()
	f.daemon.Wait()
	f.daemon = nil
}
func (f *bird1NativeFixture) require(t *testing.T, want string, pending bool) {
	t.Helper()
	raw, _, e := bird1PrivateRead(f.opts.Target, maxConfigSize)
	if e != nil || string(raw) != want {
		t.Fatal("exact disk readback differs", e)
	}
	host, e := newBird1Host(f.opts)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := host.readback(context.Background())
	expected, _ := bird1RuntimeFingerprint(want)
	if e != nil || actual != expected {
		t.Fatal("exact native runtime readback differs", e)
	}
	_, e = os.Stat(f.opts.Target + ".moedove-pending")
	if pending && e != nil || !pending && !os.IsNotExist(e) {
		t.Fatal("unexpected durable pending state", e)
	}
}
func TestBIRD1NativeApplyAndUndoDeadline(t *testing.T) {
	for _, family := range []int{4, 6} {
		t.Run(strconv.Itoa(family), func(t *testing.T) {
			f := newBird1NativeFixture(t, family)
			if e := ApplyBIRD1(context.Background(), f.next, f.opts); e != nil {
				t.Fatal(e)
			}
			f.require(t, f.next, false)
			time.Sleep(2200 * time.Millisecond)
			f.require(t, f.next, false)
			backup, _, e := bird1PrivateRead(f.opts.Target+".moedove-previous", maxConfigSize)
			if e != nil || string(backup) != f.base {
				t.Fatal("previous exact bytes lost", e)
			}
			if e = ApplyBIRD1(context.Background(), f.next, f.opts); e == nil {
				t.Fatal("stale base silently replaced the rollback snapshot")
			}
		})
	}
}
func TestBIRD1NativeLostReplyAndErrorRestoreBaseline(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, failure := range []string{"configure-reply-lost", "confirm-reply-lost", "native-error"} {
			t.Run(strconv.Itoa(family)+"/"+failure, func(t *testing.T) {
				f := newBird1NativeFixture(t, family)
				injected := false
				factory := func(opts BIRD1ApplyOptions) (*bird1Host, error) {
					h, e := newBird1Host(opts)
					if e != nil {
						return nil, e
					}
					invoke := h.invoke
					h.invoke = func(ctx context.Context, command string) (bird1Reply, error) {
						timed := strings.HasSuffix(command, " timeout 2")
						if !injected && (timed && (failure == "configure-reply-lost" || failure == "native-error") || command == "configure confirm" && failure == "confirm-reply-lost") {
							injected = true
							if failure == "native-error" {
								return invoke(ctx, "configure check \"/scratch/not-present.conf\"")
							}
							reply, e := invoke(ctx, command)
							if e != nil {
								return reply, e
							}
							return reply, errors.New("injected lost acknowledgment")
						}
						return invoke(ctx, command)
					}
					return h, nil
				}
				e := applyBIRD1(context.Background(), f.next, f.opts, factory)
				if e == nil || !injected || !strings.Contains(e.Error(), "verified restored") {
					t.Fatal("uncertain/error result did not restore verified baseline", e)
				}
				f.require(t, f.base, false)
			})
		}
	}
}

func TestBIRD1NativeDaemonAbortAndExplicitRecovery(t *testing.T) {
	for _, family := range []int{4, 6} {
		t.Run(strconv.Itoa(family), func(t *testing.T) {
			f := newBird1NativeFixture(t, family)
			injected := false
			factory := func(opts BIRD1ApplyOptions) (*bird1Host, error) {
				h, e := newBird1Host(opts)
				if e != nil {
					return nil, e
				}
				invoke := h.invoke
				h.invoke = func(ctx context.Context, command string) (bird1Reply, error) {
					reply, e := invoke(ctx, command)
					if e == nil && !injected && strings.HasSuffix(command, " timeout 2") {
						injected = true
						f.stop()
						return reply, errors.New("actual daemon aborted after configure")
					}
					return reply, e
				}
				return h, nil
			}
			if e := applyBIRD1(context.Background(), f.next, f.opts, factory); e == nil || !strings.Contains(e.Error(), "rollback incomplete") {
				t.Fatal("daemon abort did not retain uncertainty", e)
			}
			if _, e := readBIRD1Journal(f.opts.Target); e != nil {
				t.Fatal("journal missing after actual daemon death", e)
			}
			f.start(t)
			if e := RecoverBIRD1(context.Background(), f.opts); e != nil {
				t.Fatal("explicit restarted-daemon recovery", e)
			}
			f.require(t, f.base, false)
		})
	}
}

type bird1CrashRequest struct {
	Options          BIRD1ApplyOptions
	Candidate, Phase string
}

func TestBIRD1CrashProcessHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "bird1-crash-helper" && i+1 < len(os.Args) {
			raw, e := os.ReadFile(os.Args[i+1])
			if e != nil {
				os.Exit(71)
			}
			var request bird1CrashRequest
			if json.Unmarshal(raw, &request) != nil {
				os.Exit(72)
			}
			request.Options.checkpoint = func(phase string) {
				if phase == request.Phase {
					os.Exit(66)
				}
			}
			if ApplyBIRD1(context.Background(), request.Candidate, request.Options) != nil {
				os.Exit(73)
			}
			os.Exit(74)
		}
	}
}
func TestBIRD1NativeAgentCrashPositionsRequireExplicitRecovery(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, phase := range []string{"prepared", "candidate-staged", "configured", "confirmed", "published"} {
			t.Run(strconv.Itoa(family)+"/"+phase, func(t *testing.T) {
				f := newBird1NativeFixture(t, family)
				path := filepath.Join(f.root, "crash-request.json")
				raw, _ := json.Marshal(bird1CrashRequest{Options: f.opts, Candidate: f.next, Phase: phase})
				if e := os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
				executable, e := os.Executable()
				if e != nil {
					t.Fatal(e)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				process := exec.CommandContext(ctx, executable, "-test.run=^TestBIRD1CrashProcessHelper$", "bird1-crash-helper", path)
				process.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "TMPDIR=" + os.TempDir()}
				e = process.Run()
				var exit *exec.ExitError
				if !errors.As(e, &exit) || exit.ExitCode() != 66 {
					t.Fatal("real agent process did not terminate at durable boundary", e)
				}
				before, _ := os.ReadFile(f.opts.Target + ".moedove-pending")
				if e = ApplyBIRD1(context.Background(), f.next, f.opts); e == nil {
					t.Fatal("ordinary apply resumed an interrupted transaction")
				}
				after, _ := os.ReadFile(f.opts.Target + ".moedove-pending")
				if string(before) != string(after) {
					t.Fatal("refusal altered the pending journal")
				}
				if phase == "configured" {
					time.Sleep(2200 * time.Millisecond)
				} // native deadline may independently restore runtime
				if binary := os.Getenv("BIRD1_AGENT_BIN"); phase == "confirmed" && binary != "" {
					command := exec.CommandContext(ctx, binary, "recover", "--platform", "bird1", "--validator", f.opts.BirdBinary, "--target", f.opts.Target, "--socket", f.opts.Socket, "--base-checksum", f.opts.BaseChecksum, "--checksum", f.opts.Checksum, "--exclusive-instance-acknowledged")
					command.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=" + os.TempDir(), "LANG=C"}
					e = command.Run()
				} else {
					e = RecoverBIRD1(context.Background(), f.opts)
				}
				if e != nil {
					t.Fatal("explicit interrupted recovery", e)
				}
				f.require(t, f.base, false)
			})
		}
	}
}
func TestBIRD1NativeApplyRefusesWrongBindingsAndDrift(t *testing.T) {
	for _, name := range []string{"ack", "base", "family", "entity", "v1-baseline", "unknown-runtime", "changed-snapshot", "third-disk"} {
		t.Run(name, func(t *testing.T) {
			f := newBird1NativeFixture(t, 4)
			opts := f.opts
			config := f.next
			switch name {
			case "ack":
				opts.ExclusiveInstanceAcknowledged = false
			case "base":
				opts.BaseChecksum = strings.Repeat("a", 64)
			case "family":
				opts.BirdBinary = os.Getenv("BIRD1_IPV6_BIN")
			case "entity":
				model, _ := parseManagedBIRD1(config)
				model.Router.ID = "another-router"
				config, _ = RenderBIRD1(model.Router, model.Peers)
				opts.Checksum = Checksum(config)
			case "v1-baseline":
				f.stop()
				model, _ := parseManagedBIRD1(f.base)
				old, _ := renderBIRD1Version(model.Router, model.Peers, 1)
				os.WriteFile(opts.Target, []byte(old), 0600)
				opts.BaseChecksum = Checksum(old)
			case "unknown-runtime":
				model, _ := parseManagedBIRD1(f.base)
				model.Router.Name = "unreviewed runtime"
				foreign, _ := RenderBIRD1(model.Router, model.Peers)
				os.WriteFile(opts.Target, []byte(foreign), 0600)
				host, _ := newBird1Host(opts)
				if e := host.configure(context.Background(), false); e != nil {
					t.Fatal(e)
				}
				os.WriteFile(opts.Target, []byte(f.base), 0600)
			case "changed-snapshot", "third-disk":
				opts.checkpoint = func(phase string) {
					if phase == "configured" {
						if name == "changed-snapshot" {
							os.WriteFile(opts.Target+".moedove-previous", []byte("changed"), 0600)
						} else {
							os.WriteFile(opts.Target, []byte("unrecognized third bytes"), 0600)
						}
						panic("fixture interruption")
					}
				}
				func() { defer func() { _ = recover() }(); _ = ApplyBIRD1(context.Background(), config, opts) }()
				opts.checkpoint = nil
				before, _ := os.ReadFile(opts.Target)
				if e := RecoverBIRD1(context.Background(), opts); e == nil {
					t.Fatal("changed recovery evidence accepted")
				}
				after, _ := os.ReadFile(opts.Target)
				if string(before) != string(after) {
					t.Fatal("changed recovery material was overwritten")
				}
				return
			}
			before, _ := os.ReadFile(opts.Target)
			if e := ApplyBIRD1(context.Background(), config, opts); e == nil {
				t.Fatal("wrong binding accepted", name)
			}
			after, _ := os.ReadFile(opts.Target)
			if string(before) != string(after) {
				t.Fatal("preflight refusal changed target")
			}
		})
	}
}

func TestBIRD1NativeSocketCannotTargetAnotherFamily(t *testing.T) {
	first := newBird1NativeFixture(t, 4)
	second := newBird1NativeFixture(t, 6)
	opts := first.opts
	opts.Socket = second.opts.Socket
	if e := ApplyBIRD1(context.Background(), first.next, opts); e == nil {
		t.Fatal("different-family socket was adopted")
	}
	first.require(t, first.base, false)
	second.require(t, second.base, false)
}
func TestBIRD1NativeCLIApplyUsesDedicatedEntry(t *testing.T) {
	binary := os.Getenv("BIRD1_AGENT_BIN")
	if binary == "" {
		t.Skip("actual built ixp-agent fixture required")
	}
	for _, family := range []int{4, 6} {
		t.Run(strconv.Itoa(family), func(t *testing.T) {
			f := newBird1NativeFixture(t, family)
			file := filepath.Join(f.root, "approved.conf")
			if e := os.WriteFile(file, []byte(f.next), 0600); e != nil {
				t.Fatal(e)
			}
			args := []string{"apply", "--platform", "bird1", "--validator", f.opts.BirdBinary, "--target", f.opts.Target, "--socket", f.opts.Socket, "--base-checksum", f.opts.BaseChecksum, "--checksum", f.opts.Checksum, "--file", file, "--timeout", "2", "--exclusive-instance-acknowledged"}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=" + os.TempDir(), "LANG=C"}
			if e := cmd.Run(); e != nil {
				t.Fatal("actual CLI dedicated apply failed", e)
			}
			f.require(t, f.next, false)
		})
	}
}

func TestBIRD1NativeDeathAfterPublicationKeepsRecoveryAuthority(t *testing.T) {
	for _, family := range []int{4, 6} {
		t.Run(strconv.Itoa(family), func(t *testing.T) {
			f := newBird1NativeFixture(t, family)
			opts := f.opts
			opts.checkpoint = func(phase string) {
				if phase == "published" {
					f.stop()
				}
			}
			if e := ApplyBIRD1(context.Background(), f.next, opts); e == nil || !strings.Contains(e.Error(), "rollback incomplete") {
				t.Fatal("daemon death after publication was reported as success", e)
			}
			f.start(t)
			if e := RecoverBIRD1(context.Background(), f.opts); e != nil {
				t.Fatal(e)
			}
			f.require(t, f.base, false)
		})
	}
}

func TestBIRD1NativeRouteServerCollectorTransition(t *testing.T) {
	for _, family := range []int{4, 6} {
		t.Run(strconv.Itoa(family), func(t *testing.T) {
			f := newBird1NativeFixture(t, family)
			model, e := parseManagedBIRD1(f.next)
			if e != nil {
				t.Fatal(e)
			}
			model.Router.Role = RoleRouteCollector
			model.Router.CollectorPolicy = CollectorObserve
			f.next, e = RenderBIRD1(model.Router, model.Peers)
			if e != nil {
				t.Fatal(e)
			}
			f.opts.Checksum = Checksum(f.next)
			if e = ApplyBIRD1(context.Background(), f.next, f.opts); e != nil {
				t.Fatal("actual RS to collector transition", e)
			}
			f.require(t, f.next, false)
			if strings.Contains(f.next, "rs client;") || !strings.Contains(f.next, "export none;") {
				t.Fatal("collector transition retained RS export")
			}
		})
	}
}
