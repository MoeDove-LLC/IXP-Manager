package terminal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixtureSecret = "NEVER_RETURN_THIS_FIXTURE_PASSWORD"

type fixture struct {
	ownedMode               string
	ownedEntered            chan struct{}
	ownedRelease            <-chan struct{}
	peer                    net.Conn
	mu                      sync.Mutex
	commands                []string
	controls                []byte
	safe                    bool
	ownerMenu               bool
	ownerMismatch           bool
	busyOwner               bool
	echoOnly                bool
	deviceQuery             bool
	takenMessage            string
	releaseMessage          string
	releaseSafePrompt       bool
	releaseEOF              bool
	releaseNoPrompt         bool
	releaseOwnerOther       bool
	releaseOwnerUnavailable bool
	released                bool
	hideSafePrompt          bool
	resetOnCancel           bool
	bottomPrompt            bool
	pages                   int
	reject                  bool
	identifier              string
	historyCount            string
	done                    chan struct{}
}

var markerCommand = regexp.MustCompile(`:put \("MD_(BEGIN|END)_" \. "([a-f0-9]{32})"\)`)

func limits() Limits {
	l := DefaultLimits()
	l.OperationTimeout = time.Second
	l.Lifetime = 10 * time.Second
	return l
}
func newFixture(t *testing.T, change func(*fixture)) (*Client, *fixture) {
	t.Helper()
	local, peer := net.Pipe()
	f := &fixture{peer: peer, ownerMenu: true, takenMessage: "Taking Safe Mode session... Success!", releaseMessage: "Releasing Safe Mode... Success!", identifier: "syntheticCHR7", historyCount: "0", done: make(chan struct{})}
	if change != nil {
		change(f)
	}
	c, err := Open(local, limits())
	if err != nil {
		t.Fatal(err)
	}
	go f.serve()
	t.Cleanup(func() {
		_ = c.Close()
		_ = peer.Close()
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Error("terminal fixture did not stop")
		}
	})
	return c, f
}
func (f *fixture) write(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Deliberately split ANSI controls, marker lines and prompts across reads.
	for len(text) > 0 {
		n := 3
		if len(text) < n {
			n = len(text)
		}
		if _, err := io.WriteString(f.peer, text[:n]); err != nil {
			return false
		}
		text = text[n:]
	}
	return true
}
func (f *fixture) prompt() string {
	if f.safe && !f.hideSafePrompt {
		return "[admin@CHR] <SAFE> "
	}
	return "[admin@CHR] > "
}
func (f *fixture) serve() {
	defer close(f.done)
	defer f.peer.Close()
	reader := bufio.NewReader(f.peer)
	if f.deviceQuery {
		if !f.write("\r\n\r\r\x1bZ") {
			return
		}
		response := make([]byte, len("\x1b[?1;2c"))
		if _, err := io.ReadFull(reader, response); err != nil || string(response) != "\x1b[?1;2c" {
			return
		}
		if !f.write("\x1b[6n") {
			return
		}
		response = make([]byte, len("\x1b[1;1R"))
		if _, err := io.ReadFull(reader, response); err != nil || string(response) != "\x1b[1;1R" {
			return
		}
	}
	initial := "\x1b[32mMikroTik RouterOS\x1b[0m\r\n" + f.prompt()
	if f.bottomPrompt {
		initial = "\r\r\r\x1b[9999B" + f.prompt()
	}
	if !f.write(initial) {
		return
	}
	var line strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case 0x18:
			f.mu.Lock()
			f.controls = append(f.controls, b)
			f.mu.Unlock()
			if f.busyOwner {
				if !f.write("\r\nHijacking Safe Mode from private-owner - unroll/release/don't take it [u/r/d]:") {
					return
				}
				choice, err := reader.ReadByte()
				if err != nil {
					return
				}
				f.mu.Lock()
				f.controls = append(f.controls, choice)
				f.mu.Unlock()
				if choice != 'd' {
					return
				}
				if !f.write("\r\n" + f.prompt()) {
					return
				}
				continue
			}
			f.safe = !f.safe
			if f.safe {
				if !f.write("\r\n" + f.takenMessage + "\r\n" + f.prompt()) {
					return
				}
			} else {
				f.released = true
				if f.releaseEOF {
					return
				}
				prompt := f.prompt()
				if f.releaseSafePrompt {
					prompt = "[admin@CHR] <SAFE> "
				}
				if f.releaseNoPrompt {
					prompt = ""
				}
				if !f.write("\r\n" + f.releaseMessage + "\r\n" + prompt) {
					return
				}
			}
		case 0x04:
			f.mu.Lock()
			f.controls = append(f.controls, b)
			f.mu.Unlock()
			if f.resetOnCancel {
				if !f.write("\r\x1b7\x1b8\x1b[1;1H\x1b[H\x1b[J\x1bcMikroTik 7.24.6 (long-term)\r\r\nCHR Login:") {
					return
				}
				// Keep the socket alive: the client must recognize the actual
				// logout/login sequence rather than relying on a fixture EOF.
				_, _ = reader.ReadByte()
			}
			return
		case '\r':
			command := line.String()
			line.Reset()
			f.mu.Lock()
			f.commands = append(f.commands, command)
			f.mu.Unlock()
			if !f.write("\x1b[33m" + command + "\x1b[0m\r\n") {
				return
			}
			markers := markerCommand.FindAllStringSubmatch(command, -1)
			if len(markers) != 2 || markers[0][1] != "BEGIN" || markers[1][1] != "END" || markers[0][2] != markers[1][2] {
				return
			}
			if f.echoOnly {
				if !f.write(f.prompt()) {
					return
				}
				continue
			}
			ownedCommand := strings.Contains(command, ":do { /routing/")
			if ownedCommand && f.ownedMode == "echo-only" {
				if !f.write(f.prompt()) {
					return
				}
				continue
			}
			if strings.Contains(command, "/safe-mode/print") && (!f.ownerMenu || f.released && f.releaseOwnerUnavailable) {
				if !f.write("bad command name safe-mode (line 1 column 80)\r\n" + f.prompt()) {
					return
				}
				continue
			}
			if !f.write("MD_BEGIN_" + markers[0][2] + "\r\n") {
				return
			}
			if ownedCommand {
				if f.ownedEntered != nil {
					close(f.ownedEntered)
					f.ownedEntered = nil
				}
				if f.ownedRelease != nil {
					<-f.ownedRelease
				}
				mode := f.ownedMode
				switch mode {
				case "raw-error":
					if !f.write("failure: " + fixtureSecret + "\r\n") {
						return
					}
				case "fail":
					if !f.write("MD_FAIL_" + markers[0][2] + "\r\n") {
						return
					}
				case "end-only":
				default:
					if !f.write("MD_OK_" + markers[0][2] + "\r\n") {
						return
					}
				}
				if mode == "double-ok" {
					if !f.write("MD_OK_" + markers[0][2] + "\r\n") {
						return
					}
				}
				if mode == "normal-prompt" {
					f.hideSafePrompt = true
				}
				if mode == "post-owner-unknown" {
					f.ownerMenu = false
				}
				if !f.write("MD_END_" + markers[1][2] + "\r\n" + f.prompt()) {
					return
				}
				continue
			}
			if f.reject {
				if !f.write("failure: " + fixtureSecret + "\r\n") {
					return
				}
			} else {
				switch {
				case strings.Contains(command, "/system/resource/get version"):
					if !f.write("7.24.6 (long-term)\r\n") {
						return
					}
				case strings.Contains(command, "/system/license/get system-id") || strings.Contains(command, "/system/routerboard/get serial-number") || strings.Contains(command, "/system/license/get software-id"):
					if !f.write(f.identifier + "\r\n") {
						return
					}
				case strings.Contains(command, "/safe-mode/print"):
					data := "enabled: no\r\ncurrent: no\r\nowner: \r\nuser: \r\n"
					if f.safe {
						data = "enabled: yes\r\ncurrent: yes\r\nowner: ssh-private-session\r\nuser: private-user\r\n"
					}
					if f.busyOwner || f.ownerMismatch || f.released && f.releaseOwnerOther {
						data = "enabled: yes\r\ncurrent: no\r\nowner: other-private-session\r\nuser: other-private-user\r\n"
					}
					if !f.write(data) {
						return
					}
				case strings.Contains(command, "/system/history/print count-only where floating-undo"):
					if !f.write(f.historyCount + "\r\n") {
						return
					}
				case strings.Contains(command, "/system/history/") || strings.Contains(command, "/user/active/"):
					if !f.write(fixtureSecret + "\r\n") {
						return
					}
					for page := 0; page < f.pages; page++ {
						if !f.write("-- [Q quit|D dump|down]") {
							return
						}
						space, err := reader.ReadByte()
						if err != nil || space != ' ' {
							return
						}
						if !f.write("\r\x1b[2Knext page\r\n") {
							return
						}
					}
				}
			}
			if !f.write("MD_END_" + markers[1][2] + "\r\n" + f.prompt()) {
				return
			}
		default:
			line.WriteByte(b)
		}
	}
}
func codeOf(err error) Code {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

func TestPersistentSessionMarkersSafeModeAndUnverifiedCancel(t *testing.T) {
	c, f := newFixture(t, nil)
	r, err := c.Query(context.Background(), Version)
	if err != nil || r.Version != "7.24.6 (long-term)" || r.Evidence.Mode != Ready {
		t.Fatalf("version probe: %+v %v", r, err)
	}
	e, err := c.EnterSafeMode(context.Background())
	if err != nil || e.Mode != SafeOwned || e.Owner != OwnerThisSession || e.NativeValidated {
		t.Fatalf("enter: %+v %v", e, err)
	}
	if _, err = c.EnterSafeMode(context.Background()); codeOf(err) != StateUnknown {
		t.Fatal("second enter could toggle/commit safe mode", err)
	}
	e, err = c.OwnerEvidence(context.Background())
	if err != nil || !e.OwnerQueryAvailable || e.Owner != OwnerThisSession || len(e.OwnerFingerprint) != 64 {
		t.Fatalf("owner: %+v %v", e, err)
	}
	encoded, _ := json.Marshal(e)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("raw owner identity disclosed")
	}
	result, err := c.CancelSafeMode(context.Background())
	if err != nil || !result.Requested || !result.SessionEnded || result.RollbackVerified || result.Evidence.RollbackVerified || result.Evidence.Mode != CancelPending {
		t.Fatalf("cancel is not rollback verification: %+v %v", result, err)
	}
	f.mu.Lock()
	controls := append([]byte(nil), f.controls...)
	commands := append([]string(nil), f.commands...)
	f.mu.Unlock()
	if string(controls) != string([]byte{0x18, 0x04}) {
		t.Fatal("unexpected control bytes", controls)
	}
	for _, command := range commands {
		if strings.Contains(command, "/quit") {
			t.Fatal("quit used as rollback")
		}
	}
}
func TestNativeDECIDAndCursorQueriesBeforeFirstCommand(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.deviceQuery = true })
	e, err := c.Synchronize(context.Background())
	if err != nil || e.Mode != Ready {
		t.Fatal("VT100 identification handshake failed", e, err)
	}
}

