package terminal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	session                  Session
	limits                   Limits
	operation                chan struct{}
	events                   chan event
	done                     chan struct{}
	pumpDone                 chan struct{}
	once                     sync.Once
	mu                       sync.Mutex
	evidence                 Evidence
	owned, promptSafe, ended bool
	initialized              bool  // guarded by operation, not supplied by a caller
	consumed                 int64 // last decoded wire position consumed by an operation
	reason                   error
	timer                    *time.Timer
	bytes                    atomic.Int64
}

func Open(session Session, limits Limits) (*Client, error) {
	l, err := checkedLimits(limits)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, failure(InvalidInput)
	}
	c := &Client{session: session, limits: l, operation: make(chan struct{}, 1), events: make(chan event, 64), done: make(chan struct{}), pumpDone: make(chan struct{}), evidence: Evidence{Mode: Unknown, Owner: OwnerUnknown}}
	c.timer = time.NewTimer(l.Lifetime)
	go func() {
		select {
		case <-c.timer.C:
			c.stop(failure(Deadline))
		case <-c.done:
		}
	}()
	go c.read()
	return c, nil
}
func (c *Client) Snapshot() Evidence { c.mu.Lock(); defer c.mu.Unlock(); return c.evidence }
func (c *Client) stop(reason error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.reason = reason
		c.evidence.Closed = true
		if c.evidence.Mode != Lost && c.evidence.Mode != CancelPending {
			c.evidence.Mode = Unknown
		}
		c.evidence.Owner = OwnerUnknown
		c.owned = false
		c.mu.Unlock()
		if c.timer != nil {
			c.timer.Stop()
		}
		close(c.done)
		_ = c.session.Close()
	})
}

// Close never uses /quit or toggles Ctrl-X. It makes no rollback guarantee.
func (c *Client) Close() error { c.stop(failure(SessionClosed)); return nil }
func (c *Client) closedReason() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason != nil {
		return c.reason
	}
	return failure(SessionClosed)
}
func (c *Client) observe(e event) bool {
	c.mu.Lock()
	lost := false
	switch e.kind {
	case lostEvent:
		c.evidence.Mode = Lost
		c.owned = false
		lost = true
	case promptEvent:
		c.promptSafe = e.safe
		c.evidence.ObservedAt = time.Now().UTC()
		if c.owned && !e.safe && c.evidence.Mode != CancelPending && c.evidence.Mode != ConfirmPending {
			c.evidence.Mode = Lost
			c.owned = false
			lost = true
		}
		if c.evidence.Mode != Lost && c.evidence.Mode != CancelPending && c.evidence.Mode != ConfirmPending {
			if c.owned {
				c.evidence.Mode = SafeOwned
			} else if e.safe {
				c.evidence.Mode = SafeObserved
			} else {
				c.evidence.Mode = Ready
			}
		}
	case hijackEvent:
		c.evidence.Owner = OwnerOther
	case loginEvent:
		if c.evidence.Mode == CancelPending {
			c.ended = true
		}
	}
	c.mu.Unlock()
	if lost {
		c.stop(failure(OwnershipLost))
		return false
	}
	return true
}
func (c *Client) read() {
	defer close(c.pumpDone)
	d := newDecoder(c.limits.MaxLineBytes)
	buffer := make([]byte, 1024)
	defer clear(buffer)
	for {
		n, err := c.session.Read(buffer)
		if n > 0 {
			if c.bytes.Add(int64(n)) > int64(c.limits.MaxSessionBytes) {
				c.stop(failure(BoundsExceeded))
				return
			}
			events, e := d.feed(buffer[:n])
			clear(buffer[:n])
			if e != nil {
				c.stop(e)
				return
			}
			for _, event := range events {
				if !c.observe(event) {
					return
				}
				select {
				case c.events <- event:
				case <-c.done:
					return
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				c.mu.Lock()
				c.ended = true
				c.mu.Unlock()
			}
			c.stop(failure(TransportFailure))
			return
		}
		if n == 0 {
			c.stop(failure(TransportFailure))
			return
		}
	}
}
func (c *Client) begin(parent context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(parent, c.limits.OperationTimeout)
	if ctx.Err() != nil {
		err := contextFailure(ctx)
		cancel()
		return nil, nil, err
	}
	select {
	case <-c.done:
		cancel()
		return nil, nil, c.closedReason()
	default:
	}
	select {
	case c.operation <- struct{}{}:
		if ctx.Err() != nil {
			err := contextFailure(ctx)
			cancel()
			<-c.operation
			return nil, nil, err
		}
		select {
		case <-c.done:
			cancel()
			<-c.operation
			return nil, nil, c.closedReason()
		default:
		}
		return ctx, func() { cancel(); <-c.operation }, nil
	case <-ctx.Done():
		cancel()
		return nil, nil, contextFailure(ctx)
	case <-c.done:
		cancel()
		return nil, nil, c.closedReason()
	}
}
func (c *Client) write(ctx context.Context, data []byte) error {
	if ctx.Err() != nil {
		err := contextFailure(ctx)
		c.stop(err)
		return err
	}
	result := make(chan error, 1)
	ownedData := append([]byte(nil), data...)
	go func() {
		defer clear(ownedData)
		written := 0
		for written < len(ownedData) {
			n, err := c.session.Write(ownedData[written:])
			if err != nil || n <= 0 || n > len(ownedData)-written {
				result <- failure(TransportFailure)
				return
			}
			written += n
		}
		result <- nil
	}()
	select {
	case err := <-result:
		if err != nil {
			c.stop(err)
		}
		return err
	case <-ctx.Done():
		err := contextFailure(ctx)
		c.stop(err)
		return err
	case <-c.done:
		// EOF may race a successful Ctrl-D write. Wait for the bounded write
		// result rather than turning a completed control write into a lie.
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return c.closedReason()
		}
	}
}

