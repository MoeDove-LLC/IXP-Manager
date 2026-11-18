package sflow

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"strings"
	"testing"
)

func words(v ...uint32) []byte {
	b := make([]byte, len(v)*4)
	for i, n := range v {
		binary.BigEndian.PutUint32(b[i*4:], n)
	}
	return b
}
func opaque(b []byte) []byte {
	out := append(words(uint32(len(b))), b...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	return out
}
func record(tag uint32, b []byte) []byte { return append(words(tag), opaque(b)...) }
func packet4() []byte {
	p := make([]byte, 54)
	copy(p, []byte{0, 1, 2, 3, 4, 5, 0, 6, 7, 8, 9, 10, 8, 0})
	p[14] = 0x45
	binary.BigEndian.PutUint16(p[16:18], 40)
	p[23] = 6
	copy(p[26:30], []byte{192, 0, 2, 99})
	copy(p[30:34], []byte{198, 51, 100, 99})
	binary.BigEndian.PutUint16(p[34:36], 179)
	binary.BigEndian.PutUint16(p[36:38], 12345)
	p[46] = 0x50
	p[47] = 0x10
	return p
}
func flowBytes(expanded bool, records ...[]byte) []byte {
	b := words(1, 1, 400, 4000, 2, 1, 2, uint32(len(records)))
	if expanded {
		b = words(1, 0, 0x1000000, 400, 4000, 2, 0, 0x1000000, 0, 0x1000001, uint32(len(records)))
	}
	for _, r := range records {
		b = append(b, r...)
	}
	return b
}
func headerRecord(protocol uint32, p []byte) []byte {
	return record(1, append(words(protocol, uint32(len(p))+4, 4), opaque(p)...))
}
func datagramBytes(tag uint32, b []byte) []byte {
	return append(words(5, 1, 0xc0000201, 0, 10, 100000, 1), record(tag, b)...)
}

func TestParseNormalExpandedAndMetadataOnly(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		tag := uint32(1)
		if expanded {
			tag = 3
		}
		p := append(packet4(), []byte("PRIVATE_APPLICATION_PAYLOAD")...)
		raw := datagramBytes(tag, flowBytes(expanded, headerRecord(1, p), record(1001, words(100, 0, 100, 0))))
		d, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Flows) != 1 || len(d.Counters) != 0 {
			t.Fatal("FLOW/CNTR confused")
		}
		f := d.Flows[0]
		if f.SourceMAC != "00:06:07:08:09:0a" || f.DestinationMAC != "00:01:02:03:04:05" || f.Family != 4 || !f.SampledTCP179Activity || f.EstimatedPackets != "400" || f.SampleCount != 1 || *f.InputVLAN != 100 {
			t.Fatalf("incorrect flow metadata: %+v", f)
		}
		if expanded && f.SourceIndex != 0x1000000 {
			t.Fatal("expanded source truncated")
		}
		encoded, _ := json.Marshal(d)
		for _, secret := range []string{"PRIVATE_APPLICATION_PAYLOAD", "192.0.2.99", "198.51.100.99", "12345", "Established"} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("packet data exposed: %s", secret)
			}
		}
		p[47] = 0x12
		d, err = Parse(datagramBytes(tag, flowBytes(expanded, headerRecord(1, p))))
		if err != nil || d.Flows[0].SampledTCP179Activity {
			t.Fatal("SYN activity claimed ACK-only BGP evidence")
		}
	}
}
func TestParseVLANIPv6AndTruncatedHeader(t *testing.T) {
	p := packet4()
	tagged := append(append(append([]byte{}, p[:12]...), []byte{0x88, 0xa8, 0, 100, 0x81, 0, 0, 200, 8, 0}...), p[14:]...)
	d, err := Parse(datagramBytes(1, flowBytes(false, headerRecord(1, tagged))))
	if err != nil || len(d.Flows[0].HeaderVLANs) != 2 || d.Flows[0].HeaderVLANs[1] != 200 {
		t.Fatal("QinQ lost", err)
	}
	v6 := make([]byte, 60)
	v6[0] = 0x60
	binary.BigEndian.PutUint16(v6[4:6], 20)
	v6[6] = 6
	copy(v6[40:], p[34:])
	d, err = Parse(datagramBytes(3, flowBytes(true, headerRecord(12, v6))))
	if err != nil || d.Flows[0].Family != 6 || d.Flows[0].UnattributedReason != "missing-mac-metadata" || !d.Flows[0].SampledTCP179Activity {
		t.Fatal("IPv6 metadata unavailable", err)
	}
	d, err = Parse(datagramBytes(1, flowBytes(false, headerRecord(1, []byte{0, 1, 2}))))
	if err != nil || d.Flows[0].UnattributedReason != "truncated-ethernet-header" {
		t.Fatal("valid short capture fabricated headers", err)
	}
}
func TestParseCountersAndExtensionsStaySeparate(t *testing.T) {
	counter := words(7, 6, math.MaxUint32, math.MaxUint32, 1, 3, math.MaxUint32, math.MaxUint32, 1, 2, 3, 4, 5, 6, 0, 900, 7, 8, 9, 10, 11, 0)
	body := append(words(2, 7, 2), record(1, counter)...)
	body = append(body, record(42, words(99))...)
	d, err := Parse(datagramBytes(2, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Flows) != 0 || len(d.Counters) != 1 || d.Counters[0].Interfaces[0].InOctets != "18446744073709551615" || d.UnknownRecords != 1 {
		t.Fatalf("counter precision/extensions lost: %+v", d)
	}
	d, err = Parse(datagramBytes(1, flowBytes(false, headerRecord(1, packet4()), record(4096+99, words(123)))))
	if err != nil || d.UnknownRecords != 1 {
		t.Fatal("vendor extension not bounded/skipped", err)
	}
}
func TestParseRejectsTruncationAndMaliciousLengths(t *testing.T) {
	raw := datagramBytes(1, flowBytes(false, headerRecord(1, packet4())))
	for i := 0; i < len(raw); i++ {
		if d, err := Parse(raw[:i]); err == nil || len(d.Flows) != 0 {
			t.Fatalf("truncated datagram accepted at %d", i)
		}
	}
	for _, bad := range [][]byte{append(append([]byte{}, raw...), 0), make([]byte, MaxDatagramBytes+1), append(words(5, 1, 0xc0000201, 0, 1, 1, MaxSamples+1), make([]byte, 8)...), append(words(5, 1, 0xc0000201, 0, 1, 1, 1, 1), words(math.MaxUint32)...), datagramBytes(1, words(1, 1, 1, 1, 0, 1, 2, MaxRecords+1))} {
		if d, err := Parse(bad); err == nil || len(d.Flows) != 0 {
			t.Fatal("malicious bound accepted")
		}
	}
	oversize := append(words(1, math.MaxUint32, 0, MaxHeaderBytes+1), make([]byte, MaxHeaderBytes+1)...)
	if _, err := Parse(datagramBytes(1, flowBytes(false, record(1, oversize)))); err == nil {
		t.Fatal("oversized captured header accepted")
	}
}
func TestTrackerDoesNotAdvanceOnDuplicateOrOutOfOrder(t *testing.T) {
	var tr Tracker
	d, _ := Parse(datagramBytes(1, flowBytes(false, headerRecord(1, packet4()))))
	q, err := tr.Observe("exporter", d)
	if err != nil || q.SequenceState != "first" {
		t.Fatal(q, err)
	}
	q, _ = tr.Observe("exporter", d)
	if q.SequenceState != "duplicate" {
		t.Fatal(q)
	}
	d.Sequence = 12
	d.UptimeMS += 1000
	q, _ = tr.Observe("exporter", d)
	if q.MissingDatagrams != 1 {
		t.Fatal(q)
	}
	d.Sequence = 11
	d.UptimeMS -= 500
	q, _ = tr.Observe("exporter", d)
	if q.SequenceState != "out-of-order" {
		t.Fatal(q)
	}
	d.Sequence = 13
	d.UptimeMS += 1000
	q, _ = tr.Observe("exporter", d)
	if q.SequenceState != "in-order" {
		t.Fatal(q)
	}
	d.Sequence = 1
	d.UptimeMS = 1
	q, _ = tr.Observe("exporter", d)
	if q.SequenceState != "restart" || q.UDPAuthenticated {
		t.Fatal(q)
	}
	d.AgentAddress = "192.0.2.2"
	if _, err = tr.Observe("exporter", d); err == nil {
		t.Fatal("identity tuple silently changed")
	}
}
func TestValidateBatchRejectsInventedEstimates(t *testing.T) {
	d, _ := Parse(datagramBytes(1, flowBytes(false, headerRecord(1, packet4()))))
	d.Flows[0].EstimatedBytes = "900719925474099300000000"
	if ValidateDatagram(d) == nil {
		t.Fatal("invented estimate accepted")
	}
	d.Flows[0].EstimatedBytes = "1"
	d.Flows[0].UnattributedReason = strings.Repeat("x", 2000)
	if ValidateDatagram(d) == nil {
		t.Fatal("arbitrary diagnostic text accepted")
	}
}

func TestBoundedRandomFramesDoNotPanicOrReturnPartialData(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 256; i++ {
		raw := make([]byte, rng.Intn(2048))
		_, _ = rng.Read(raw)
		d, err := Parse(raw)
		if err != nil && (len(d.Flows) != 0 || len(d.Counters) != 0) {
			t.Fatal("malformed frame returned partial data")
		}
	}
}