func TestNative7246CtrlDResetReturnsToLoginWithoutRollbackClaim(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.resetOnCancel = true })
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := c.CancelSafeMode(context.Background())
	if err != nil || !out.Requested || !out.SessionEnded || out.RollbackVerified || out.Evidence.RollbackVerified {
		t.Fatal("native terminal logout sequence was not recognized safely", err)
	}
}

func TestNative7246BottomPromptSynchronization(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.bottomPrompt = true })
	if evidence, err := c.Synchronize(context.Background()); err != nil || evidence.Mode != Ready {
		t.Fatal("native fresh bottom prompt did not synchronize", err)
	}
}

func TestSafeModeTakenRequiresExactAcknowledgementAndSafePrompt(t *testing.T) {
	for _, line := range []string{"[Safe Mode taken]", "Taking Safe Mode session... Success!"} {
		c, _ := newFixture(t, func(f *fixture) { f.takenMessage = line })
		if e, err := c.EnterSafeMode(context.Background()); err != nil || e.Mode != SafeOwned {
			t.Fatal("documented/native acknowledgement rejected", err)
		}
		_ = c.Close()
	}
	for _, line := range []string{"Success!", "Taking Safe Mode session... Success! extra"} {
		c, _ := newFixture(t, func(f *fixture) { f.takenMessage = line })
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		_, err := c.EnterSafeMode(ctx)
		cancel()
		if codeOf(err) != Deadline || !c.Snapshot().Closed {
			t.Fatal("generic success text acquired safe ownership", err)
		}
	}
	c, _ := newFixture(t, func(f *fixture) { f.hideSafePrompt = true })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := c.EnterSafeMode(ctx); codeOf(err) != Deadline {
		t.Fatal("taken acknowledgement without safe prompt became ownership", err)
	}
}
func TestForeignSafeModeIsDeclinedWithoutTakeover(t *testing.T) {
	c, f := newFixture(t, func(f *fixture) { f.busyOwner = true })
	e, err := c.EnterSafeMode(context.Background())
	if codeOf(err) != OwnerConflict || e.Owner != OwnerOther || e.Mode != Ready || e.Closed {
		t.Fatal("foreign owner handling", e, err)
	}
	f.mu.Lock()
	controls := append([]byte(nil), f.controls...)
	f.mu.Unlock()
	if string(controls) != string([]byte{0x18, 'd'}) {
		t.Fatal("hijack response changed another user's state", controls)
	}
	e, err = c.OwnerEvidence(context.Background())
	if err != nil || e.Owner != OwnerOther {
		t.Fatal(e, err)
	}
}
func TestUnsupportedOwnerMenuStaysUnknownAndDoesNotInventAvailability(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.ownerMenu = false })
	e, err := c.OwnerEvidence(context.Background())
	if codeOf(err) != RemoteRejected || e.OwnerQueryAvailable || e.Owner != OwnerUnknown || e.Closed {
		t.Fatal("unsupported menu inferred an owner", e, err)
	}
	if r, err := c.Query(context.Background(), Version); err != nil || r.Version == "" {
		t.Fatal("read rejection poisoned an otherwise synchronized session", err)
	}
}
func TestCommandEchoNeverCompletesAFrame(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.echoOnly = true })
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := c.Query(ctx, Version); codeOf(err) != Deadline {
		t.Fatal("echo satisfied success markers", err)
	}
	if !c.Snapshot().Closed {
		t.Fatal("ambiguous command timeout left channel reusable")
	}
}
func TestPaginationIsBoundedAndRawOutputNeverEscapes(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.pages = 2 })
	r, err := c.Query(context.Background(), History)
	if err != nil || r.ObservedLines < 2 {
		t.Fatal("bounded pager continuation failed", r, err)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), fixtureSecret) {
		t.Fatal("history output leaked")
	}
	c2, _ := newFixture(t, func(f *fixture) { f.reject = true })
	r, err = c2.Query(context.Background(), History)
	if codeOf(err) != RemoteRejected || strings.Contains(err.Error(), fixtureSecret) {
		t.Fatal("remote error did not redact echo", err)
	}
	encoded, _ = json.Marshal(r)
	if strings.Contains(string(encoded), fixtureSecret) {
		t.Fatal("error result leaked remote payload")
	}
}
func TestLostOwnershipPoisonsPersistentChannel(t *testing.T) {
	c, f := newFixture(t, nil)
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	go f.write("\r\n[Safe mode released by another user]\r\n[admin@CHR] > ")
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("ownership loss was not observed while idle")
	}
	if c.Snapshot().Mode != Lost {
		t.Fatal("ownership loss hidden")
	}
	if _, err := c.Query(context.Background(), Probe); codeOf(err) != OwnershipLost {
		t.Fatal("lost channel reused", err)
	}
}

