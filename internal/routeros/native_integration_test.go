package routeros

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	rosapi "github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

// This is an opt-in test for the disposable CHR harness, not a remote-device
// test selected by an environment hostname. The trusted harness verifies the
// official image hash before providing this private, fixed-path context.
type nativeCHRContext struct {
	Version       string `json:"version"`
	ImageSHA256   string `json:"imageSha256"`
	APIAddress    string `json:"apiAddress"`
	ServerName    string `json:"serverName"`
	CAFile        string `json:"caFile"`
	PasswordFile  string `json:"passwordFile"`
	ConsoleSocket string `json:"consoleSocket"`
}

func nativeCHRFile(t *testing.T, path string, max int64, private bool) []byte {
	t.Helper()
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() < 1 || before.Size() > max || before.Mode().Perm()&022 != 0 || private && before.Mode().Perm()&077 != 0 {
		t.Fatal("fixture_file_valid=false")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("fixture_file_readable=false")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		t.Fatal("fixture_file_stable=false")
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || len(data) == 0 || int64(len(data)) > max {
		clear(data)
		t.Fatal("fixture_file_bounded=false")
	}
	return data
}

func nativeCHRRequireLab(t *testing.T) nativeCHRContext {
	t.Helper()
	if os.Getenv("MOEDOVE_CHR_NATIVE_TEST") != "1" {
		t.Skip("native_chr_fixture_enabled=false")
	}
	// Check isolation before reading any credential or opening either channel.
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || len(status) > 64<<10 {
		t.Fatal("fixture_capabilities_readable=false")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "CapEff:" && fields[0] != "CapBnd:" {
			continue
		}
		value, e := strconv.ParseUint(fields[1], 16, 64)
		if e != nil || value != 0 || seen[fields[0]] {
			t.Fatal("fixture_capabilities_zero=false")
		}
		seen[fields[0]] = true
	}
	if !seen["CapEff:"] || !seen["CapBnd:"] {
		t.Fatal("fixture_capabilities_zero=false")
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Name != "lo" || interfaces[0].Flags&net.FlagLoopback == 0 || interfaces[0].Flags&net.FlagUp == 0 {
		t.Fatal("fixture_loopback_only=false")
	}
	scratch, err := os.Lstat("/scratch")
	if err != nil || !scratch.IsDir() || scratch.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fixture_scratch_valid=false")
	}
	raw := nativeCHRFile(t, "/scratch/native-context.json", 8192, false)
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var configuration nativeCHRContext
	if decoder.Decode(&configuration) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("fixture_context_valid=false")
	}
	imageHash, err := hex.DecodeString(configuration.ImageSHA256)
	if err != nil || len(imageHash) != 32 || hex.EncodeToString(imageHash) != configuration.ImageSHA256 || bytes.Equal(imageHash, make([]byte, 32)) || configuration.Version != "7.24.6" || configuration.APIAddress != "127.0.0.1:18729" || configuration.ServerName != "chr-fixture.invalid" || configuration.CAFile != "/scratch/api.crt" || configuration.PasswordFile != "/scratch/api-password" || configuration.ConsoleSocket != "/scratch/console.sock" {
		t.Fatal("fixture_fixed_target_valid=false")
	}
	socket, err := os.Lstat(configuration.ConsoleSocket)
	if err != nil || socket.Mode()&os.ModeSocket == 0 || socket.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fixture_console_socket_valid=false")
	}
	return configuration
}

func nativeCHRVersionMatches(value string) bool {
	return value == "7.24.6" || value == "7.24.6 (stable)" || value == "7.24.6 (long-term)"
}

// Diagnostics are a closed vocabulary; never format a remote/transport error.
func nativeCHRTerminalCode(err error) string {
	if err == nil {
		return "none"
	}
	var failed *terminal.Failure
	if !errors.As(err, &failed) {
		return "unclassified"
	}
	switch failed.Code {
	case terminal.InvalidInput, terminal.TransportFailure, terminal.Deadline, terminal.Cancelled,
		terminal.BoundsExceeded, terminal.ProtocolUnknown, terminal.RemoteRejected,
		terminal.AuthenticationRequired, terminal.OwnerConflict, terminal.OwnershipLost,
		terminal.StateUnknown, terminal.SessionClosed:
		return string(failed.Code)
	default:
		return "unclassified"
	}
}

func nativeCHRTablePresence(rows []rosapi.Record, name, id string) (named, identified int) {
	for _, row := range rows {
		if row["name"] == name {
			named++
		}
		if id != "" && row[".id"] == id {
			identified++
		}
	}
	return
}

func TestNativeCHRSafeModeRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	configuration := nativeCHRRequireLab(t)

	caData := nativeCHRFile(t, configuration.CAFile, 64<<10, false)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("fixture_ca_parsed=false")
	}
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
		t.Fatal("api_tls_authenticated=false")
	}
	defer apiClient.Close()
	t.Log("api_tls_authenticated=true")
	resources, err := apiClient.Read(ctx, rosapi.SystemResource)
	if err != nil || len(resources) != 1 || !nativeCHRVersionMatches(resources[0]["version"]) {
		t.Fatal("api_version_matches=false")
	}
	t.Log("api_version=7.24.6")

	const tableName = "md_native_cancel"
	tables, err := apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatal("baseline_tables_readable=false")
	}
	if named, _ := nativeCHRTablePresence(tables, tableName, ""); named != 0 {
		t.Fatal("fixture_table_initially_absent=false")
	}
	apiMode, err := apiClient.ReadSafeMode(ctx)
	if err != nil || apiMode.Enabled || apiMode.Current {
		t.Fatal("fixture_initial_safe_mode_idle=false")
	}

	console, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", configuration.ConsoleSocket)
	if err != nil {
		t.Fatal("console_connected=false")
	}
	limits := terminal.DefaultLimits()
	limits.OperationTimeout = 15 * time.Second
	limits.Lifetime = 120 * time.Second
	term, err := terminal.Open(console, limits)
	if err != nil {
		console.Close()
		t.Fatalf("console_opened=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	defer term.Close()
	if state, err := term.Synchronize(ctx); err != nil || state.Mode != terminal.Ready {
		t.Fatalf("console_synchronized=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	version, err := term.Query(ctx, terminal.Version)
	if err != nil || !nativeCHRVersionMatches(version.Version) {
		t.Fatalf("console_version_matches=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("console_version=7.24.6")
	owner, err := term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerNone {
		t.Fatalf("console_initial_owner_clear=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	if state, err := term.EnterSafeMode(ctx); err != nil || state.Mode != terminal.SafeOwned || state.Owner != terminal.OwnerThisSession {
		t.Fatalf("console_safe_mode_taken=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	owner, err = term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned {
		t.Fatalf("console_current_owner_verified=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	apiMode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !apiMode.Enabled || apiMode.Current {
		t.Fatal("api_observes_other_channel_owner=false")
	}
	t.Log("console_current_owner_verified=true api_observes_other_channel_owner=true")

	// Deliberately omit fib: CLI fib=no was rejected by the native target.
	// This tests API add semantics and readback; it never guesses a default.
	created, err := apiClient.Apply(ctx, rosapi.Mutation{Resource: rosapi.RoutingTable, Action: rosapi.Add, Attributes: []rosapi.Attribute{{Name: "name", Value: tableName}}})
	if err != nil || !created.Acknowledged || len(created.ID) < 2 || !strings.HasPrefix(created.ID, "*") {
		t.Fatal("typed_table_add_acknowledged=false")
	}
	tables, err = apiClient.Read(ctx, rosapi.RoutingTable)
	if err != nil {
		t.Fatal("added_table_readback_available=false")
	}
	named, identified := nativeCHRTablePresence(tables, tableName, created.ID)
	matching := false
	for _, row := range tables {
		matching = matching || row["name"] == tableName && row[".id"] == created.ID
	}
	if named != 1 || identified != 1 || !matching {
		t.Fatal("added_table_native_id_readback=false")
	}
	t.Log("typed_table_add_acknowledged=true added_table_native_id_readback=true")
	apiMode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || !apiMode.Enabled || apiMode.Current {
		t.Fatal("safe_mode_preserved_after_api_write=false")
	}
	owner, err = term.OwnerEvidence(ctx)
	if err != nil || !owner.OwnerQueryAvailable || owner.Owner != terminal.OwnerThisSession || owner.Mode != terminal.SafeOwned {
		t.Fatalf("console_owner_preserved_after_api_write=false terminal_code=%s", nativeCHRTerminalCode(err))
	}

	cancellation, err := term.CancelSafeMode(ctx)
	if err != nil || !cancellation.Requested || !cancellation.SessionEnded || cancellation.RollbackVerified || cancellation.Evidence.RollbackVerified {
		t.Fatalf("console_cancel_session_ended_without_rollback_claim=false terminal_code=%s", nativeCHRTerminalCode(err))
	}
	t.Log("console_cancel_session_ended=true terminal_claimed_rollback=false")

	// Keep using the original verified API connection. No reconnect and no
	// explicit remove/cleanup may manufacture the rollback proof.
	rollbackCtx, stopPolling := context.WithTimeout(ctx, 10*time.Second)
	defer stopPolling()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		tables, err = apiClient.Read(rollbackCtx, rosapi.RoutingTable)
		if err != nil {
			t.Fatal("rollback_readback_available=false")
		}
		named, identified = nativeCHRTablePresence(tables, tableName, created.ID)
		if named == 0 && identified == 0 {
			break
		}
		select {
		case <-rollbackCtx.Done():
			t.Fatal("rollback_readback_verified=false")
		case <-ticker.C:
		}
	}
	apiMode, err = apiClient.ReadSafeMode(ctx)
	if err != nil || apiMode.Enabled || apiMode.Current {
		t.Fatal("safe_mode_released_after_cancel=false")
	}
	t.Log("rollback_readback_verified=true safe_mode_released_after_cancel=true")
}
