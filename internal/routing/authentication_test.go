package routing

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixturePassword = "SyntheticOnly+/=_-4BGPSecret"
const fixtureAuthRevision = "abcdefghijklmnopqr--_0"

func authenticatedFixture() (Router, []Peer) {
	r, peers := pairedFixture()
	for i := range peers {
		peers[i].Password = fixturePassword
		peers[i].AuthenticationRevision = fixtureAuthRevision
	}
	return r, peers
}

func TestBGPPasswordValidationBoundary(t *testing.T) {
	for _, value := range []string{"a", "+/=_-", strings.Repeat("A", 80), fixturePassword} {
		if err := ValidateBGPPassword(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"", strings.Repeat("A", 81), "包含中文", "trailing ", " leading", "line\nbreak", "tab\tkey", "nul\x00key", `quote"key`, `back\slash`, "semi;colon", "$variable", RedactedBGPPassword} {
		if err := ValidateBGPPassword(value); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
	r, peers := authenticatedFixture()
	for _, revision := range []string{"", "short", strings.Repeat("a", 23), "abcdefghijklmnopqrst;#"} {
		peers[0].AuthenticationRevision = revision
		if _, err := Render(r, peers); err == nil {
			t.Fatal("invalid authentication revision accepted")
		}
	}
	peers[0].Enabled = false
	peers[0].Password = "unsafe\nkey"
	if _, err := Render(r, peers); err == nil {
		t.Fatal("disabled peer hid unsafe authentication input")
	}
}

func TestBGPAuthenticationRenderAndRedaction(t *testing.T) {
	for _, platform := range []string{"bird2", "frr", "junos", "routeros7"} {
		t.Run(platform, func(t *testing.T) {
			r, peers := authenticatedFixture()
			config, err := RenderFor(platform, r, peers)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(config, fixturePassword) != len(peers) {
				t.Fatal("credential omitted or duplicated")
			}
			masked, err := RedactConfig(platform, config)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(masked, fixturePassword) || strings.Count(masked, RedactedBGPPassword) != len(peers) {
				t.Fatal("credential leaked or redaction lost")
			}
			if platform == "junos" {
				var doc struct {
					Groups struct {
						Operation string `xml:"urn:ietf:params:xml:ns:netconf:base:1.0 operation,attr"`
					} `xml:"groups"`
				}
				if err := xml.Unmarshal([]byte(masked), &doc); err != nil || doc.Groups.Operation != "replace" || strings.Contains(masked, "_xmlns") {
					t.Fatal("Junos preview changed the managed replacement namespace")
				}
			}
			for i := range peers {
				peers[i].Password = "OtherSecret+/_-="
			}
			other, _ := RenderFor(platform, r, peers)
			otherMasked, err := RedactConfig(platform, other)
			if err != nil || otherMasked != masked {
				t.Fatal("masked artifact reveals password changes without public revision")
			}
			peers[0].AuthenticationRevision = "zyxwvutsrqponmlkjihgfe"
			rotated, _ := RenderFor(platform, r, peers)
			rotatedMasked, err := RedactConfig(platform, rotated)
			if err != nil || rotatedMasked == masked {
				t.Fatal("rotation revision invisible in review")
			}
			peers[0].Password = ""
			removed, _ := RenderFor(platform, r, peers)
			removedMasked, err := RedactConfig(platform, removed)
			if err != nil || !strings.Contains(removedMasked, "state=disabled") || strings.Count(removedMasked, RedactedBGPPassword) != 1 {
				t.Fatal("removal revision lost")
			}
			if platform == "bird2" || platform == "frr" {
				r.RPKI = &RPKIConfig{Host: "127.0.0.1", Policy: "require-valid", ExpireSeconds: 600}
				config, err = RenderFor(platform, r, peers)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = RedactConfig(platform, config); err != nil {
					t.Fatal("redaction rejected runtime RPKI profile:", err)
				}
			}
		})
	}
}

func TestBGPSecretExcludedFromLoggingAndJSON(t *testing.T) {
	_, peers := authenticatedFixture()
	data, err := json.Marshal(peers)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(data), fmt.Sprint(peers), fmt.Sprintf("%+v", peers), fmt.Sprintf("%#v", peers)} {
		if strings.Contains(output, fixturePassword) {
			t.Fatal("peer logging exposed secret")
		}
	}
}

func TestBGPRedactorFailsClosed(t *testing.T) {
	for _, platform := range []string{"bird2", "frr", "junos", "routeros7"} {
		plainRouter, plainPeers := fixture()
		for _, legacyPeers := range [][]Peer{plainPeers, nil} {
			plain, err := RenderFor(platform, plainRouter, legacyPeers)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := RedactConfig(platform, plain)
			if err != nil || strings.Contains(preview, RedactedBGPPassword) {
				t.Fatal("uncredentialed generated artifact rejected", platform, err)
			}
		}
		r, peers := authenticatedFixture()
		config, _ := RenderFor(platform, r, peers)
		for _, malformed := range []string{strings.ReplaceAll(config, fixturePassword, fixturePassword+" unexpected"), config + "\nunknown " + fixturePassword, strings.ReplaceAll(config, fixturePassword, "line\nbreak"), config + "\x00", strings.ReplaceAll(config, fixturePassword, fixturePassword+"\u00a0"), strings.ReplaceAll(config, " neighbor ", "\u00a0neighbor ")} {
			if malformed == config {
				continue
			}
			preview, err := RedactConfig(platform, malformed)
			if err == nil || preview != "" || strings.Contains(err.Error(), fixturePassword) {
				t.Fatal(platform, "returned unsafe preview/error")
			}
		}
	}
}

func TestBGPRedactedArtifactsNeverValidatedOrApplied(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	marker := filepath.Join(dir, "native-called")
	binary := executable(t, dir, "native", "touch "+shellLiteral(marker)+"\nexit 0\n")
	r, peers := authenticatedFixture()
	for _, platform := range []string{"bird2", "frr", "junos", "routeros7"} {
		config, _ := RenderFor(platform, r, peers)
		preview, err := RedactConfig(platform, config)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateFor(ctx, platform, binary, preview); err == nil || !strings.Contains(err.Error(), "redacted previews") {
			t.Fatal("redacted preview accepted", platform)
		}
		if platform == "bird2" {
			target := filepath.Join(dir, "bird.conf")
			if err = os.WriteFile(target, []byte("previous"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = Apply(ctx, preview, ApplyOptions{BirdBinary: binary, BirdcBinary: binary, Target: target, Checksum: Checksum(preview)}); err == nil {
				t.Fatal("redacted BIRD applied")
			}
			assertFile(t, target, "previous")
			if _, err = os.Stat(target + ".moedove-lock"); !os.IsNotExist(err) {
				t.Fatal("redacted artifact touched apply lock")
			}
		}
		if platform == "frr" && ValidateManagedFRR(preview) == nil {
			t.Fatal("managed FRR accepted placeholder")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("redacted artifact invoked native tool")
	}
}

func TestBGPDiagnosticsNeverEchoSecrets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, exit := range []string{"exit 1", "exit 0"} {
		bird := executable(t, dir, "bird", "if [ \"$1\" = --version ]; then echo 'BIRD version 2.17.5'; exit 0; fi\necho 'syntax error "+fixturePassword+" SyntheticOnly'; exit 1\n")
		if err := Validate(ctx, bird, "private-config"); err == nil || strings.Contains(err.Error(), "SyntheticOnly") {
			t.Fatal("BIRD diagnostic leaked")
		}
		frr := executable(t, dir, "bgpd", "if [ \"$1\" = --version ]; then echo 'bgpd version 10.3'; exit 0; fi\necho 'Unknown command "+fixturePassword+" SyntheticOnly'; "+exit+"\n")
		if err := validateFRR(ctx, frr, "private-config"); err == nil || strings.Contains(err.Error(), "SyntheticOnly") {
			t.Fatal("FRR diagnostic leaked or error ignored")
		}
	}
	if err := checkControlReply("8001 syntax error "+fixturePassword, []string{"configure"}); err == nil || strings.Contains(err.Error(), "SyntheticOnly") {
		t.Fatal("BIRD control leaked")
	}
	if err := ValidateJunosXML("<configuration><" + fixturePassword + "></configuration>"); err == nil || strings.Contains(err.Error(), "SyntheticOnly") {
		t.Fatal("XML syntax error leaked")
	}
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	w.opts.Reload = executable(t, dir, "reload", "echo 'ERROR "+fixturePassword+" SyntheticOnly'; exit 1\n")
	if err := w.reload(ctx, h.opts.Target); err == nil || strings.Contains(err.Error(), "SyntheticOnly") {
		t.Fatal("reload diagnostic leaked")
	}
}

func TestNativeBGPAuthentication(t *testing.T) {
	for _, pair := range []struct{ platform, env string }{{"bird2", "BIRD_BIN"}, {"frr", "FRR_BIN"}} {
		binary := os.Getenv(pair.env)
		if binary == "" {
			t.Log(pair.env, "unset; native authentication check skipped")
			continue
		}
		for _, password := range []string{"a", fixturePassword, strings.Repeat("A", 80)} {
			r, peers := authenticatedFixture()
			for i := range peers {
				peers[i].Password = password
			}
			config, err := RenderFor(pair.platform, r, peers)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateFor(context.Background(), pair.platform, binary, config); err != nil {
				t.Fatal(pair.platform, err)
			}
			if pair.platform == "bird2" && password == fixturePassword && os.Getenv("BIRD_AUTH_ARTIFACT") != "" {
				if err = os.WriteFile(os.Getenv("BIRD_AUTH_ARTIFACT"), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestBGPAuthenticationPrivateTarget(t *testing.T) {
	r, peers := authenticatedFixture()
	for _, mode := range []os.FileMode{0600, 0640} {
		for _, platform := range []string{"bird2", "frr"} {
			config, _ := RenderFor(platform, r, peers)
			if err := privateAuthenticationTarget(config, mode); err != nil {
				t.Fatal("private target refused", err)
			}
		}
	}
	for _, mode := range []os.FileMode{0644, 0660, 0610, 0700, 0601} {
		for _, platform := range []string{"bird2", "frr"} {
			config, _ := RenderFor(platform, r, peers)
			if privateAuthenticationTarget(config, mode) == nil {
				t.Fatal("unsafe key permissions accepted")
			}
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	if err := os.WriteFile(target, []byte("previous"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	config, _ := Render(r, peers)
	if err := Apply(context.Background(), config, ApplyOptions{Target: target, Checksum: Checksum(config), BirdBinary: fakeBird(t, dir)}); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatal("world-readable BIRD candidate accepted", err)
	}
	assertFile(t, target, "previous")
	h := newFRRTestHost(t)
	w := openTestWatchdog(t, h)
	if err := os.Chmod(h.opts.Target, 0644); err != nil {
		t.Fatal(err)
	}
	config, _ = RenderFor("frr", r, peers)
	req := h.request()
	req.Config = config
	req.Checksum = Checksum(config)
	if _, err := w.Stage(context.Background(), req); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatal("world-readable FRR candidate accepted", err)
	}
	assertFile(t, h.opts.Target, h.old)
	if _, err := os.Stat(filepath.Join(h.opts.StateDir, "candidate.conf")); !os.IsNotExist(err) {
		t.Fatal("permission refusal wrote candidate")
	}
}

func TestBGPRecoveryAndBootstrapPrivateFiles(t *testing.T) {
	ctx := context.Background()
	r, peers := authenticatedFixture()
	config, _ := Render(r, peers)
	dir := t.TempDir()
	target := filepath.Join(dir, "bird.conf")
	backup := target + ".moedove-previous"
	for path, value := range map[string]string{target: "unconfirmed", backup: config, target + ".moedove-pending": Checksum(config)} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(backup, 0660); err != nil {
		t.Fatal(err)
	}
	opts := ApplyOptions{Target: target, BirdBinary: fakeBird(t, dir), BirdcBinary: executable(t, dir, "birdc", "echo '0003 Reconfigured'\n")}
	if err := Recover(ctx, opts); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatal("public/group-writable recovery key installed", err)
	}
	assertFile(t, target, "unconfirmed")
	if err := os.Chmod(backup, 0640); err != nil {
		t.Fatal(err)
	}
	if err := Recover(ctx, opts); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, config)
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("restored credential permissions lost")
	}
	h := newFRRTestHost(t)
	frr, _ := RenderFor("frr", r, peers)
	if err := os.WriteFile(h.opts.Target, []byte("unmanaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(h.opts.Target, 0644); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapFRR(ctx, h.opts.Target, h.opts.Validator, frr, Checksum(frr), Checksum("unmanaged")); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatal("bootstrap wrote public key", err)
	}
	assertFile(t, h.opts.Target, "unmanaged")
	if _, err := os.Stat(h.opts.Target + ".moedove-bootstrap-original"); !os.IsNotExist(err) {
		t.Fatal("failed permission check changed bootstrap files")
	}
}

func TestBGPFRRRollbackCredentialsRemainPrivate(t *testing.T) {
	h := newFRRTestHost(t)
	r, peers := authenticatedFixture()
	h.old, _ = RenderFor("frr", r, peers)
	peers[0].MaxPrefixes++
	h.new, _ = RenderFor("frr", r, peers)
	for _, path := range []string{h.opts.Target, h.live} {
		if err := os.WriteFile(path, []byte(h.old), 0640); err != nil {
			t.Fatal(err)
		}
	}
	w := openTestWatchdog(t, h)
	s, err := w.Stage(context.Background(), h.request())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(h.opts.Target, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Cancel(FRRDecision{ID: s.ID, Checksum: s.Checksum}); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatal("rollback installed secret into insecure target", err)
	}
	assertFile(t, h.opts.Target, h.new)
	if w.Status().Status != "rollback-failed" {
		t.Fatal("unsafe recovery failure was hidden")
	}
	if err = os.Chmod(h.opts.Target, 0640); err != nil {
		t.Fatal(err)
	}
	if err = w.Tick(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, h.opts.Target, h.old)
	for _, path := range []string{"previous.conf", "candidate.conf"} {
		if info, err := os.Stat(filepath.Join(h.opts.StateDir, path)); err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("watchdog key snapshot is not private")
		}
	}
}

func TestBGPInlineAuthenticationPrivateFiles(t *testing.T) {
	inline := `protocol bgp example { password "SyntheticInlineOnly"; }`
	for _, candidate := range []string{inline, "protocol bgp x { PASSWORD\t\"SyntheticInlineOnly\"; }", "# password is a conservative permission signal"} {
		if privateAuthenticationTarget(candidate, 0644) == nil {
			t.Fatal("inline or mixed-case authentication escaped permission check")
		}
	}
	for _, previous := range []bool{false, true} {
		dir := t.TempDir()
		target := filepath.Join(dir, "bird.conf")
		old, next := "previous", inline
		if previous {
			old, next = inline, "replacement"
		}
		if err := os.WriteFile(target, []byte(old), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0644); err != nil {
			t.Fatal(err)
		}
		if err := Apply(context.Background(), next, ApplyOptions{Target: target, Checksum: Checksum(next)}); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatal("inline candidate or previous key accepted insecure permissions", err)
		}
		assertFile(t, target, old)
		backup := target + ".moedove-previous"
		if err := os.WriteFile(backup, []byte(inline), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(backup, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target+".moedove-pending", []byte(Checksum(inline)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Recover(context.Background(), ApplyOptions{Target: target}); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatal("inline old key recovery accepted insecure permissions", err)
		}
		assertFile(t, target, old)
	}
}
