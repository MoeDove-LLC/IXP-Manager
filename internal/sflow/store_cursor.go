package sflow

import (
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

const maxExporters = 4096

type cursorPoint struct {
	Sequence   uint32    `json:"sequence"`
	UptimeMS   uint32    `json:"uptimeMs"`
	Digest     string    `json:"digest"`
	ReceivedAt time.Time `json:"receivedAt"`
}
type exporterCursor struct {
	ExporterID     string       `json:"exporterId"`
	SwitchID       string       `json:"switchId"`
	Head           cursorPoint  `json:"head"`
	LastDatagram   *cursorPoint `json:"lastDatagram,omitempty"`
	Pending        *cursorPoint `json:"pending,omitempty"`
	LastReceivedAt time.Time    `json:"lastReceivedAt"`
	SequenceState  string       `json:"sequenceState"`
}

func validDigest(v string) bool {
	raw, err := hex.DecodeString(v)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == v
}
func hasLegacyExporter(s flowSnapshot, id string) bool {
	for _, row := range s.Rows {
		if row.ExporterID == id {
			return true
		}
	}
	for _, q := range s.Quality {
		if q.ExporterID == id {
			return true
		}
	}
	return false
}
func validCursor(c exporterCursor) bool {
	if !identifier.MatchString(c.ExporterID) || !identifier.MatchString(c.SwitchID) || !validDigest(c.Head.Digest) || c.Head.ReceivedAt.IsZero() || c.LastReceivedAt.IsZero() {
		return false
	}
	if c.Pending != nil && (!validDigest(c.Pending.Digest) || c.Pending.ReceivedAt.IsZero()) {
		return false
	}
	if c.LastDatagram != nil && (!validDigest(c.LastDatagram.Digest) || c.LastDatagram.ReceivedAt.IsZero()) {
		return false
	}
	switch c.SequenceState {
	case "first", "in-order", "gap", "wrap", "uptime-wrap", "duplicate", "sequence-conflict", "out-of-order", "restart-candidate", "restart-inferred", "legacy-baseline":
		return true
	}
	return false
}
func advances(old, next cursorPoint) bool {
	delta := next.Sequence - old.Sequence
	uptime := next.UptimeMS - old.UptimeMS
	if delta == 0 || delta >= 1<<31 || uptime >= 1<<31 || next.ReceivedAt.Before(old.ReceivedAt) {
		return false
	}
	// Allow bounded export/batching jitter, not an arbitrary old uptime moving
	// modulo2^32 into the future. Receipt time still is not packet capture time.
	elapsed := next.ReceivedAt.Sub(old.ReceivedAt)
	return uint64(uptime) <= uint64(elapsed/time.Millisecond)+30000
}
func classifyCursor(old *exporterCursor, b AttributedBatch, legacy bool) (Classification, exporterCursor) {
	p := cursorPoint{b.Sequence, b.UptimeMS, b.DatagramDigest, b.ReceivedAt}
	result := Classification{SequenceState: "first", AcceptFlows: true}
	next := exporterCursor{ExporterID: b.ExporterID, SwitchID: b.SwitchID, Head: p, LastDatagram: &p, LastReceivedAt: b.ReceivedAt, SequenceState: "first"}
	if old == nil {
		if legacy {
			result.SequenceState = "legacy-baseline"
			result.AcceptFlows = false
			next.SequenceState = result.SequenceState
		}
		return result, next
	}
	next = *old
	next.SwitchID = b.SwitchID
	next.LastDatagram = &p
	if b.ReceivedAt.After(next.LastReceivedAt) {
		next.LastReceivedAt = b.ReceivedAt
	}
	result.AcceptFlows = false
	switch {
	case p.Sequence == old.Head.Sequence && p.UptimeMS == old.Head.UptimeMS:
		result.SequenceState = "sequence-conflict"
		if p.Digest == old.Head.Digest {
			result.SequenceState = "duplicate"
		}
	case p.Sequence == old.Head.Sequence:
		result.SequenceState = "sequence-conflict"
	case advances(old.Head, p):
		delta := p.Sequence - old.Head.Sequence
		result = Classification{SequenceState: "in-order", AcceptFlows: true, MissingDatagrams: delta - 1}
		if delta > 1 {
			result.SequenceState = "gap"
		}
		if p.Sequence < old.Head.Sequence {
			result.SequenceState = "wrap"
		}
		if p.UptimeMS < old.Head.UptimeMS {
			result.SequenceState = "uptime-wrap"
		}
		next.Head = p
		next.Pending = nil
	case old.Pending != nil && p.Sequence == old.Pending.Sequence && p.UptimeMS == old.Pending.UptimeMS:
		result.SequenceState = "sequence-conflict"
		if p.Digest == old.Pending.Digest {
			result.SequenceState = "duplicate"
		}
	case old.Pending != nil && p.Sequence-old.Pending.Sequence == 1 && advances(*old.Pending, p) && p.ReceivedAt.Sub(old.Pending.ReceivedAt) <= time.Minute:
		result = Classification{SequenceState: "restart-inferred", AcceptFlows: true}
		next.Head = p
		next.Pending = nil
	case p.Sequence < old.Head.Sequence && p.Sequence <= 16 && p.UptimeMS < old.Head.UptimeMS && !p.ReceivedAt.Before(old.Head.ReceivedAt):
		result.SequenceState = "restart-candidate"
		copy := p
		next.Pending = &copy
	default:
		result.SequenceState = "out-of-order"
	}
	next.SequenceState = result.SequenceState
	return result, next
}

// Classify is advisory only. IngestWithResult repeats this under its write lock;
// callers must not use this preview as an independent authorization to count.
func (s *Store) Classify(b AttributedBatch) (Classification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return Classification{}, errors.New("sFlow store closed")
	}
	if !identifier.MatchString(b.ExporterID) || !identifier.MatchString(b.SwitchID) || !validDigest(b.DatagramDigest) || b.ReceivedAt.IsZero() {
		return Classification{}, errors.New("invalid datagram cursor identity")
	}
	var old *exporterCursor
	if value, ok := s.state.Cursors[b.ExporterID]; ok {
		old = &value
	}
	r, _ := classifyCursor(old, b, old == nil && s.exporterHistory[b.ExporterID])
	return r, nil
}
func (s *Store) Status() ([]ExporterStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, errors.New("sFlow store closed")
	}
	out := []ExporterStatus{}
	for _, c := range s.state.Cursors {
		last := c.Head
		if c.LastDatagram != nil {
			last = *c.LastDatagram
		}
		out = append(out, ExporterStatus{ExporterID: c.ExporterID, SwitchID: c.SwitchID, Sequence: c.Head.Sequence, UptimeMS: c.Head.UptimeMS, LastDatagramSequence: last.Sequence, LastDatagramUptimeMS: last.UptimeMS, LastReceivedAt: c.LastReceivedAt, SequenceState: c.SequenceState, PendingRestart: c.Pending != nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExporterID < out[j].ExporterID })
	return out, nil
}
