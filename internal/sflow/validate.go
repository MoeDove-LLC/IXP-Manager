package sflow

import (
	"errors"
	"net"
	"strconv"
)

func validMAC(s string) bool {
	b, err := net.ParseMAC(s)
	return err == nil && len(b) == 6 && net.HardwareAddr(b).String() == s
}
func decimal(s string) bool {
	v, err := strconv.ParseUint(s, 10, 64)
	return err == nil && strconv.FormatUint(v, 10) == s
}
func ValidateBatch(b Batch) error {
	if b.Version != 1 || !identifier.MatchString(b.ExporterID) || !identifier.MatchString(b.SwitchID) || !revisionPattern.MatchString(b.MappingRevision) || b.ReceivedAt.IsZero() || !validIP(b.TransportSource) || b.Quality.UDPAuthenticated {
		return errors.New("invalid sFlow batch identity or authentication claim")
	}
	switch b.Quality.SequenceState {
	case "first", "in-order", "gap", "wrap", "restart", "duplicate", "sequence-conflict", "out-of-order":
	default:
		return errors.New("invalid sFlow sequence quality")
	}
	if b.Quality.CounterSamples != uint32(len(b.Datagram.Counters)) {
		return errors.New("sFlow counter sample quality mismatch")
	}
	if b.RequestNonce != "" && !validNonce(b.RequestNonce) {
		return errors.New("invalid per-packet request nonce")
	}
	if b.DatagramDigest != "" && (!validDigest(b.DatagramDigest) || b.DatagramDigest != digest(b.Datagram)) {
		return errors.New("safe datagram digest mismatch")
	}
	if b.RequestDeliveries && (b.RequestNonce == "" || b.DatagramDigest == "") {
		return errors.New("forwarding requires a bound nonce and datagram digest")
	}
	return ValidateDatagram(b.Datagram)
}
func ValidateDatagram(d Datagram) error {
	if d.Version != 5 || !validIP(d.AgentAddress) || len(d.Flows)+len(d.Counters)+int(d.UnknownSamples) > MaxSamples || d.UnknownRecords > MaxSamples*MaxRecords {
		return errMalformed
	}
	for _, f := range d.Flows {
		if f.SampleCount != 1 || f.Family != 0 && f.Family != 4 && f.Family != 6 || len(f.HeaderVLANs) > 4 || f.UnknownRecords > MaxRecords+1 || f.Input.Format > 3 || f.Output.Format > 3 {
			return errMalformed
		}
		if f.SourceMAC != "" && !validMAC(f.SourceMAC) || f.DestinationMAC != "" && !validMAC(f.DestinationMAC) {
			return errMalformed
		}
		for _, v := range f.HeaderVLANs {
			if v > 4095 {
				return errMalformed
			}
		}
		for _, v := range []*uint32{f.InputVLAN, f.OutputVLAN} {
			if v != nil && *v > 4095 {
				return errMalformed
			}
		}
		switch f.UnattributedReason {
		case "", "truncated-ethernet-header", "unsupported-vlan-depth", "truncated-vlan-header", "unsupported-network-protocol", "truncated-ipv4-header", "invalid-ipv4-header", "truncated-ipv6-header", "unsupported-ipv6-jumbogram", "unsupported-header-protocol", "missing-packet-record", "unknown-sampling-rate", "unknown-packet-size", "missing-mac-metadata", "unsupported-source-type":
		default:
			return errMalformed
		}
		if f.SamplingRate == 0 || f.PacketSize == 0 {
			if f.EstimatedBytes != "" || f.EstimatedPackets != "" || f.UnattributedReason == "" {
				return errMalformed
			}
		} else if f.EstimatedBytes != strconv.FormatUint(uint64(f.PacketSize)*uint64(f.SamplingRate), 10) || f.EstimatedPackets != strconv.FormatUint(uint64(f.SamplingRate), 10) {
			return errMalformed
		}
		if f.UnattributedReason == "" && (f.SourceMAC == "" || f.DestinationMAC == "" || f.Family == 0) {
			return errMalformed
		}
		if f.SampledTCP179Activity && f.Family == 0 {
			return errMalformed
		}
	}
	for _, s := range d.Counters {
		if len(s.Interfaces) > MaxRecords || s.UnknownRecords > MaxRecords+1 {
			return errMalformed
		}
		for _, c := range s.Interfaces {
			if !decimal(c.IfSpeed) || !decimal(c.InOctets) || !decimal(c.OutOctets) {
				return errMalformed
			}
		}
	}
	return nil
}