func TestOwnerQueryContradictingOwnedPromptPoisonsChannel(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.ownerMismatch = true })
	if _, err := c.EnterSafeMode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerEvidence(context.Background()); codeOf(err) != OwnershipLost {
		t.Fatal("owner query contradicted local protection without invalidating channel", err)
	}
	if s := c.Snapshot(); s.Mode != Lost || !s.Closed {
		t.Fatal("contradictory owner evidence remained usable", s)
	}
}
func TestConcurrentQueriesDoNotInterleaveTerminalCommands(t *testing.T) {
	c, _ := newFixture(t, nil)
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Query(context.Background(), Version)
			if err == nil && r.Version != "7.24.6 (long-term)" {
				err = failure(ProtocolUnknown)
			}
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("concurrent read corrupted frame", err)
		}
	}
}
func TestBoundsAndBlockedReadDeadlineCloseTransport(t *testing.T) {
	for _, which := range []string{"line", "operation", "pages", "timeout"} {
		t.Run(which, func(t *testing.T) {
			local, peer := net.Pipe()
			l := limits()
			l.MaxLineBytes = 128
			l.MaxOperationBytes = 1024
			l.MaxSessionBytes = 4096
			l.MaxPages = 0
			c, err := Open(local, l)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			defer peer.Close()
			if which == "timeout" {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				_, err = c.Synchronize(ctx)
				if codeOf(err) != Deadline {
					t.Fatal(err)
				}
			} else {
				text := strings.Repeat("x", 129)
				if which == "operation" {
					text = strings.Repeat("line\r\n", 900)
				}
				if which == "pages" {
					text = "-- [Q quit|D dump|down]"
				}
				go io.WriteString(peer, text)
				_, err = c.Synchronize(context.Background())
				if codeOf(err) != BoundsExceeded {
					t.Fatal("unbounded terminal output accepted", err)
				}
			}
			if !c.Snapshot().Closed {
				t.Fatal("failed channel remained open")
			}
		})
	}
}
func TestUnknownQueryNeverWrites(t *testing.T) {
	c, f := newFixture(t, nil)
	if _, err := c.Query(context.Background(), Query(255)); codeOf(err) != InvalidInput {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.commands)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("unknown query wrote terminal input")
	}
}