type operationBudget struct {
	start                   int64
	lines, pages, responses int
}

func (c *Client) next(ctx context.Context, b *operationBudget) (event, error) {
	for {
		if ctx.Err() != nil {
			err := contextFailure(ctx)
			c.stop(err)
			return event{}, err
		}
		if c.bytes.Load()-b.start > int64(c.limits.MaxOperationBytes) {
			err := failure(BoundsExceeded)
			c.stop(err)
			return event{}, err
		}
		var e event
		select {
		case e = <-c.events:
		case <-ctx.Done():
			err := contextFailure(ctx)
			c.stop(err)
			return event{}, err
		case <-c.done:
			return event{}, c.closedReason()
		}
		if e.throughBytes > c.consumed {
			c.consumed = e.throughBytes
		}
		if e.throughBytes-b.start > int64(c.limits.MaxOperationBytes) {
			err := failure(BoundsExceeded)
			c.stop(err)
			return event{}, err
		}
		b.lines++
		if b.lines > c.limits.MaxLines {
			err := failure(BoundsExceeded)
			c.stop(err)
			return event{}, err
		}
		switch e.kind {
		case cursorEvent, deviceEvent:
			b.responses++
			if b.responses > 16 {
				c.stop(failure(BoundsExceeded))
				return event{}, failure(BoundsExceeded)
			}
			response := "\x1b[1;1R"
			if e.kind == deviceEvent {
				response = "\x1b[?1;2c"
			}
			if err := c.write(ctx, []byte(response)); err != nil {
				return event{}, err
			}
			continue
		case pageEvent:
			b.pages++
			if b.pages > c.limits.MaxPages {
				c.stop(failure(BoundsExceeded))
				return event{}, failure(BoundsExceeded)
			}
			if err := c.write(ctx, []byte(" ")); err != nil {
				return event{}, err
			}
			continue
		case lostEvent:
			return event{}, failure(OwnershipLost)
		case loginEvent:
			c.mu.Lock()
			cancel := c.evidence.Mode == CancelPending
			c.mu.Unlock()
			if !cancel {
				c.stop(failure(AuthenticationRequired))
				return event{}, failure(AuthenticationRequired)
			}
		}
		return e, nil
	}
}

