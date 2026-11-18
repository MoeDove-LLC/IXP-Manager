package terminal

import (
	"context"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

var ownedConnectionFields = map[string]bool{
	"name": true, "instance": true, "as": true, "routing-table": true, "templates": true,
	"disabled": true, "afi": true, "local.address": true, "local.role": true,
	"remote.address": true, "remote.as": true, "connect": true, "listen": true,
	"nexthop-choice": true, "input.filter": true, "input.limit-process-routes-ipv4": true,
	"input.limit-process-routes-ipv6": true, "output.filter-chain": true,
	"output.default-originate": true, "output.default-prepend": true, "tcp-md5-key": true,
}

func ownedPropertyAllowed(resource api.Resource, name string) bool {
	switch resource {
	case api.FilterRule:
		return name == "rule"
	case api.BGPInstance:
		return name == "as" || name == "router-id"
	case api.BGPConnection:
		return ownedConnectionFields[name]
	}
	return false
}
func ownedASCII(value string) bool {
	for _, b := range []byte(value) {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}
func ownedPortableSecret(value string) bool {
	if len(value) > 80 {
		return false
	}
	for _, b := range []byte(value) {
		if !(b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/' || b == '=' || b == '_' || b == '-') {
			return false
		}
	}
	return true
}
func ownedQuote(value string) string {
	var b strings.Builder
	b.WriteByte(34)
	for i := 0; i < len(value); i++ {
		v := value[i]
		if v == 92 || v == 34 || v == 36 {
			b.WriteByte(92)
		}
		b.WriteByte(v)
	}
	b.WriteByte(34)
	return b.String()
}
func ownedNumeric(name string) bool {
	return name == "as" || name == "remote.as" || name == "input.limit-process-routes-ipv4" || name == "input.limit-process-routes-ipv6" || name == "output.default-prepend"
}

// ValidateOwnedMutation is narrower than the API schema: no Add/Remove, no
// arbitrary path, no selector expressions, and no reset beyond AF limits.
// Namespace ownership is additionally enforced by the caller's canonical bridge.
func ValidateOwnedMutation(m api.Mutation) error {
	if err := api.ValidateMutation(m); err != nil {
		return failure(InvalidInput)
	}
	if m.Resource != api.FilterRule && m.Resource != api.BGPInstance && m.Resource != api.BGPConnection {
		return failure(InvalidInput)
	}
	switch m.Action {
	case api.Set:
	case api.Unset:
		if m.Resource != api.BGPConnection || (m.Property != "input.limit-process-routes-ipv4" && m.Property != "input.limit-process-routes-ipv6") {
			return failure(InvalidInput)
		}
	case api.Enable, api.Disable:
		if m.Resource != api.BGPConnection {
			return failure(InvalidInput)
		}
	default:
		return failure(InvalidInput)
	}
	for _, a := range m.Attributes {
		if !ownedPropertyAllowed(m.Resource, a.Name) || !ownedASCII(a.Value) {
			return failure(InvalidInput)
		}
		switch a.Name {
		case "local.role":
			if a.Value != "ebgp-rs" {
				return failure(InvalidInput)
			}
		case "nexthop-choice":
			if a.Value != "propagate" {
				return failure(InvalidInput)
			}
		case "output.default-originate":
			if a.Value != "never" {
				return failure(InvalidInput)
			}
		case "output.default-prepend":
			if a.Value != "0" {
				return failure(InvalidInput)
			}
		case "disabled", "connect", "listen":
			if a.Value != "yes" && a.Value != "true" {
				return failure(InvalidInput)
			}
		case "afi":
			if a.Value != "ip" && a.Value != "ipv6" {
				return failure(InvalidInput)
			}
		case "tcp-md5-key":
			if !ownedPortableSecret(a.Value) {
				return failure(InvalidInput)
			}
		}
	}
	return nil
}

// encodeOwned emits CLI syntax, not API word syntax. The exact target version
// must verify these fixed command forms before the live activation gate opens.
func encodeOwned(m api.Mutation) ([]byte, error) {
	if err := ValidateOwnedMutation(m); err != nil {
		return nil, err
	}
	path := ""
	switch m.Resource {
	case api.FilterRule:
		path = "/routing/filter/rule"
	case api.BGPInstance:
		path = "/routing/bgp/instance"
	case api.BGPConnection:
		path = "/routing/bgp/connection"
	}
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte('/')
	b.WriteString(string(m.Action))
	b.WriteByte(' ')
	b.WriteString(strings.ToUpper(m.ID))
	if m.Action == api.Unset {
		b.WriteByte(' ')
		b.WriteString(m.Property)
	}
	attrs := append([]api.Attribute(nil), m.Attributes...)
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	for _, a := range attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name)
		b.WriteByte('=')
		if ownedNumeric(a.Name) || a.Name == "disabled" || a.Name == "connect" || a.Name == "listen" {
			b.WriteString(a.Value)
		} else {
			b.WriteString(ownedQuote(a.Value))
		}
	}
	if b.Len() > 3072 {
		return nil, failure(BoundsExceeded)
	}
	return []byte(b.String()), nil
}
func (c *Client) ownedNow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owned && c.promptSafe && c.evidence.Mode == SafeOwned && !c.evidence.Closed && c.evidence.OwnerQueryAvailable && c.evidence.Owner == OwnerThisSession
}
func trimMarker(kind, nonce string) string {
	return strings.TrimSuffix(putMarker(kind, nonce), string(rune(13)))
}