func TestBlockedWriteAndLifetimeAreBounded(t *testing.T) {
	local, peer := net.Pipe()
	c, err := Open(local, limits())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer peer.Close()
	go io.WriteString(peer, "[admin@CHR] > ") // Peer never reads the command.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = c.Query(ctx, Probe); codeOf(err) != Deadline {
		t.Fatal("blocked writer escaped deadline", err)
	}
	if !c.Snapshot().Closed {
		t.Fatal("partial command could be reused")
	}
	local2, peer2 := net.Pipe()
	defer peer2.Close()
	l := limits()
	l.OperationTimeout = 5 * time.Millisecond
	l.Lifetime = 10 * time.Millisecond
	c2, err := Open(local2, l)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	select {
	case <-c2.done:
	case <-time.After(time.Second):
		t.Fatal("idle lifetime did not close the channel")
	}
	if _, err = c2.Query(context.Background(), Probe); codeOf(err) != Deadline {
		t.Fatal("expired session resumed", err)
	}
}

func TestOperationBudgetAlsoCountsAlreadyBufferedWireBytes(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	l := limits()
	l.MaxOperationBytes = 1024
	l.MaxSessionBytes = 4096
	l.MaxLineBytes = 128
	c, err := Open(local, l)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	written := make(chan struct{})
	go func() {
		_, _ = io.WriteString(peer, strings.Repeat("private sampled metadata line\r\n", 64)+"[admin@CHR] > ")
		close(written)
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("fixture did not buffer bounded startup")
	}
	if _, err := c.Synchronize(context.Background()); codeOf(err) != BoundsExceeded {
		t.Fatal("read-ahead bypassed per-operation output cap", err)
	}
}

func TestAlreadyCancelledOperationDoesNotWriteOrTakeSafeMode(t *testing.T) {
	c, f := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.EnterSafeMode(ctx); codeOf(err) != Cancelled {
		t.Fatal("cancelled entry was attempted", err)
	}
	f.mu.Lock()
	count := len(f.commands) + len(f.controls)
	f.mu.Unlock()
	if count != 0 {
		t.Fatal("cancelled call wrote terminal data")
	}
	if _, err := c.Query(context.Background(), Version); err != nil {
		t.Fatal("unstarted cancellation poisoned channel", err)
	}
}
