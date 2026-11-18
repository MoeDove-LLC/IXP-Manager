package routeros

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	rosapi "github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

// The current official Safe Mode documentation allows about nine minutes for
// TCP timeout after a lost connection. This is a conservative initial bound,
// not a measured 7.24.6 SSH guarantee:
// https://manual.mikrotik.com/docs/getting-started/configuration-management/#safe-mode
//
// An explicit local SSH process close is the first boundary case. It does not
// simulate a network black hole, device reboot, concurrent takeover or history
// overflow. The original API session remains open and must prove actual undo.
// This extra opt-in deliberately excludes the long case from ordinary native
// SSH suites; a future harness selector must budget for the ten-minute wait.
func TestNativeCHRSSHDisconnectRollback(t *testing.T) {
	if os.Getenv("MOEDOVE_CHR_FAILURE_TEST") != "1" {
		t.Skip("native_chr_failure_fixture_enabled=false")
	}
	configuration := nativeCHRRequireLab(t)
	if configuration.ImageSHA256 != "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878" {
		t.Fatal("ssh_disconnect_pinned_image=false")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	defer cancel()
	key := nativeCHRFile(t, "/scratch/ssh_identity", 32<<10, true)
	clear(key)
	keyInfo, err := os.Lstat("/scratch/ssh_identity")
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatal("ssh_disconnect_private_key_mode=false")
	}
	hosts := nativeCHRFile(t, "/scratch/known_hosts", 32<<10, false)
	clear(hosts)
	caData := nativeCHRFile(t, configuration.CAFile, 64<<10, false)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("ssh_disconnect_ca_parsed=false")
	}
	clear(caData)
	password := nativeCHRFile(t, configuration.PasswordFile, 1024, true)
	defer clear(password)
	apiClient, err := rosapi.Dial(ctx, rosapi.Config{
		Address: configuration.APIAddress, ServerName: configuration.ServerName,
		Username: "admin", Password: password, RootCAs: pool,
		DialTimeout: 5 * time.Second, CommandTimeout: 5 * time.Second,
		Limits: rosapi.Limits{MaxReplyBytes: 1 << 20, MaxRecords: 1024},
	})
	clear(password)
	if err != nil {
		t.Fatalf("ssh_disconnect_api_authenticated=false api_code=%s", nativeCHRAPICode(err))
	}
	defer apiClient.Close()
	resources, err := apiClient.Read(ctx, rosapi.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatalf("ssh_disconnect_api_version_matches=false api_code=%s", nativeCHRAPICode(err))
	}
	licenses, err := apiClient.Read(ctx, rosapi.SystemLicense)
	if err != nil || len(licenses) != 1 || licenses[0]["system-id"] == "" {
		t.Fatalf("ssh_disconnect_api_identity_available=false api_code=%s", nativeCHRAPICode(err))
	}
	apiIdentifier := sha256.Sum256([]byte(licenses[0]["system-id"]))
	const tableName = "md_native_ssh_disconnect"
	tables, err := apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_disconnect_baseline_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	if named, _ := nativeCHRTablePresence(tables, tableName, ""); named != 0 {
		t.Fatal("ssh_disconnect_table_initially_absent=false")
	}
	mode, err := apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_disconnect_initial_safe_mode_idle=false api_code=%s", nativeCHRAPICode(err))
	}
	limits := terminal.DefaultLimits()
	limits.OperationTimeout = 15 * time.Second
	limits.Lifetime = 10 * time.Minute
	term, err := terminal.DialSSH(ctx, terminal.SSHOptions{
		Host: "127.0.0.1", Port: 12222, User: "admin", Identity: "/scratch/ssh_identity", KnownHosts: "/scratch/known_hosts",
	}, limits)
	if err != nil {
		t.Fatalf("ssh_disconnect_channel_opened=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	defer term.Close()
	if evidence, err := term.Synchronize(ctx); err != nil || evidence.Mode != terminal.Ready {
		t.Fatalf("ssh_disconnect_synchronized=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	version, err := term.Query(ctx, terminal.Version)
	if err != nil || !nativeCHRVersionMatches(version.Version) {
		t.Fatalf("ssh_disconnect_version_matches=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	identity, err := term.Query(ctx, terminal.LicenseSystemID)
	if err != nil || identity.IdentifierFingerprint != hex.EncodeToString(apiIdentifier[:]) {
		t.Fatalf("ssh_disconnect_same_device_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_disconnect_version_verified=true ssh_disconnect_same_device_verified=true")
	if owner, err := term.OwnerEvidence(ctx); err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerNone {
		t.Fatalf("ssh_disconnect_initial_owner_clear=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	if owner, err := term.EnterSafeMode(ctx); err != nil || owner.Mode != terminal.SafeOwned || owner.Owner != terminal.OwnerThisSession {
		t.Fatalf("ssh_disconnect_safe_mode_taken=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	count, err := term.Query(ctx, terminal.FloatingUndoCount)
	if err != nil || !count.HistoryKnown || count.HistoryActions != 0 {
		t.Fatalf("ssh_disconnect_initial_history_empty=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	created, err := apiClient.Apply(ctx, rosapi.Mutation{
		Resource: rosapi.RoutingTable, Action: rosapi.Add,
		Attributes: []rosapi.Attribute{{Name: "name", Value: tableName}, {Name: "disabled", Value: "yes"}},
	})
	if err != nil || !created.Acknowledged || len(created.ID) < 2 || !strings.HasPrefix(created.ID, "*") {
		t.Fatalf("ssh_disconnect_table_add_acknowledged=false api_code=%s", nativeCHRAPICode(err))
	}
	tables, err = apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_disconnect_added_table_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	named, identified := nativeCHRTablePresence(tables, tableName, created.ID)
	matching := false
	for _, row := range tables {
		matching = matching || row["name"] == tableName && row[".id"] == created.ID
	}
	if named != 1 || identified != 1 || !matching {
		t.Fatal("ssh_disconnect_added_table_native_id_readback=false")
	}
	disabled, err := apiClient.ReadProperty(ctx, rosapi.RoutingTable, created.ID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "yes" && disabled.Value != "true" {
		t.Fatalf("ssh_disconnect_disabled_table_verified=false api_code=%s", nativeCHRAPICode(err))
	}
	// Absent fib after a successful getter is the separately verified omission
	// representation only for this pinned 7.24.6 version.
	fib, err := apiClient.ReadProperty(ctx, rosapi.RoutingTable, created.ID, "fib")
	if err != nil || fib.Known && fib.Value != "no" && fib.Value != "false" || !fib.Known && fib.Value != "" {
		t.Fatalf("ssh_disconnect_nonfib_table_verified=false api_code=%s", nativeCHRAPICode(err))
	}
	owner, err := term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned || owner.Closed {
		t.Fatalf("ssh_disconnect_current_owner_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !mode.Enabled || mode.Current {
		t.Fatalf("ssh_disconnect_api_other_owner_verified=false api_code=%s", nativeCHRAPICode(err))
	}
	count, err = term.Query(ctx, terminal.FloatingUndoCount)
	if err != nil || !count.HistoryKnown || count.HistoryActions < 1 || count.HistoryActions > 80 {
		t.Fatalf("ssh_disconnect_pending_history_known=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_disconnect_current_owner_verified=true ssh_disconnect_api_other_owner_verified=true ssh_disconnect_exact_inert_table_readback=true ssh_disconnect_pending_history_known=true")

	// Close the persistent SSH transport only. There is no Ctrl-D, confirming
	// Ctrl-X, /quit, replacement SSH channel or compensating API remove here.
	started := time.Now()
	if err = term.Close(); err != nil {
		t.Fatal("ssh_disconnect_transport_closed=false")
	}
	closed := term.Snapshot()
	if !closed.Closed || closed.RollbackVerified || closed.CommitVerified {
		t.Fatal("ssh_disconnect_unverified_close_evidence=false")
	}
	t.Log("ssh_disconnect_transport_closed=true ssh_disconnect_terminal_claimed_rollback=false")
	rollbackCtx, stop := context.WithDeadline(ctx, started.Add(10*time.Minute))
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	stable := 0
	for {
		tables, err = apiClient.Read(rollbackCtx, rosapi.RoutingTable)
		if err != nil {
			t.Fatalf("ssh_disconnect_original_api_readback=false api_code=%s", nativeCHRAPICode(err))
		}
		named, identified = nativeCHRTablePresence(tables, tableName, created.ID)
		mode, err = apiClient.ReadSafeMode(rollbackCtx)
		if err != nil {
			t.Fatalf("ssh_disconnect_owner_readback=false api_code=%s", nativeCHRAPICode(err))
		}
		if named == 0 && identified == 0 && !mode.Enabled && !mode.Current {
			stable++
			if stable == 2 {
				break
			}
		} else {
			stable = 0
		}
		select {
		case <-rollbackCtx.Done():
			t.Fatal("ssh_disconnect_rollback_readback_verified=false ssh_disconnect_wait_exhausted=true")
		case <-ticker.C:
		}
	}
	wait := time.Since(started).Milliseconds()
	if wait < 0 || wait > 600000 {
		t.Fatal("ssh_disconnect_wait_bound_verified=false")
	}
	t.Logf("ssh_disconnect_rollback_readback_verified=true ssh_disconnect_owner_released=true ssh_disconnect_manual_cleanup=false ssh_disconnect_wait_ms=%d", wait)
}
