package live

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

// Read the two actual fixture NLRIs across all routing tables. Use a separate
// pinned, read-only invocation so these probes never claim the Safe Mode owner.
func nativeRSRIB(ctx context.Context, runtime *Runtime, cfg Config, active, filtered string) string {
	owner := routeros.OwnerName(cfg.RouterID)
	inv, err := runtime.api.Inventory(ctx)
	if err != nil {
		return "rs-rib-nonfib-table"
	}
	tableID := ""
	for _, table := range inv.Tables {
		if table["name"] != owner {
			continue
		}
		if _, fib := table["fib"]; fib || tableID != "" || table[".id"] == "" {
			return "rs-rib-nonfib-table"
		}
		tableID = table[".id"]
	}
	if tableID == "" {
		return "rs-rib-nonfib-table"
	}
	fib, err := runtime.api.ReadProperty(ctx, api.RoutingTable, tableID, "fib")
	// 7.24.6 represents the switch being unset by a completed getter with no
	// value. A printed empty flag is enabled and was rejected above.
	if err != nil || fib.Known || fib.Value != "" {
		return "rs-rib-nonfib-table"
	}

	args, err := terminal.SSHArgs(terminal.SSHOptions{
		Host: cfg.SSH.Host, Port: cfg.SSH.Port, User: cfg.SSH.Username,
		Identity: cfg.SSH.IdentityFile, KnownHosts: cfg.SSH.KnownHostsFile,
	})
	if err != nil {
		return "rs-rib-ssh"
	}
	var script strings.Builder
	for _, value := range []string{active, filtered} {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.String() != value {
			return "rs-rib-response"
		}
		fmt.Fprintf(&script, `:foreach r in=[/routing/route/print as-value where dst-address=%s] do={ :put ("MDLAB_ROUTE=" . ($r->"dst-address") . "|" . ($r->"routing-table")) }; :put "MDLAB_PREFIX_DONE=%s"; `, prefix, prefix)
	}
	// The fixture uses the production SSH restrictions, except that the
	// isolated probe requests a single command without a terminal.
	args = append(args, script.String())
	for i, arg := range args {
		switch arg {
		case "-tt":
			args[i] = "-T"
		case "RequestTTY=force":
			args[i] = "RequestTTY=no"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never"}
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return "rs-rib-ssh"
	}
	const limit = 4096
	raw, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if len(raw) > limit || readErr != nil {
		cancel()
	}
	if err := cmd.Wait(); err != nil || len(raw) > limit || readErr != nil {
		return "rs-rib-ssh"
	}
	return nativeRSRIBProof(string(raw), owner, active, filtered)
}

func nativeRSRIBProof(reply, owner, active, filtered string) string {
	const marker = "MDLAB_ROUTE="
	prefixes := []string{active, filtered}
	allowedSeen := false
	for _, row := range strings.FieldsFunc(reply, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if len(prefixes) == 0 {
			return "rs-rib-response"
		}
		if row == "MDLAB_PREFIX_DONE="+prefixes[0] {
			prefixes = prefixes[1:]
			continue
		}
		fields := strings.Split(strings.TrimPrefix(row, marker), "|")
		if !strings.HasPrefix(row, marker) || len(fields) != 2 || fields[0] != prefixes[0] {
			return "rs-rib-response"
		}
		if fields[1] != owner {
			return "rs-rib-foreign-table"
		}
		allowedSeen = allowedSeen || fields[0] == active
	}
	if len(prefixes) > 0 {
		return "rs-rib-response"
	}
	if !allowedSeen {
		return "rs-rib-route-missing"
	}
	return ""
}

func TestNativeRSRIBProof(t *testing.T) {
	const active = "2001:db8:100::/48"
	const filtered = "2001:db8:200::/48"
	const owner = "md_test"
	const row = "MDLAB_ROUTE=" + active + "|" + owner + "\r"
	const doneActive = "MDLAB_PREFIX_DONE=" + active + "\r\n"
	const doneFiltered = "MDLAB_PREFIX_DONE=" + filtered + "\n"
	for _, tt := range []struct{ name, reply, code string }{
		{"complete", row + doneActive + doneFiltered, ""},
		{"filtered in owner", row + doneActive + "MDLAB_ROUTE=" + filtered + "|" + owner + "\n" + doneFiltered, ""},
		{"leaked to main", row + "MDLAB_ROUTE=" + active + "|main\n" + doneActive + doneFiltered, "rs-rib-foreign-table"},
		{"filtered in foreign RIB", row + doneActive + "MDLAB_ROUTE=" + filtered + "|foreign\n" + doneFiltered, "rs-rib-foreign-table"},
		{"no allowed route", doneActive + doneFiltered, "rs-rib-route-missing"},
		{"incomplete", row + doneActive, "rs-rib-response"},
		{"unexpected route", row + row + doneActive + row + doneFiltered, "rs-rib-response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := nativeRSRIBProof(tt.reply, owner, active, filtered); got != tt.code {
				t.Fatalf("result %q, want %q", got, tt.code)
			}
		})
	}
}
