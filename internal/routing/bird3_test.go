package routing

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fakeBIRD3(t *testing.T, dir string) string {
	return executable(t, dir, "bird3", "if [ \"$1\" = --version ]; then echo 'BIRD version 3.4.0'; exit 0; fi\nif [ \"$1\" = -p ] && [ \"$2\" = -c ]; then exit 0; fi\nexit 1\n")
}
func fakeBIRD3Control(t *testing.T, dir string, major string) string {
	return executable(t, dir, "birdc3", `case "$*" in
 *"show status"*) echo '1000-BIRD `+major+`.4.0'; echo '0013 Daemon is up and running';;
 *"configure confirm"*) echo '0018 Reconfiguration confirmed';;
 *"configure status"*) echo '0003-Daemon is up and running'; echo '0000 ';;
 *"configure "*) echo '0003 Reconfigured';;
 *) exit 1;;
esac
`)
}

func TestBIRD3TypedPolicyAndRedaction(t *testing.T) {
	for _, role := range []string{RoleRouteServer, RoleRouteCollector} {
		policies := []string{""}
		if role == RoleRouteCollector {
			policies = []string{CollectorObserve, CollectorAuthorizedOnly}
		}
		for _, policy := range policies {
			r, p := authenticatedFixture()
			r.Role = role
			r.CollectorPolicy = policy
			r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 600}
			config, err := RenderBIRD3(r, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, need := range []string{"BIRD 3;", "enforce first as on;", "bgp_path.first != 64513", "bgp_path.last = 64520", "authentication md5;", "import table yes;", "import keep filtered yes;", "receive limit 100 action disable;", "roa_check(md_roa4, net, bgp_path.last) != ROA_VALID", "rpki reload on;"} {
				if !strings.Contains(config, need) {
					t.Fatal("BIRD3 lost required policy", need)
				}
			}
			if strings.Contains(config, "protocol kernel") {
				t.Fatal("BIRD3 configured kernel installation")
			}
			if role == RoleRouteCollector && (strings.Count(config, "export none;") != len(p) || strings.Contains(config, "rs client;")) {
				t.Fatal("BIRD3 collector can export/server-client was retained")
			}
			preview, err := RedactBIRD3(config)
			if err != nil || strings.Contains(preview, fixturePassword) || strings.Count(preview, RedactedBGPPassword) != len(p) || !strings.Contains(preview, "BIRD 3;") {
				t.Fatal("BIRD3 preview leaked a key or lost target version", err)
			}
			if err = ValidateBIRD3(context.Background(), "never-run", preview); err == nil || !strings.Contains(err.Error(), "redacted previews") {
				t.Fatal("BIRD3 redacted artifact accepted")
			}
			if result, err := RedactBIRD3(config + "include \"/tmp/unsafe\";\n"); err == nil || result != "" {
				t.Fatal("unknown BIRD3 source returned in preview")
			}
		}
	}
	r, p := fixture()
	r.Role = RoleRouteCollector
	for i := range p {
		p[i].AllowedPrefixes = nil
	}
	if config, err := RenderBIRD3(r, p); err != nil || strings.Contains(config, "accept;") {
		t.Fatal("BIRD3 observation empty allowlist admitted route", err)
	}
	r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 60}
	if _, err := RenderBIRD3(r, p); err == nil {
		t.Fatal("BIRD3 accepted unsupported RPKI expiry")
	}
}

func TestBIRDRFC1997PassthroughRequiresExplicitSelection(t *testing.T) {
	for _, render := range []func(Router, []Peer) (string, error){Render, RenderBIRD3} {
		r, p := authenticatedFixture()
		baseline, err := render(r, p)
		if err != nil {
			t.Fatal(err)
		}
		no := false
		r.RFC1997Passthrough = &no
		explicitNo, err := render(r, p)
		if err != nil || explicitNo != baseline {
			t.Fatal("false/nil changed existing BIRD community behavior or bytes", err)
		}
		yes := true
		r.RFC1997Passthrough = &yes
		passthrough, err := render(r, p)
		if err != nil || strings.Count(passthrough, "interpret communities off;") != len(p) {
			t.Fatal("explicit RFC1997 pass-through was not rendered", err)
		}
		r.Role = RoleRouteCollector
		collector, err := render(r, p)
		if err != nil || strings.Count(collector, "export none;") != len(p) || strings.Contains(collector, "interpret communities off;") {
			t.Fatal("RFC1997 option relaxed collector export restriction", err)
		}
	}
}

