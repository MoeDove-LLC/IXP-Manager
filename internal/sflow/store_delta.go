package sflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// A mutation contains only the records touched by one bounded datagram. Neither
// preparation nor byte accounting enumerates unrelated history or HTTP IDs.
type storeMutation struct {
	Rows           map[string]Bucket
	Quality        map[string]QualityDay
	Cursors        map[string]exporterCursor
	SeenID         string
	Seen           *seenBatch
	Classification Classification
	ByteChange     int64
}

func entryBytes(key string, value any) (int64, error) {
	k, err := json.Marshal(key)
	if err != nil {
		return 0, err
	}
	v, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	// Include a comma for every entry: this intentionally overcounts each
	// nonempty map by one byte and therefore remains a conservative bound.
	return int64(len(k) + len(v) + 2), nil
}
func mutationBytes[T any](changes, current map[string]T) (int64, error) {
	var delta int64
	for key, value := range changes {
		n, err := entryBytes(key, value)
		if err != nil {
			return 0, err
		}
		delta += n
		if old, ok := current[key]; ok {
			n, err = entryBytes(key, old)
			if err != nil {
				return 0, err
			}
			delta -= n
		}
	}
	return delta, nil
}

func prepareMutation(state flowSnapshot, b AttributedBatch, now time.Time, legacy bool) (storeMutation, error) {
	empty := storeMutation{}
	m := storeMutation{Rows: map[string]Bucket{}, Quality: map[string]QualityDay{}, Cursors: map[string]exporterCursor{}, Classification: Classification{SequenceState: b.Quality.SequenceState, AcceptFlows: true}}
	checksum := digest(b)
	if old, ok := state.Seen[b.ID]; ok {
		if old.Checksum != checksum {
			return empty, errors.New("sFlow batch ID reused with different content")
		}
		m.Classification = Classification{SequenceState: "duplicate"}
		return m, nil
	}
	if len(state.Seen) >= maxSeen {
		return empty, errors.New("sFlow recent batch quota exceeded")
	}
	if b.DatagramDigest != "" {
		var prior *exporterCursor
		if c, ok := state.Cursors[b.ExporterID]; ok {
			prior = &c
		} else if len(state.Cursors) >= maxExporters {
			return empty, errors.New("sFlow durable exporter cursor quota exceeded")
		}
		classification, cursor := classifyCursor(prior, b, prior == nil && legacy)
		m.Classification = classification
		m.Cursors[b.ExporterID] = cursor
		b.Quality.SequenceState = classification.SequenceState
		b.Quality.MissingDatagrams = classification.MissingDatagrams
	}
	flows := b.Flows
	if !m.Classification.AcceptFlows {
		flows = nil
	}
	newRows := 0
	for _, flow := range flows {
		for _, resolution := range []string{"5m", "1d"} {
			step, retention := 5*time.Minute, FiveMinuteRetention
			if resolution == "1d" {
				step, retention = 24*time.Hour, DailyRetention
			}
			if b.ReceivedAt.Before(now.Add(-retention)) {
				continue
			}
			row := Bucket{Start: b.ReceivedAt.UTC().Truncate(step), Resolution: resolution, ExporterID: b.ExporterID, SwitchID: b.SwitchID, InfrastructureID: b.InfrastructureID, VLANID: flow.VLANID, MappingRevision: b.MappingRevision, Family: flow.Family, SourceMemberID: flow.SourceMemberID, DestinationMemberID: flow.DestinationMemberID, SourceMemberRevision: flow.SourceMemberRevision, DestinationMemberRevision: flow.DestinationMemberRevision, SourceConnectionID: flow.SourceConnectionID, DestinationConnectionID: flow.DestinationConnectionID, EstimatedBytes: flow.EstimatedBytes, EstimatedPackets: flow.EstimatedPackets, SampleCount: fmt.Sprint(flow.SampleCount), TCP179Samples: "0", UnattributedReason: flow.UnattributedReason, FirstReceivedAt: b.ReceivedAt, LastReceivedAt: b.ReceivedAt}
			if flow.SampledTCP179Activity {
				row.TCP179Samples = row.SampleCount
				at := b.ReceivedAt
				row.LastTCP179At = &at
			}
			key := bucketKey(row)
			old, exists := m.Rows[key]
			if !exists {
				old, exists = state.Rows[key]
				if !exists {
					newRows++
				}
			}
			if exists {
				old.EstimatedBytes = addDecimal(old.EstimatedBytes, row.EstimatedBytes)
				old.EstimatedPackets = addDecimal(old.EstimatedPackets, row.EstimatedPackets)
				old.SampleCount = addDecimal(old.SampleCount, row.SampleCount)
				old.TCP179Samples = addDecimal(old.TCP179Samples, row.TCP179Samples)
				if row.FirstReceivedAt.Before(old.FirstReceivedAt) {
					old.FirstReceivedAt = row.FirstReceivedAt
				}
				if row.LastReceivedAt.After(old.LastReceivedAt) {
					old.LastReceivedAt = row.LastReceivedAt
				}
				if row.LastTCP179At != nil && (old.LastTCP179At == nil || row.LastTCP179At.After(*old.LastTCP179At)) {
					at := *row.LastTCP179At
					old.LastTCP179At = &at
				}
				row = old
			}
			if validateBucket(row) != nil {
				return empty, errors.New("sFlow aggregate numeric bound exceeded")
			}
			m.Rows[key] = row
		}
	}
	newQuality := 0
	if !b.ReceivedAt.Before(now.Add(-DailyRetention)) {
		q := QualityDay{Start: b.ReceivedAt.UTC().Truncate(24 * time.Hour), ExporterID: b.ExporterID, SwitchID: b.SwitchID, Datagrams: "0", MissingDatagrams: "0", DuplicateDatagrams: "0", UnorderedDatagrams: "0", RestartSignals: "0", SampleSequenceGaps: "0", CounterSamples: "0"}
		key := qualityKey(q)
		if old, ok := state.Quality[key]; ok {
			q = old
		} else {
			newQuality = 1
		}
		q.Datagrams = addDecimal(q.Datagrams, "1")
		q.MissingDatagrams = addDecimal(q.MissingDatagrams, fmt.Sprint(b.Quality.MissingDatagrams))
		q.SampleSequenceGaps = addDecimal(q.SampleSequenceGaps, fmt.Sprint(b.Quality.SampleSequenceGaps))
		q.CounterSamples = addDecimal(q.CounterSamples, fmt.Sprint(b.Quality.CounterSamples))
		switch b.Quality.SequenceState {
		case "duplicate":
			q.DuplicateDatagrams = addDecimal(q.DuplicateDatagrams, "1")
		case "sequence-conflict", "out-of-order":
			q.UnorderedDatagrams = addDecimal(q.UnorderedDatagrams, "1")
		case "restart", "restart-candidate":
			q.RestartSignals = addDecimal(q.RestartSignals, "1")
		}
		if validateQuality(q) != nil {
			return empty, errors.New("quality counters exceed bound")
		}
		m.Quality[key] = q
	}
	if len(state.Rows)+len(state.Quality)+newRows+newQuality > MaxAggregateRows {
		return empty, errors.New("sFlow aggregate quota exceeded")
	}
	m.SeenID = b.ID
	m.Seen = &seenBatch{Checksum: checksum, At: b.ReceivedAt}
	for _, measure := range []func() (int64, error){func() (int64, error) { return mutationBytes(m.Rows, state.Rows) }, func() (int64, error) { return mutationBytes(m.Quality, state.Quality) }, func() (int64, error) { return mutationBytes(m.Cursors, state.Cursors) }, func() (int64, error) { return entryBytes(m.SeenID, *m.Seen) }} {
		n, err := measure()
		if err != nil {
			return empty, errors.New("invalid sFlow mutation encoding")
		}
		m.ByteChange += n
	}
	return m, nil
}
func commitMutation(state *flowSnapshot, m storeMutation) {
	if state.Rows == nil {
		state.Rows = map[string]Bucket{}
	}
	if state.Seen == nil {
		state.Seen = map[string]seenBatch{}
	}
	if state.Quality == nil {
		state.Quality = map[string]QualityDay{}
	}
	if state.Cursors == nil {
		state.Cursors = map[string]exporterCursor{}
	}
	for key, row := range m.Rows {
		state.Rows[key] = row
	}
	for key, q := range m.Quality {
		state.Quality[key] = q
	}
	for key, c := range m.Cursors {
		state.Cursors[key] = c
	}
	if m.Seen != nil {
		state.Seen[m.SeenID] = *m.Seen
	}
}

// Rebuild only at open/periodic maintenance. The512-byte allowance covers fixed
// object syntax, the checksum and all20 possible WAL sequence digits.
func (s *Store) rebuildAccountingLocked() error {
	bytes := int64(512)
	history := map[string]bool{}
	for key, row := range s.state.Rows {
		n, err := entryBytes(key, row)
		if err != nil {
			return err
		}
		bytes += n
		history[row.ExporterID] = true
	}
	for key, q := range s.state.Quality {
		n, err := entryBytes(key, q)
		if err != nil {
			return err
		}
		bytes += n
		history[q.ExporterID] = true
	}
	for key, c := range s.state.Cursors {
		n, err := entryBytes(key, c)
		if err != nil {
			return err
		}
		bytes += n
		history[c.ExporterID] = true
	}
	for key, v := range s.state.Seen {
		n, err := entryBytes(key, v)
		if err != nil {
			return err
		}
		bytes += n
	}
	s.stateBytes = bytes
	s.exporterHistory = history
	return nil
}
