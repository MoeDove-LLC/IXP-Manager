package api

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func testLimits() Limits {
	return Limits{MaxWordBytes: 64 << 10, MaxSentenceBytes: 128 << 10, MaxWords: 128, MaxReplyBytes: 4 << 20, MaxRecords: 20000, MaxSentences: 40064}
}
func TestLengthEncodingOfficialBoundaries(t *testing.T) {
	cases := []struct {
		value uint32
		wire  []byte
	}{{0, []byte{0}}, {0x7f, []byte{0x7f}}, {0x80, []byte{0x80, 0x80}}, {0x3fff, []byte{0xbf, 0xff}}, {0x4000, []byte{0xc0, 0x40, 0}}, {0x1fffff, []byte{0xdf, 0xff, 0xff}}, {0x200000, []byte{0xe0, 0x20, 0, 0}}, {0xfffffff, []byte{0xef, 0xff, 0xff, 0xff}}, {0x10000000, []byte{0xf0, 0x10, 0, 0, 0}}, {0xffffffff, []byte{0xf0, 0xff, 0xff, 0xff, 0xff}}}
	for _, test := range cases {
		if raw := encodeLength(test.value); !bytes.Equal(raw, test.wire) {
			t.Fatalf("length %#x encoding mismatch", test.value)
		}
		value, e := decodeLength(bytes.NewReader(test.wire))
		if e != nil || value != test.value {
			t.Fatalf("length %#x decoding: %d %v", test.value, value, e)
		}
	}
}
func TestLengthRejectsReservedNoncanonicalAndTruncatedForms(t *testing.T) {
	for _, raw := range [][]byte{{0xf1}, {0xf7}, {0xf8}, {0xff}, {0x80, 0}, {0xc0, 0, 0x80}, {0xe0, 0, 0x40, 0}, {0xf0, 0, 0x20, 0, 0}} {
		if _, e := decodeLength(bytes.NewReader(raw)); !errors.Is(e, errProtocol) {
			t.Fatal("unknown/noncanonical framing accepted", raw, e)
		}
	}
	for _, raw := range [][]byte{{0x80}, {0xc0, 0x40}, {0xe1, 0, 0}, {0xf0, 0x10, 0, 0}} {
		if _, e := decodeLength(bytes.NewReader(raw)); e == nil {
			t.Fatal("truncated length accepted")
		}
	}
	reader := replyReader{r: bytes.NewReader([]byte{0xf0, 0xff, 0xff, 0xff, 0xff}), limits: testLimits()}
	if _, e := reader.sentence(); !errors.Is(e, errLimit) {
		t.Fatal("untrusted length was not bounded before allocation", e)
	}
}
func TestSentenceUTF8EqualsAndBoundedEmptySentences(t *testing.T) {
	words := []string{"!re", "=name=身份=a=b", ".tag=ixp-1"}
	wire, e := encodeSentence(words, testLimits())
	if e != nil {
		t.Fatal(e)
	}
	reader := replyReader{r: bytes.NewReader(wire), limits: testLimits()}
	got, e := reader.sentence()
	if e != nil || len(got) != 3 || got[1] != words[1] {
		t.Fatal("word byte length/value was altered")
	}
	reply, e := parseReply(got)
	if e != nil || reply.values["name"] != "身份=a=b" || reply.tag != "ixp-1" {
		t.Fatal("attribute equals/tag parsing failed")
	}
	for _, words := range [][]string{{"!re", "=name=a", "=name=b"}, {"!done", ".tag=1", ".tag=1"}, {"!unknown"}, {"!re", "bad"}} {
		if _, e = parseReply(words); e == nil {
			t.Fatal("ambiguous reply accepted")
		}
	}
	limits := testLimits()
	limits.MaxSentences = 2
	empty := replyReader{r: bytes.NewReader([]byte{0, 0, 0}), limits: limits}
	for i := 0; i < 2; i++ {
		if result, e := empty.sentence(); e != nil || len(result) != 0 {
			t.Fatal("empty sentence rejected before bound")
		}
	}
	if _, e = empty.sentence(); !errors.Is(e, errLimit) {
		t.Fatal("empty sentence flood unbounded")
	}
	if _, e = decodeLength(bytes.NewReader(nil)); !errors.Is(e, io.EOF) {
		t.Fatal("EOF was not reported")
	}
}