func TestNativeBIRDRFC1997Passthrough(t *testing.T) {
	for _, tc := range []struct {
		env   string
		major int
	}{{"BIRD_BIN", 2}, {"BIRD3_BIN", 3}} {
		binary := os.Getenv(tc.env)
		if binary == "" {
			t.Log(tc.env, "unset; native explicit community check skipped")
			continue
		}
		r, p := authenticatedFixture()
		yes := true
		r.RFC1997Passthrough = &yes
		var config string
		var err error
		if tc.major == 2 {
			config, err = Render(r, p)
		} else {
			config, err = RenderBIRD3(r, p)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = validateBIRDMajor(context.Background(), binary, config, tc.major); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBIRD3VersionAndHeaderNeverDowngrade(t *testing.T) {
	r, p := fixture()
	two, _ := Render(r, p)
	three, _ := RenderBIRD3(r, p)
	dir := t.TempDir()
	bin2 := fakeBird(t, dir)
	bin3 := fakeBIRD3(t, dir)
	ctx := context.Background()
	for _, tc := range []struct {
		major          int
		binary, config string
	}{{3, bin2, three}, {2, bin3, two}, {2, bin2, three}, {3, bin3, two}, {4, bin3, three}} {
		if err := validateBIRDMajor(ctx, tc.binary, tc.config, tc.major); err == nil {
			t.Fatal("wrong BIRD binary/artifact major accepted")
		}
	}
	if err := ValidateBIRD3(ctx, bin3, three); err != nil {
		t.Fatal(err)
	}
	if err := Validate(ctx, bin2, two); err != nil {
		t.Fatal("BIRD2 compatibility lost", err)
	}
	if _, err := RedactBIRD3(two); err == nil {
		t.Fatal("BIRD2 artifact accepted by BIRD3 redactor")
	}
}

func TestBIRD3ApplyChecksDaemonAndBindsRecoveryVersion(t *testing.T) {
	r, p := authenticatedFixture()
	old, _ := RenderBIRD3(r, p)
	p[0].MaxPrefixes++
	next, _ := RenderBIRD3(r, p)
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	opts := ApplyOptions{BirdMajor: 3, BirdBinary: fakeBIRD3(t, dir), BirdcBinary: fakeBIRD3Control(t, dir, "2"), Target: target, Checksum: Checksum(next)}
	if err := Apply(context.Background(), next, opts); err == nil || !strings.Contains(err.Error(), "BIRD 3 daemon") {
		t.Fatal("BIRD3 candidate applied to another daemon major", err)
	}
	assertFile(t, target, old)
	if _, err := os.Stat(target + ".moedove-previous"); !os.IsNotExist(err) {
		t.Fatal("daemon mismatch changed recovery files")
	}
	opts.BirdcBinary = fakeBIRD3Control(t, dir, "3")
	if err := Apply(context.Background(), next, opts); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, next)
	assertFile(t, target+".moedove-previous", old)
	if err := writePendingMajor(target, Checksum(old), 3); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(target + ".moedove-pending")
	if err != nil || !strings.HasPrefix(string(marker), "bird3:") {
		t.Fatal("BIRD3 recovery journal was not version-bound")
	}
	wrong := opts
	wrong.BirdMajor = 0
	if err := Recover(context.Background(), wrong); err == nil || !strings.Contains(err.Error(), "different BIRD major") {
		t.Fatal("default BIRD2 recovery consumed BIRD3 journal", err)
	}
	assertFile(t, target, next)
	if err := Recover(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, old)
	if _, err := os.Stat(target + ".moedove-pending"); !os.IsNotExist(err) {
		t.Fatal("BIRD3 recovery journal not cleared")
	}
}

func TestBIRD3ApplyValidatesRollbackAndHidesNativeDiagnostics(t *testing.T) {
	r, p := fixture()
	two, _ := Render(r, p)
	three, _ := RenderBIRD3(r, p)
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte(two), 0600); err != nil {
		t.Fatal(err)
	}
	opts := ApplyOptions{BirdMajor: 3, BirdBinary: fakeBIRD3(t, dir), BirdcBinary: fakeBIRD3Control(t, dir, "3"), Target: target, Checksum: Checksum(three)}
	if err := Apply(context.Background(), three, opts); err == nil || !strings.Contains(err.Error(), "rollback snapshot") {
		t.Fatal("cross-major rollback baseline accepted", err)
	}
	assertFile(t, target, two)
	leaking := executable(t, dir, "leaking-bird3", "if [ \"$1\" = --version ]; then echo 'BIRD version 3.4.0'; exit 0; fi\necho 'syntax error SyntheticSecretFragment'; exit 1\n")
	if err := ValidateBIRD3(context.Background(), leaking, three); err == nil || strings.Contains(err.Error(), "SyntheticSecret") {
		t.Fatal("BIRD3 native diagnostics leaked")
	}
}

func TestBIRD3FailedConfigureRestoresPreviousRevision(t *testing.T) {
	r, p := authenticatedFixture()
	old, _ := RenderBIRD3(r, p)
	p[0].MaxPrefixes++
	next, _ := RenderBIRD3(r, p)
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	control := executable(t, dir, "birdc-fail", `case "$*" in
 *"show status"*) echo '1000-BIRD 3.4.0'; echo '0013 Daemon is up and running';;
 *"timeout 60"*) echo '8002 Native diagnostic SyntheticSecretFragment';;
 *"configure status"*) echo '0003-Daemon is up and running'; echo '0000 ';;
 *"configure "*) echo '0003 Reconfigured';;
 *) exit 1;;
esac
`)
	err := Apply(context.Background(), next, ApplyOptions{BirdMajor: 3, BirdBinary: fakeBIRD3(t, dir), BirdcBinary: control, Target: target, Checksum: Checksum(next)})
	if err == nil || !strings.Contains(err.Error(), "previous configuration restored") || strings.Contains(err.Error(), "SyntheticSecret") {
		t.Fatal("failed BIRD3 configure did not safely recover", err)
	}
	assertFile(t, target, old)
	if _, err := os.Stat(target + ".moedove-pending"); !os.IsNotExist(err) {
		t.Fatal("successful BIRD3 rollback retained pending marker")
	}
}

func TestNativeBIRD3Profiles(t *testing.T) {
	binary := os.Getenv("BIRD3_BIN")
	if binary == "" {
		t.Skip("set BIRD3_BIN for actual BIRD3 native parsing")
	}
	for _, role := range []string{RoleRouteServer, RoleRouteCollector} {
		policies := []string{""}
		if role == RoleRouteCollector {
			policies = []string{CollectorObserve, CollectorAuthorizedOnly}
		}
		for _, policy := range policies {
			for _, variant := range []string{"paired", "rpki", "empty"} {
				if variant == "empty" && (role != RoleRouteCollector || policy != CollectorObserve) {
					continue
				}
				r, p := authenticatedFixture()
				r.Role = role
				r.CollectorPolicy = policy
				if variant == "rpki" {
					r.RPKI = &RPKIConfig{Host: "127.0.0.1", Port: 3323, Policy: "require-valid", ExpireSeconds: 600}
				}
				if variant == "empty" {
					for i := range p {
						p[i].AllowedPrefixes = nil
						p[i].AllowedRoutes = nil
					}
				}
				config, err := RenderBIRD3(r, p)
				if err != nil {
					t.Fatal(err)
				}
				if err = ValidateBIRD3(context.Background(), binary, config); err != nil {
					t.Fatal(role, policy, variant, err)
				}
				if err = ValidateBIRD3(context.Background(), binary, config+"not-a-bird-command;\n"); err == nil {
					t.Fatal("BIRD3 parser accepted invalid configuration")
				}
				if dir := os.Getenv("BIRD3_ARTIFACT_DIR"); dir != "" {
					if err = os.WriteFile(filepath.Join(dir, role+"-"+policy+"-"+variant+".bird3"), []byte(config), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

// This uses the actual BIRD3 client against a private mock Unix control socket;
// no daemon is started and no routing session or kernel table is touched.
func TestNativeBIRD3ClientAgainstMockControlProtocol(t *testing.T) {
	bird, birdc := os.Getenv("BIRD3_BIN"), os.Getenv("BIRD3_BIRDC")
	if bird == "" || birdc == "" {
		t.Skip("set BIRD3_BIN and BIRD3_BIRDC for real-client/mock-control test")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	commands := []string{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				_, _ = conn.Write([]byte("0001 BIRD 3.4.0 ready.\n"))
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimSpace(line)
				mu.Lock()
				commands = append(commands, line)
				mu.Unlock()
				reply := "9001 Unsupported mock command\n"
				switch {
				case line == "show status":
					reply = "1000-BIRD 3.4.0\n0013 Daemon is up and running\n"
				case line == "configure status":
					reply = "0003-Daemon is up and running\n0000 \n"
				case line == "configure confirm":
					reply = "0018 Reconfiguration confirmed\n"
				case strings.HasPrefix(line, "configure \""):
					reply = "0022-Undo scheduled in 60 s\n0003 Reconfigured\n"
				}
				_, _ = conn.Write([]byte(reply))
			}()
		}
	}()
	defer func() { listener.Close(); <-done }()
	r, p := authenticatedFixture()
	old, _ := RenderBIRD3(r, p)
	p[0].MaxPrefixes++
	next, _ := RenderBIRD3(r, p)
	target := filepath.Join(dir, "bird.conf")
	if err = os.WriteFile(target, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	opts := ApplyOptions{BirdMajor: 3, BirdBinary: bird, BirdcBinary: birdc, Socket: socket, Target: target, Checksum: Checksum(next)}
	if err = Apply(context.Background(), next, opts); err != nil {
		t.Fatal("actual BIRD3 client/mock protocol rejected transaction", err)
	}
	assertFile(t, target, next)
	mu.Lock()
	seen := strings.Join(commands, "\n")
	mu.Unlock()
	for _, required := range []string{"show status", "timeout 60", "configure status", "configure confirm"} {
		if !strings.Contains(seen, required) {
			t.Fatal("native client skipped transaction command", required)
		}
	}
}