func fixedCommand(q Query) (string, bool) {
	switch q {
	case Probe:
		return "", true
	case Version:
		return ":put [/system/resource/get version]", true
	case ActiveUsers:
		return "/user/active/print detail without-paging", true
	case History:
		return "/system/history/print without-paging", true
	case SafeModeOwner:
		return "/safe-mode/print", true
	case LicenseSystemID:
		return ":put [/system/license/get system-id]", true
	case RouterBoardSerial:
		return ":put [/system/routerboard/get serial-number]", true
	case LicenseSoftwareID:
		return ":put [/system/license/get software-id]", true
	case FloatingUndoCount:
		return "/system/history/print count-only where floating-undo", true
	default:
		return "", false
	}
}
func markers() (string, string, string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", failure(TransportFailure)
	}
	nonce := hex.EncodeToString(b)
	clear(b)
	return nonce, "MD_BEGIN_" + nonce, "MD_END_" + nonce, nil
}

// Splitting the literal prevents command echo (including terminal wrapping)
// from containing a standalone complete marker. No caller input is interpolated.
func putMarker(kind, nonce string) string { return ":put (\"MD_" + kind + "_\" . \"" + nonce + "\")\r" }

var versionPattern = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}(?:\.[0-9]{1,3})?(?:(?:rc|beta)[0-9]{1,4})?(?: \((?:stable|testing|development|long-term|longterm)\))?$`)
var deviceIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

// CHR System IDs are opaque data: native 7.24.6 has returned '/' in an
// eleven-character ID. Accept the bounded unpadded Base64 alphabet, including
// a leading '+' or '/', without decoding, normalizing or interpolating it.
var licenseSystemIDPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{1,128}$`)
var historyCountPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]?|100)$`)

func rejected(line string) bool {
	s := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{"failure:", "syntax error", "bad command name", "expected end of command", "expected command name", "not enough permissions", "input does not match", "no such item"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func safeModeTaken(line string) bool {
	line = strings.TrimSpace(line)
	return strings.EqualFold(line, "[Safe Mode taken]") || line == "Taking Safe Mode session... Success!"
}

func (c *Client) queryLocked(ctx context.Context, q Query, b *operationBudget) (Result, error) {
	result := Result{Query: q}
	command, ok := fixedCommand(q)
	if !ok {
		return result, failure(InvalidInput)
	}
	if !c.initialized {
		for {
			e, err := c.next(ctx, b)
			if err != nil {
				return result, err
			}
			if e.kind == hijackEvent {
				c.stop(failure(OwnerConflict))
				return result, failure(OwnerConflict)
			}
			if e.kind == promptEvent {
				c.initialized = true
				break
			}
		}
	}
	nonce, begin, end, err := markers()
	if err != nil {
		return result, err
	}
	wire := strings.TrimSuffix(putMarker("BEGIN", nonce), "\r")
	if command != "" {
		wire += "; " + command
	}
	wire += "; " + putMarker("END", nonce)
	if err = c.write(ctx, []byte(wire)); err != nil {
		return result, err
	}
	started, ended, remoteFailure := false, false, false
	ownerFields := map[string]string{}
	version := ""
	identifier := ""
	identifierQuery := q == LicenseSystemID || q == RouterBoardSerial || q == LicenseSoftwareID
	identifierInvalid := false
	historyText := ""
	historyInvalid := false
	for {
		e, eErr := c.next(ctx, b)
		if eErr != nil {
			return result, eErr
		}
		line := strings.TrimSpace(e.text)
		if e.kind == lineEvent && rejected(line) {
			remoteFailure = true
			continue
		}
		if e.kind == hijackEvent {
			c.stop(failure(OwnerConflict))
			return result, failure(OwnerConflict)
		}
		if e.kind == lineEvent && line == begin {
			if started {
				c.stop(failure(ProtocolUnknown))
				return result, failure(ProtocolUnknown)
			}
			started = true
			continue
		}
		if e.kind == lineEvent && line == end && started {
			ended = true
			continue
		}
		if (ended || remoteFailure) && e.kind == promptEvent {
			if q == SafeModeOwner {
				if c.applyOwner(ownerFields, !remoteFailure) {
					c.stop(failure(OwnershipLost))
					result.Evidence = c.Snapshot()
					return result, failure(OwnershipLost)
				}
			}
			result.Evidence = c.Snapshot()
			result.Version = version
			if result.Evidence.Closed {
				return result, c.closedReason()
			}
			if remoteFailure {
				return result, failure(RemoteRejected)
			}
			if q == Version && version == "" {
				c.stop(failure(ProtocolUnknown))
				return result, failure(ProtocolUnknown)
			}
			if identifierQuery {
				if identifierInvalid || identifier == "" || result.ObservedLines != 1 {
					c.stop(failure(ProtocolUnknown))
					return result, failure(ProtocolUnknown)
				}
				result.IdentifierFingerprint = fingerprint(identifier)
			}
			if q == FloatingUndoCount {
				if historyInvalid || historyText == "" || result.ObservedLines != 1 {
					c.stop(failure(ProtocolUnknown))
					return result, failure(ProtocolUnknown)
				}
				result.HistoryActions, _ = strconv.Atoi(historyText)
				result.HistoryKnown = true
			}
			return result, nil
		}
		if !started || ended || e.kind != lineEvent || line == "" {
			continue
		}
		// Count observed lines only, not users or undo actions. Raw echoes and
		// payloads are never returned; especially history may contain secrets.
		result.ObservedLines++
		if q == FloatingUndoCount {
			if historyText != "" || line != e.text || !historyCountPattern.MatchString(line) {
				historyInvalid = true
			} else {
				historyText = line
			}
		}
		if identifierQuery {
			valid := deviceIdentifierPattern.MatchString(line)
			if q == LicenseSystemID {
				valid = line == e.text && licenseSystemIDPattern.MatchString(line)
			}
			if identifier != "" || !valid {
				identifierInvalid = true
			} else {
				identifier = line
			}
		}
		if q == Version && versionPattern.MatchString(line) {
			if version != "" {
				remoteFailure = true
			}
			version = line
		}
		if q == SafeModeOwner {
			key, value, found := strings.Cut(line, ":")
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if found && (key == "enabled" || key == "current" || key == "owner" || key == "user") {
				if len(value) > 256 {
					remoteFailure = true
					continue
				}
				if _, exists := ownerFields[key]; exists {
					remoteFailure = true
					continue
				}
				ownerFields[key] = value
			}
		}
	}
}
func boolField(value string) (bool, bool) {
	switch value {
	case "true", "yes":
		return true, true
	case "false", "no":
		return false, true
	default:
		return false, false
	}
}
func fingerprint(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (c *Client) applyOwner(fields map[string]string, success bool) bool {
	enabled, eOK := boolField(fields["enabled"])
	current, cOK := boolField(fields["current"])
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evidence.OwnerQueryAvailable = success && eOK && cOK && (!current || enabled)
	c.evidence.Owner = OwnerUnknown
	c.evidence.OwnerFingerprint = ""
	c.evidence.UserFingerprint = ""
	if !c.evidence.OwnerQueryAvailable {
		return false
	}
	c.evidence.ObservedAt = time.Now().UTC()
	c.evidence.OwnerFingerprint = fingerprint(fields["owner"])
	c.evidence.UserFingerprint = fingerprint(fields["user"])
	if !enabled {
		c.evidence.Owner = OwnerNone
	} else if current {
		c.evidence.Owner = OwnerThisSession
	} else {
		c.evidence.Owner = OwnerOther
	}
	if c.owned && (!enabled || !current) {
		c.owned = false
		c.evidence.Mode = Lost
		return true
	}
	return false
}

// Query only accepts the closed set of read-only probes. Unsupported /safe-mode
// menus return a safe rejection and leave ownership unknown, not "unowned".
func (c *Client) Query(parent context.Context, q Query) (Result, error) {
	if _, ok := fixedCommand(q); !ok {
		return Result{}, failure(InvalidInput)
	}
	ctx, end, err := c.begin(parent)
	if err != nil {
		return Result{}, err
	}
	defer end()
	b := operationBudget{start: c.consumed}
	return c.queryLocked(ctx, q, &b)
}
func (c *Client) Synchronize(ctx context.Context) (Evidence, error) {
	r, err := c.Query(ctx, Probe)
	return r.Evidence, err
}
func (c *Client) OwnerEvidence(ctx context.Context) (Evidence, error) {
	r, err := c.Query(ctx, SafeModeOwner)
	return r.Evidence, err
}

func (c *Client) EnterSafeMode(parent context.Context) (Evidence, error) {
	ctx, end, err := c.begin(parent)
	if err != nil {
		return c.Snapshot(), err
	}
	defer end()
	b := operationBudget{start: c.consumed}
	if _, err = c.queryLocked(ctx, Probe, &b); err != nil {
		return c.Snapshot(), err
	}
	c.mu.Lock()
	eligible := c.evidence.Mode == Ready && !c.owned && !c.promptSafe && !c.evidence.Closed && c.evidence.Owner != OwnerThisSession
	c.mu.Unlock()
	if !eligible {
		return c.Snapshot(), failure(StateUnknown)
	}
	if err = c.write(ctx, []byte{0x18}); err != nil {
		return c.Snapshot(), err
	}
	taken, declined := false, false
	for {
		e, err := c.next(ctx, &b)
		if err != nil {
			return c.Snapshot(), err
		}
		if e.kind == hijackEvent {
			if declined {
				c.stop(failure(ProtocolUnknown))
				return c.Snapshot(), failure(ProtocolUnknown)
			}
			declined = true
			if err = c.write(ctx, []byte("d")); err != nil {
				return c.Snapshot(), err
			}
			continue
		}
		if safeModeTaken(e.text) {
			taken = true
			continue
		}
		if e.kind != promptEvent {
			continue
		}
		if declined && !e.safe {
			return c.Snapshot(), failure(OwnerConflict)
		}
		if taken && e.safe && !declined {
			c.mu.Lock()
			valid := c.promptSafe && !c.evidence.Closed
			if valid {
				c.owned = true
				c.evidence.Mode = SafeOwned
				c.evidence.Owner = OwnerThisSession
				c.evidence.ObservedAt = time.Now().UTC()
			}
			c.mu.Unlock()
			if valid {
				return c.Snapshot(), nil
			}
			return c.Snapshot(), failure(StateUnknown)
		}
	}
}

// CancelSafeMode sends Ctrl-D only after a fresh marker confirms an empty safe
// prompt on this channel. EOF/login is session-end evidence, never proof that
// the device has finished undoing configuration.
func (c *Client) CancelSafeMode(parent context.Context) (Cancellation, error) {
	out := Cancellation{}
	ctx, end, err := c.begin(parent)
	if err != nil {
		return out, err
	}
	defer end()
	c.mu.Lock()
	owned := c.owned && c.evidence.Mode == SafeOwned
	c.mu.Unlock()
	if !owned {
		return out, failure(StateUnknown)
	}
	b := operationBudget{start: c.consumed}
	if _, err = c.queryLocked(ctx, Probe, &b); err != nil {
		out.Evidence = c.Snapshot()
		return out, err
	}
	c.mu.Lock()
	owned = c.owned && c.promptSafe && !c.evidence.Closed
	if owned {
		c.evidence.Mode = CancelPending
	}
	c.mu.Unlock()
	if !owned {
		out.Evidence = c.Snapshot()
		return out, failure(StateUnknown)
	}
	if err = c.write(ctx, []byte{0x04}); err != nil {
		out.Evidence = c.Snapshot()
		return out, err
	}
	out.Requested = true
	for {
		e, nextErr := c.next(ctx, &b)
		c.mu.Lock()
		ended := c.ended
		c.mu.Unlock()
		if ended || e.kind == loginEvent {
			out.SessionEnded = true
			c.stop(failure(SessionClosed))
			out.Evidence = c.Snapshot()
			return out, nil
		}
		if nextErr != nil {
			out.Evidence = c.Snapshot()
			return out, nextErr
		}
		// A trailing prompt repaint can already be in flight when Ctrl-D is
		// sent. Only EOF or a new login prompt proves the session ended; any
		// continuing authenticated prompt remains inconclusive until timeout.
	}
}

// ConfirmSafeMode releases this channel's Safe Mode with exactly one Ctrl-X.
// The exact 7.24.6 acknowledgement, normal prompt and a fresh no-owner query
// establish only release. The caller must independently read configuration
// back before claiming commit; neither timeout nor EOF can imply success.
func (c *Client) ConfirmSafeMode(parent context.Context) (Confirmation, error) {
	out := Confirmation{}
	ctx, end, err := c.begin(parent)
	if err != nil {
		out.Evidence = c.Snapshot()
		return out, err
	}
	defer end()
	abort := func(err error) (Confirmation, error) {
		c.stop(err)
		out.Evidence = c.Snapshot()
		return out, err
	}
	c.mu.Lock()
	owned := c.owned && c.evidence.Mode == SafeOwned && !c.evidence.Closed
	c.mu.Unlock()
	if !owned {
		out.Evidence = c.Snapshot()
		return out, failure(StateUnknown)
	}
	b := operationBudget{start: c.consumed}
	if _, err = c.queryLocked(ctx, Probe, &b); err != nil {
		return abort(err)
	}
	if _, err = c.queryLocked(ctx, SafeModeOwner, &b); err != nil {
		return abort(err)
	}
	c.mu.Lock()
	owned = c.owned && c.promptSafe && c.evidence.Mode == SafeOwned && !c.evidence.Closed &&
		c.evidence.OwnerQueryAvailable && c.evidence.Owner == OwnerThisSession
	if owned {
		// The read pump can observe the normal prompt before this operation
		// consumes its release acknowledgement. Preserve the pending state.
		c.evidence.Mode = ConfirmPending
	}
	c.mu.Unlock()
	if !owned {
		return abort(failure(StateUnknown))
	}
	if err = c.write(ctx, []byte{0x18}); err != nil {
		return abort(err)
	}
	out.Requested = true
	acknowledged := false
	for {
		e, err := c.next(ctx, &b)
		if err != nil {
			return abort(err)
		}
		switch e.kind {
		case hijackEvent:
			return abort(failure(OwnerConflict))
		case lineEvent:
			line := strings.TrimSpace(e.text)
			if line == "" {
				continue
			}
			if line != "Releasing Safe Mode... Success!" || acknowledged {
				return abort(failure(ProtocolUnknown))
			}
			acknowledged = true
		case promptEvent:
			if !acknowledged || e.safe {
				return abort(failure(ProtocolUnknown))
			}
			c.mu.Lock()
			valid := c.evidence.Mode == ConfirmPending && !c.evidence.Closed && !c.promptSafe
			if valid {
				c.owned = false
			}
			c.mu.Unlock()
			if !valid {
				return abort(failure(StateUnknown))
			}
			if _, err = c.queryLocked(ctx, SafeModeOwner, &b); err != nil {
				return abort(err)
			}
			c.mu.Lock()
			valid = c.evidence.Mode == ConfirmPending && !c.evidence.Closed && !c.promptSafe &&
				c.evidence.OwnerQueryAvailable && c.evidence.Owner == OwnerNone
			if valid {
				c.evidence.Mode = Released
				c.evidence.ObservedAt = time.Now().UTC()
				out.Released = true
				out.Evidence = c.evidence
			}
			c.mu.Unlock()
			if !valid {
				return abort(failure(StateUnknown))
			}
			return out, nil
		default:
			return abort(failure(ProtocolUnknown))
		}
	}
}
