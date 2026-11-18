package routing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are execution-entry contract tests with local simulated control tools.
// They do not claim a live BGP session or native forwarding verification.
func TestAS112BIRDApplyAndRecoveryEntrypoints(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3"} {
		t.Run(platform, func(t *testing.T) {
			r, p := as112RouteFixture(platform, "both", true, true)
			old, err := RenderFor(platform, r, p)
			if err != nil {
				t.Fatal(err)
			}
			r.AS112Service = "dname"
			next, err := RenderFor(platform, r, p)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "bird.conf")
			if err = os.WriteFile(target, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			opts := ApplyOptions{BirdMajor: 2, BirdBinary: fakeBird(t, dir), BirdcBinary: executable(t, dir, "birdc-contract", "case \"$*\" in *confirm*) echo '0018 Reconfiguration confirmed';; *) echo '0003 Reconfigured';; esac\n"), Target: target, Checksum: Checksum(next)}
			if platform == "bird3" {
				opts.BirdMajor = 3
				opts.BirdBinary = fakeBIRD3(t, dir)
				opts.BirdcBinary = fakeBIRD3Control(t, dir, "3")
			}
			if err = Apply(context.Background(), next, opts); err != nil {
				t.Fatal("AS112 apply entry rejected generated artifact", err)
			}
			assertFile(t, target, next)
			assertFile(t, target+".moedove-previous", old)
			if err = writePendingMajor(target, Checksum(old), opts.BirdMajor); err != nil {
				t.Fatal(err)
			}
			if err = Recover(context.Background(), opts); err != nil {
				t.Fatal("AS112 interrupted recovery rejected its own prior profile", err)
			}
			assertFile(t, target, old)
		})
	}
}
func TestAS112FRRWatchdogStageConfirmCancelScope(t *testing.T) {
	for _, decision := range []string{"confirm", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			h := newFRRTestHost(t)
			r, p := as112RouteFixture("frr", "both", true, true)
			var err error
			h.old, err = RenderFor("frr", r, p)
			if err != nil {
				t.Fatal(err)
			}
			r.AS112Service = "dname"
			h.new, err = RenderFor("frr", r, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{h.opts.Target, h.live} {
				if err = os.WriteFile(name, []byte(h.old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			w := openTestWatchdog(t, h)
			s, err := w.Stage(context.Background(), h.request())
			if err != nil || s.Status != "pending" {
				t.Fatal("AS112 FRR stage failed", err)
			}
			assertFile(t, h.live, h.new)
			if decision == "confirm" {
				s, err = w.Confirm(context.Background(), FRRDecision{ID: s.ID, Checksum: s.Checksum})
				if err != nil || s.Status != "confirmed" {
					t.Fatal(err)
				}
				assertFile(t, h.live, h.new)
			} else {
				s, err = w.Cancel(FRRDecision{ID: s.ID, Checksum: s.Checksum})
				if err != nil || s.Status != "cancelled" {
					t.Fatal("AS112 FRR cancellation failed", s.Status, err)
				}
				assertFile(t, h.live, h.old)
			}
		})
	}
}
func TestAS112OpenBGPDWatchdogAndAuthenticationScope(t *testing.T) {
	for _, decision := range []string{"confirm", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			h := newOpenBGPDTestHost(t)
			r, p := as112RouteFixture("openbgpd", "both", true, false)
			var err error
			h.old, err = RenderFor("openbgpd", r, p)
			if err != nil {
				t.Fatal(err)
			}
			r.AS112Service = "dname"
			p[0].Password = "Rotated-AS112-Secret"
			p[0].AuthenticationRevision = strings.Repeat("r", 22)
			h.new, err = RenderFor("openbgpd", r, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{h.opts.Target, h.live} {
				if err = os.WriteFile(name, []byte(h.old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			w := h.open(t)
			defer w.Close()
			s, err := w.Stage(context.Background(), h.request())
			if err != nil || s.Status != "pending" {
				t.Fatal("AS112 OpenBGPD stage failed", err)
			}
			if len(h.resets) != 1 || len(h.resets[0]) != 1 || h.resets[0][0] != p[0].Address {
				t.Fatal("AS112 model lost exact changed credential peer scope")
			}
			if decision == "confirm" {
				s, err = w.Confirm(context.Background(), OpenBGPDDecision{ID: s.ID, Checksum: s.Checksum})
				if err != nil || s.Status != "confirmed" {
					t.Fatal(err)
				}
				assertOpenBGPDFile(t, h.live, h.new)
			} else {
				s, err = w.Cancel(OpenBGPDDecision{ID: s.ID, Checksum: s.Checksum})
				if err != nil || s.Status != "cancelled" {
					t.Fatal("AS112 OpenBGPD cancel failed", s.Status, err)
				}
				assertOpenBGPDFile(t, h.live, h.old)
			}
		})
	}
}
