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

// This uses only the trusted disposable CHR lab. No environment-provided SSH
// hostname, key path, password, agent or ambient SSH configuration is accepted.
// An SSH EOF is not the proof: the original TLS API connection must read back
// disappearance of the exact table name and native ID after Ctrl-D.
func TestNativeCHRSSHSafeModeRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	configuration := nativeCHRRequireLab(t)
	if configuration.ImageSHA256 != "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878" {
		t.Fatal("ssh_fixture_pinned_image=false")
	}
	key := nativeCHRFile(t, "/scratch/ssh_identity", 32<<10, true)
	clear(key)
	keyInfo, err := os.Lstat("/scratch/ssh_identity")
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatal("ssh_fixture_private_key_mode=false")
	}
	hosts := nativeCHRFile(t, "/scratch/known_hosts", 32<<10, false)
	clear(hosts)
	caData := nativeCHRFile(t, configuration.CAFile, 64<<10, false)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("ssh_fixture_ca_parsed=false")
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
		t.Fatalf("ssh_api_tls_authenticated=false api_code=%s", nativeCHRAPICode(err))
	}
	defer apiClient.Close()
	resources, err := apiClient.Read(ctx, rosapi.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatalf("ssh_api_version_matches=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Log("ssh_api_tls_authenticated=true ssh_api_version=7.24.6")
	licenses, err := apiClient.Read(ctx, rosapi.SystemLicense)
	if err != nil || len(licenses) != 1 || licenses[0]["system-id"] == "" {
		t.Fatalf("ssh_api_device_identity_available=false api_code=%s", nativeCHRAPICode(err))
	}
	apiIdentifier := sha256.Sum256([]byte(licenses[0]["system-id"]))
	const tableName = "md_native_ssh_cancel"
	tables, err := apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_baseline_tables_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	if named, _ := nativeCHRTablePresence(tables, tableName, ""); named != 0 {
		t.Fatal("ssh_fixture_table_initially_absent=false")
	}
	mode, err := apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_fixture_initial_safe_mode_idle=false api_code=%s", nativeCHRAPICode(err))
	}

	limits := terminal.DefaultLimits()
	limits.OperationTimeout = 15 * time.Second
	limits.Lifetime = 120 * time.Second
	term, err := terminal.DialSSH(ctx, terminal.SSHOptions{
		Host: "127.0.0.1", Port: 12222, User: "admin",
		Identity: "/scratch/ssh_identity", KnownHosts: "/scratch/known_hosts",
	}, limits)
	if err != nil {
		t.Fatalf("ssh_channel_opened=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	defer term.Close()
	if evidence, err := term.Synchronize(ctx); err != nil || evidence.Mode != terminal.Ready {
		t.Fatalf("ssh_synchronized=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	version, err := term.Query(ctx, terminal.Version)
	if err != nil || !nativeCHRVersionMatches(version.Version) {
		t.Fatalf("ssh_version_matches=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	identity, err := term.Query(ctx, terminal.LicenseSystemID)
	if err != nil || identity.IdentifierFingerprint != hex.EncodeToString(apiIdentifier[:]) {
		t.Fatalf("ssh_api_same_device_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_version=7.24.6 ssh_api_same_device_verified=true")
	owner, err := term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerNone {
		t.Fatalf("ssh_initial_owner_clear=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	if evidence, err := term.EnterSafeMode(ctx); err != nil || evidence.Mode != terminal.SafeOwned || evidence.Owner != terminal.OwnerThisSession {
		t.Fatalf("ssh_safe_mode_taken=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	owner, err = term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned {
		t.Fatalf("ssh_current_owner_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !mode.Enabled || mode.Current {
		t.Fatalf("ssh_api_observes_other_channel_owner=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Log("ssh_current_owner_verified=true ssh_api_observes_other_channel_owner=true")
	created, err := apiClient.Apply(ctx, rosapi.Mutation{
		Resource: rosapi.RoutingTable, Action: rosapi.Add,
		Attributes: []rosapi.Attribute{{Name: "name", Value: tableName}, {Name: "disabled", Value: "yes"}},
	})
	if err != nil || !created.Acknowledged || len(created.ID) < 2 || !strings.HasPrefix(created.ID, "*") {
		t.Fatalf("ssh_typed_table_add_acknowledged=false api_code=%s", nativeCHRAPICode(err))
	}
	tables, err = apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_added_table_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	named, identified := nativeCHRTablePresence(tables, tableName, created.ID)
	matching := false
	for _, row := range tables {
		matching = matching || row["name"] == tableName && row[".id"] == created.ID
	}
	if named != 1 || identified != 1 || !matching {
		t.Fatal("ssh_added_table_native_id_readback=false")
	}
	disabled, err := apiClient.ReadProperty(ctx, rosapi.RoutingTable, created.ID, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "yes" && disabled.Value != "true" {
		t.Fatalf("ssh_disabled_table_verified=false api_code=%s", nativeCHRAPICode(err))
	}
	// The already tested omission representation is unknown/absent. Never send
	// fib=no, and reject any getter indicating that forwarding was enabled.
	fib, err := apiClient.ReadProperty(ctx, rosapi.RoutingTable, created.ID, "fib")
	if err != nil || fib.Known && fib.Value != "no" && fib.Value != "false" || !fib.Known && fib.Value != "" {
		t.Fatalf("ssh_no_fib_table_verified=false api_code=%s", nativeCHRAPICode(err))
	}
	owner, err = term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned {
		t.Fatalf("ssh_owner_preserved_after_write=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !mode.Enabled || mode.Current {
		t.Fatalf("ssh_safe_mode_preserved_after_write=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Log("ssh_added_table_native_id_readback=true ssh_disabled_table_verified=true ssh_no_fib_table_verified=true")
	cancellation, err := term.CancelSafeMode(ctx)
	if err != nil || !cancellation.Requested || !cancellation.SessionEnded || cancellation.RollbackVerified || cancellation.Evidence.RollbackVerified {
		t.Fatalf("ssh_cancel_session_ended_without_rollback_claim=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_cancel_session_ended=true ssh_terminal_claimed_rollback=false")
	rollbackCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		tables, err = apiClient.Read(rollbackCtx, rosapi.RoutingTable)
		if err != nil {
			t.Fatalf("ssh_rollback_readback_available=false api_code=%s", nativeCHRAPICode(err))
		}
		named, identified = nativeCHRTablePresence(tables, tableName, created.ID)
		if named == 0 && identified == 0 {
			break
		}
		select {
		case <-rollbackCtx.Done():
			t.Fatal("ssh_rollback_readback_verified=false")
		case <-ticker.C:
		}
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_safe_mode_released_after_cancel=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Log("ssh_rollback_readback_verified=true ssh_safe_mode_released_after_cancel=true")
}

// Confirm has a separate proof from cancellation: no explicit removal is
// permitted until the exact table survives release and closing the SSH owner.
// Cleanup below therefore cannot manufacture this retained-state observation.
func TestNativeCHRSSHConfirmRetains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	configuration := nativeCHRRequireLab(t)
	if configuration.ImageSHA256 != "c6abf87622c5bdbd86f7b5730c8e83f86f2335ee2fbe97d008da176ffbf97878" {
		t.Fatal("ssh_confirm_pinned_image=false")
	}
	key := nativeCHRFile(t, "/scratch/ssh_identity", 32<<10, true)
	clear(key)
	keyInfo, err := os.Lstat("/scratch/ssh_identity")
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatal("ssh_confirm_private_key_mode=false")
	}
	hosts := nativeCHRFile(t, "/scratch/known_hosts", 32<<10, false)
	clear(hosts)
	caData := nativeCHRFile(t, configuration.CAFile, 64<<10, false)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("ssh_confirm_ca_parsed=false")
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
		t.Fatalf("ssh_confirm_api_authenticated=false api_code=%s", nativeCHRAPICode(err))
	}
	defer apiClient.Close()
	resources, err := apiClient.Read(ctx, rosapi.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatalf("ssh_confirm_api_version_matches=false api_code=%s", nativeCHRAPICode(err))
	}
	licenses, err := apiClient.Read(ctx, rosapi.SystemLicense)
	if err != nil || len(licenses) != 1 || licenses[0]["system-id"] == "" {
		t.Fatalf("ssh_confirm_api_identity_available=false api_code=%s", nativeCHRAPICode(err))
	}
	apiIdentifier := sha256.Sum256([]byte(licenses[0]["system-id"]))
	const tableName = "md_native_ssh_confirm"
	tables, err := apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_confirm_baseline_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	if named, _ := nativeCHRTablePresence(tables, tableName, ""); named != 0 {
		t.Fatal("ssh_confirm_table_initially_absent=false")
	}
	mode, err := apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_confirm_initial_safe_mode_idle=false api_code=%s", nativeCHRAPICode(err))
	}
	limits := terminal.DefaultLimits()
	limits.OperationTimeout = 15 * time.Second
	limits.Lifetime = 120 * time.Second
	term, err := terminal.DialSSH(ctx, terminal.SSHOptions{
		Host: "127.0.0.1", Port: 12222, User: "admin", Identity: "/scratch/ssh_identity", KnownHosts: "/scratch/known_hosts",
	}, limits)
	if err != nil {
		t.Fatalf("ssh_confirm_channel_opened=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	defer term.Close()
	if evidence, err := term.Synchronize(ctx); err != nil || evidence.Mode != terminal.Ready {
		t.Fatalf("ssh_confirm_synchronized=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	version, err := term.Query(ctx, terminal.Version)
	if err != nil || !nativeCHRVersionMatches(version.Version) {
		t.Fatalf("ssh_confirm_version_matches=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	identity, err := term.Query(ctx, terminal.LicenseSystemID)
	if err != nil || identity.IdentifierFingerprint != hex.EncodeToString(apiIdentifier[:]) {
		t.Fatalf("ssh_confirm_same_device_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_confirm_version=7.24.6 ssh_confirm_same_device_verified=true")
	if evidence, err := term.EnterSafeMode(ctx); err != nil || evidence.Mode != terminal.SafeOwned || evidence.Owner != terminal.OwnerThisSession {
		t.Fatalf("ssh_confirm_safe_mode_taken=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	owner, err := term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned {
		t.Fatalf("ssh_confirm_current_owner_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !mode.Enabled || mode.Current {
		t.Fatalf("ssh_confirm_api_observes_other_owner=false api_code=%s", nativeCHRAPICode(err))
	}
	initialCount, err := term.Query(ctx, terminal.FloatingUndoCount)
	if err != nil || !initialCount.HistoryKnown || initialCount.HistoryActions != 0 {
		t.Fatalf("ssh_confirm_initial_undo_empty=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	created, err := apiClient.Apply(ctx, rosapi.Mutation{
		Resource: rosapi.RoutingTable, Action: rosapi.Add,
		Attributes: []rosapi.Attribute{{Name: "name", Value: tableName}, {Name: "disabled", Value: "yes"}},
	})
	if err != nil || !created.Acknowledged || len(created.ID) < 2 || !strings.HasPrefix(created.ID, "*") {
		t.Fatalf("ssh_confirm_table_add_acknowledged=false api_code=%s", nativeCHRAPICode(err))
	}
	nativeCHRSSHRetainedTable(t, ctx, apiClient, tableName, created.ID)
	count, err := term.Query(ctx, terminal.FloatingUndoCount)
	if err != nil || !count.HistoryKnown || count.HistoryActions < 1 || count.HistoryActions > 100 {
		t.Fatalf("ssh_confirm_pending_undo_known=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_confirm_pending_undo_known=true ssh_confirm_table_readback=true")
	confirmation, err := term.ConfirmSafeMode(ctx)
	if err != nil || !confirmation.Requested || !confirmation.Released || confirmation.CommitVerified || confirmation.Evidence.CommitVerified || confirmation.Evidence.RollbackVerified || confirmation.Evidence.Mode != terminal.Released || confirmation.Evidence.Owner != terminal.OwnerNone || !confirmation.Evidence.OwnerQueryAvailable || confirmation.Evidence.Closed {
		t.Fatalf("ssh_confirm_release_observed_without_commit_claim=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_confirm_api_safe_mode_released=false api_code=%s", nativeCHRAPICode(err))
	}
	nativeCHRSSHRetainedTable(t, ctx, apiClient, tableName, created.ID)
	count, err = term.Query(ctx, terminal.FloatingUndoCount)
	if err != nil || !count.HistoryKnown || count.HistoryActions != 0 {
		t.Fatalf("ssh_confirm_released_undo_empty=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("ssh_confirm_release_observed=true ssh_confirm_terminal_claimed_commit=false ssh_confirm_retained_after_release=true")
	if term.Close() != nil {
		t.Fatal("ssh_confirm_channel_closed=false")
	}
	// Only the original authenticated API connection is used for both reads.
	// There is no reconnect, compensating write or remove before this point.
	nativeCHRSSHRetainedTable(t, ctx, apiClient, tableName, created.ID)
	mode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || mode.Enabled || mode.Current {
		t.Fatalf("ssh_confirm_safe_mode_idle_after_close=false api_code=%s", nativeCHRAPICode(err))
	}
	t.Log("ssh_confirm_retained_after_close=true ssh_confirm_independent_readback_verified=true")

	// Explicit cleanup is allowed only after retained-state proof. Recheck the
	// exact native ID and fixture name immediately before removing that object.
	nativeCHRSSHRetainedTable(t, ctx, apiClient, tableName, created.ID)
	removed, err := apiClient.Apply(ctx, rosapi.Mutation{Resource: rosapi.RoutingTable, Action: rosapi.Remove, ID: created.ID})
	if err != nil || !removed.Acknowledged {
		t.Fatalf("ssh_confirm_explicit_cleanup=false api_code=%s", nativeCHRAPICode(err))
	}
	tables, err = apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_confirm_cleanup_readback_available=false api_code=%s", nativeCHRAPICode(err))
	}
	if named, identified := nativeCHRTablePresence(tables, tableName, created.ID); named != 0 || identified != 0 {
		t.Fatal("ssh_confirm_cleanup_readback_verified=false")
	}
	t.Log("ssh_confirm_explicit_cleanup_after_proof=true")
}

func nativeCHRSSHRetainedTable(t *testing.T, ctx context.Context, client *rosapi.Client, name, id string) {
	t.Helper()
	tables, err := client.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatalf("ssh_confirm_retained_table_readable=false api_code=%s", nativeCHRAPICode(err))
	}
	named, identified := nativeCHRTablePresence(tables, name, id)
	matching := false
	for _, row := range tables {
		matching = matching || row["name"] == name && row[".id"] == id
	}
	if named != 1 || identified != 1 || !matching {
		t.Fatal("ssh_confirm_exact_native_identity_retained=false")
	}
	disabled, err := client.ReadProperty(ctx, rosapi.RoutingTable, id, "disabled")
	if err != nil || !disabled.Known || disabled.Value != "yes" && disabled.Value != "true" {
		t.Fatalf("ssh_confirm_disabled_table_retained=false api_code=%s", nativeCHRAPICode(err))
	}
	// This fixture first pins and checks the native 7.24.6 version, for which
	// omitted fib was separately proved absent by a successful getter.
	fib, err := client.ReadProperty(ctx, rosapi.RoutingTable, id, "fib")
	if err != nil || fib.Known && fib.Value != "no" && fib.Value != "false" || !fib.Known && fib.Value != "" {
		t.Fatalf("ssh_confirm_no_fib_retained=false api_code=%s", nativeCHRAPICode(err))
	}
}
