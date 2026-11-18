package api

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var errProtocol = errors.New("invalid RouterOS protocol")
var errLimit = errors.New("RouterOS protocol bound exceeded")

func encodeLength(n uint32) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x4000:
		return []byte{byte(n>>8) | 0x80, byte(n)}
	case n < 0x200000:
		return []byte{byte(n>>16) | 0xc0, byte(n >> 8), byte(n)}
	case n < 0x10000000:
		return []byte{byte(n>>24) | 0xe0, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{0xf0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}
func decodeLength(r io.Reader) (uint32, error) {
	var first [1]byte
	if _, e := io.ReadFull(r, first[:]); e != nil {
		return 0, e
	}
	b := first[0]
	var extra int
	var n, min uint32
	switch {
	case b < 0x80:
		return uint32(b), nil
	case b < 0xc0:
		extra = 1
		n = uint32(b & 0x3f)
		min = 0x80
	case b < 0xe0:
		extra = 2
		n = uint32(b & 0x1f)
		min = 0x4000
	case b < 0xf0:
		extra = 3
		n = uint32(b & 0x0f)
		min = 0x200000
	case b == 0xf0:
		extra = 4
		min = 0x10000000
	default:
		return 0, errProtocol
	}
	var raw [4]byte
	if _, e := io.ReadFull(r, raw[:extra]); e != nil {
		return 0, e
	}
	if extra == 4 {
		n = binary.BigEndian.Uint32(raw[:])
	} else {
		for _, v := range raw[:extra] {
			n = n<<8 | uint32(v)
		}
	}
	if n < min {
		return 0, errProtocol
	}
	return n, nil
}
func encodeSentence(words []string, limits Limits) ([]byte, error) {
	if len(words) == 0 || len(words) > limits.MaxWords {
		return nil, errLimit
	}
	out := []byte{}
	for _, word := range words {
		if len(word) == 0 || len(word) > limits.MaxWordBytes {
			return nil, errLimit
		}
		header := encodeLength(uint32(len(word)))
		if len(out)+len(header)+len(word)+1 > limits.MaxSentenceBytes {
			return nil, errLimit
		}
		out = append(out, header...)
		out = append(out, word...)
	}
	return append(out, 0), nil
}

type replyReader struct {
	r                io.Reader
	limits           Limits
	bytes, sentences int
	limitKind        string
}

func (r *replyReader) Read(p []byte) (int, error) {
	if len(p) > r.limits.MaxReplyBytes-r.bytes {
		r.limitKind = "reply-bytes"
		return 0, errLimit
	}
	n, e := r.r.Read(p)
	r.bytes += n
	return n, e
}
func (r *replyReader) sentence() ([]string, error) {
	r.sentences++
	if r.sentences > r.limits.MaxSentences {
		r.limitKind = "sentences"
		return nil, errLimit
	}
	words := []string{}
	start := r.bytes
	for {
		n, e := decodeLength(r)
		if e != nil {
			return nil, e
		}
		if n == 0 {
			return words, nil
		}
		if uint64(n) > uint64(r.limits.MaxWordBytes) {
			r.limitKind = "word-bytes"
			return nil, errLimit
		}
		if int64(r.bytes-start)+int64(n) > int64(r.limits.MaxSentenceBytes) {
			r.limitKind = "sentence-bytes"
			return nil, errLimit
		}
		if len(words) >= r.limits.MaxWords {
			r.limitKind = "words"
			return nil, errLimit
		}
		data := make([]byte, int(n))
		if _, e = io.ReadFull(r, data); e != nil {
			return nil, e
		}
		words = append(words, string(data))
	}
}

type reply struct {
	kind, tag string
	values    map[string]string
}

func parseReply(words []string) (reply, error) {
	out := reply{values: map[string]string{}}
	if len(words) == 0 {
		return out, nil
	}
	out.kind = words[0]
	if out.kind == "!fatal" {
		return out, nil
	}
	switch out.kind {
	case "!re", "!done", "!trap", "!empty":
	default:
		return out, errProtocol
	}
	seenTag := false
	for _, word := range words[1:] {
		if strings.HasPrefix(word, ".tag=") {
			if seenTag {
				return out, errProtocol
			}
			seenTag = true
			out.tag = strings.TrimPrefix(word, ".tag=")
			continue
		}
		if !strings.HasPrefix(word, "=") {
			return out, errProtocol
		}
		name, value, ok := strings.Cut(word[1:], "=")
		if !ok || name == "" || len(name) > 100 {
			return out, errProtocol
		}
		if _, exists := out.values[name]; exists {
			return out, errProtocol
		}
		out.values[name] = value
	}
	return out, nil
}