// ExecuteOwned serializes a single foreground mutation through the same
// persistent channel as Take/Confirm/Cancel. No API write connection participates.
// Echoes and device errors stay inside bounded private buffers. An unknown
// completion closes this original channel and never replays the mutation.
func (c *Client) ExecuteOwned(parent context.Context, m api.Mutation) (OwnedReceipt, error) {
	out := OwnedReceipt{}
	command, err := encodeOwned(m)
	if err != nil {
		return out, err
	}
	defer clear(command)
	ctx, end, err := c.begin(parent)
	if err != nil {
		return out, err
	}
	defer end()
	abort := func(err error) (OwnedReceipt, error) { c.stop(err); out.Evidence = c.Snapshot(); return out, err }
	c.mu.Lock()
	eligible := c.owned && c.evidence.Mode == SafeOwned && !c.evidence.Closed
	c.mu.Unlock()
	if !eligible {
		return out, failure(StateUnknown)
	}
	b := operationBudget{start: c.consumed}
	if _, err = c.queryLocked(ctx, Probe, &b); err != nil {
		return abort(err)
	}
	if _, err = c.queryLocked(ctx, SafeModeOwner, &b); err != nil {
		return abort(err)
	}
	before, err := c.queryLocked(ctx, FloatingUndoCount, &b)
	if err != nil {
		return abort(err)
	}
	reserve := len(m.Attributes) + 1
	if reserve < 2 {
		reserve = 2
	}
	if !c.ownedNow() || !before.HistoryKnown || before.HistoryActions+reserve > 80 {
		return abort(failure(StateUnknown))
	}
	nonce, begin, finish, err := markers()
	if err != nil {
		return abort(err)
	}
	okMarker, failMarker := "MD_OK_"+nonce, "MD_FAIL_"+nonce
	wire := []byte(trimMarker("BEGIN", nonce) + "; :do { " + string(command) + "; " + trimMarker("OK", nonce) + " } on-error={ " + trimMarker("FAIL", nonce) + " }; " + putMarker("END", nonce))
	defer clear(wire)
	if len(wire) > c.limits.MaxLineBytes {
		return abort(failure(BoundsExceeded))
	}
	if !c.ownedNow() {
		return abort(failure(OwnershipLost))
	}
	if err = c.write(ctx, wire); err != nil {
		return abort(err)
	}
	started, finished, succeeded, failed := false, false, false, false
	for {
		ev, e := c.next(ctx, &b)
		if e != nil {
			return abort(e)
		}
		line := strings.TrimSpace(ev.text)
		if ev.kind == hijackEvent || ev.kind == lostEvent || ev.kind == loginEvent {
			return abort(failure(OwnershipLost))
		}
		if ev.kind == lineEvent && rejected(line) {
			failed = true
		}
		if ev.kind == lineEvent && line == begin {
			if started || finished {
				return abort(failure(ProtocolUnknown))
			}
			started = true
			continue
		}
		if started && ev.kind == lineEvent {
			switch line {
			case okMarker:
				if succeeded || failed || finished {
					return abort(failure(ProtocolUnknown))
				}
				succeeded = true
				continue
			case failMarker:
				if succeeded || failed || finished {
					return abort(failure(ProtocolUnknown))
				}
				failed = true
				continue
			case finish:
				if finished || !succeeded && !failed {
					return abort(failure(ProtocolUnknown))
				}
				finished = true
				continue
			}
			if line != "" {
				return abort(failure(ProtocolUnknown))
			}
		}
		if ev.kind == promptEvent && (finished || failed) {
			if !ev.safe || !c.ownedNow() {
				return abort(failure(OwnershipLost))
			}
			if failed || !started || !finished || !succeeded {
				return abort(failure(RemoteRejected))
			}
			break
		}
	}
	if _, err = c.queryLocked(ctx, SafeModeOwner, &b); err != nil {
		return abort(err)
	}
	after, err := c.queryLocked(ctx, FloatingUndoCount, &b)
	if err != nil {
		return abort(err)
	}
	if !c.ownedNow() || !after.HistoryKnown || after.HistoryActions < before.HistoryActions || after.HistoryActions > 80 {
		return abort(failure(StateUnknown))
	}
	out.Completed = true
	out.Evidence = c.Snapshot()
	return out, nil
}
