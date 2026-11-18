package sflow

import "errors"

type sequenceSource struct {
	kind              uint8
	sourceType, index uint32
}
type sequenceState struct {
	agent                      string
	subAgent, sequence, uptime uint32
	sources                    map[sequenceSource]uint32
}

// Tracker is intentionally bounded and single-threaded. UDP state is quality
// evidence only; neither a reset nor an allowlisted address authenticates an agent.
type Tracker struct{ states map[string]sequenceState }

func (t *Tracker) Observe(exporterID string, d Datagram) (Quality, error) {
	q := Quality{SequenceState: "first", CounterSamples: uint32(len(d.Counters))}
	if !identifier.MatchString(exporterID) || ValidateDatagram(d) != nil {
		return q, errors.New("invalid bounded sFlow tracker input")
	}
	if t.states == nil {
		t.states = map[string]sequenceState{}
	}
	old, exists := t.states[exporterID]
	if !exists && len(t.states) >= 64 {
		return q, errors.New("sFlow tracker exporter limit reached")
	}
	if exists && (old.agent != d.AgentAddress || old.subAgent != d.SubAgentID) {
		return q, errors.New("sFlow exporter identity tuple changed")
	}
	sources := make(map[sequenceSource]uint32, len(old.sources))
	for key, value := range old.sources {
		sources[key] = value
	}
	if exists {
		delta := d.Sequence - old.sequence
		uptimeDelta := d.UptimeMS - old.uptime
		switch {
		case delta == 0 && d.UptimeMS == old.uptime:
			q.SequenceState = "duplicate"
			return q, nil
		case delta == 0:
			q.SequenceState = "sequence-conflict"
			return q, nil
		case delta < 1<<31 && uptimeDelta < 1<<31:
			q.SequenceState = "in-order"
			if delta > 1 {
				q.SequenceState = "gap"
				q.MissingDatagrams = delta - 1
			}
			if d.Sequence < old.sequence {
				q.SequenceState = "wrap"
			}
		case d.UptimeMS < old.uptime && old.uptime-d.UptimeMS > 60000 && d.Sequence < old.sequence && d.Sequence <= 16:
			q.SequenceState = "restart"
			sources = map[sequenceSource]uint32{}
		default:
			q.SequenceState = "out-of-order"
			return q, nil
		}
	}
	track := func(k sequenceSource, seq uint32) error {
		prior, ok := sources[k]
		if !ok && len(sources) >= 4096 {
			return errors.New("sFlow source tracker limit reached")
		}
		if ok {
			delta := seq - prior
			if delta > 1 && delta < 1<<31 {
				q.SampleSequenceGaps += uint64(delta - 1)
			}
		}
		sources[k] = seq
		return nil
	}
	for _, f := range d.Flows {
		if err := track(sequenceSource{1, f.SourceType, f.SourceIndex}, f.Sequence); err != nil {
			return q, err
		}
	}
	for _, c := range d.Counters {
		if err := track(sequenceSource{2, c.SourceType, c.SourceIndex}, c.Sequence); err != nil {
			return q, err
		}
	}
	t.states[exporterID] = sequenceState{d.AgentAddress, d.SubAgentID, d.Sequence, d.UptimeMS, sources}
	return q, nil
}
