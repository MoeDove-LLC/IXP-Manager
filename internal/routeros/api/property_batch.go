package api

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ReadProperties pipelines only a bounded set of fixed non-sensitive getters.
// Each tag must complete exactly once. A missing ret remains Known=false.
// Any failure discards the whole batch and closes the channel; no item is retried.
func (c *Client) ReadProperties(parent context.Context, requests []PropertyRequest) (values []PropertyValue, resultErr error) {
	if parent == nil || c == nil || len(requests) == 0 || len(requests) > MaxPropertyBatch {
		return nil, &Error{Code: CodeConfig}
	}
	seen := map[string]bool{}
	for _, q := range requests {
		d, ok := dictionaries[q.Resource]
		if !ok || q.Resource != RoutingTable && q.Resource != BGPInstance && q.Resource != BGPConnection && q.Resource != FilterRule || !objectID.MatchString(q.ID) || q.Property == ".id" || sensitiveName(q.Property) || !has(d.properties, q.Property) {
			return nil, &Error{Code: CodeConfig}
		}
		key := string(q.Resource) + "/" + strings.ToUpper(q.ID) + "/" + q.Property
		if seen[key] {
			return nil, &Error{Code: CodeConfig}
		}
		seen[key] = true
	}
	if c.conn == nil || c.raw == nil || c.gate == nil || c.done == nil {
		return nil, &Error{Code: CodeClosed}
	}
	kind := "command"
	if d, ok := parent.Deadline(); ok && !d.After(time.Now().Add(c.timeout)) {
		kind = "context"
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, &Error{Code: CodeCancelled, Deadline: errors.Is(ctx.Err(), context.DeadlineExceeded), DeadlineKind: kind}
	case <-c.done:
		return nil, &Error{Code: CodeClosed}
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if c.closed.Load() {
		return nil, &Error{Code: CodeClosed}
	}
	if ctx.Err() != nil {
		return nil, &Error{Code: CodeCancelled, Deadline: errors.Is(ctx.Err(), context.DeadlineExceeded), DeadlineKind: kind}
	}
	if uint64(len(requests)) > ^uint64(0)-c.sequence {
		c.Close()
		return nil, &Error{Code: CodeClosed}
	}
	payload := []byte{}
	tags := map[string]int{}
	for i, q := range requests {
		c.sequence++
		tag := "ixp-" + strconv.FormatUint(c.sequence, 10)
		tags[tag] = i
		words := []string{dictionaries[q.Resource].path + "/get", "=.id=" + q.ID, "=value-name=" + q.Property, ".tag=" + tag}
		frame, err := encodeSentence(words, c.limits)
		if err != nil {
			return nil, &Error{Code: CodeLimit}
		}
		if len(payload)+len(frame) > 64<<10 {
			return nil, &Error{Code: CodeLimit}
		}
		payload = append(payload, frame...)
	}
	deadline, _ := ctx.Deadline()
	if c.conn.SetDeadline(deadline) != nil {
		c.Close()
		clear(payload)
		return nil, &Error{Code: CodeTransport}
	}
	watchDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(watchDone) })
	defer func() {
		if !stop() {
			<-watchDone
		}
	}()
	writer := make(chan error, 1)
	go func() {
		defer clear(payload)
		for remaining := payload; len(remaining) > 0; {
			n, err := c.conn.Write(remaining)
			if err != nil || n <= 0 || n > len(remaining) {
				if err == nil {
					err = io.ErrShortWrite
				}
				writer <- err
				return
			}
			remaining = remaining[n:]
		}
		writer <- nil
	}()
	writeWaited := false
	defer func() {
		if resultErr != nil {
			c.Close()
		}
		if !writeWaited {
			<-writer
		}
	}()
	reader := replyReader{r: c.conn, limits: c.limits}
	// Getter replies are one completion and at most one !empty per item.
	if maximum := 2*len(requests) + 16; reader.limits.MaxSentences > maximum {
		reader.limits.MaxSentences = maximum
	}
	values = make([]PropertyValue, len(requests))
	completed := make([]bool, len(requests))
	empty := make([]bool, len(requests))
	done := 0
	fail := func(code ErrorCode, index int) error {
		e := &Error{Code: code, ReplyBytes: reader.bytes, ReplyRecords: done, ReplySentences: reader.sentences, LimitKind: reader.limitKind}
		if index >= 0 && index < len(requests) {
			e.Resource = requests[index].Resource
			e.Property = requests[index].Property
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(deadline) {
			e.Deadline = true
			e.DeadlineKind = kind
		} else {
			e.DeadlineKind = "none"
		}
		return e
	}
	for done < len(requests) {
		words, err := reader.sentence()
		if err != nil {
			code := CodeTransport
			if errors.Is(err, errProtocol) {
				code = CodeProtocol
			} else if errors.Is(err, errLimit) {
				code = CodeLimit
			} else if ctx.Err() != nil {
				code = CodeCancelled
			}
			return nil, fail(code, -1)
		}
		if len(words) == 0 {
			continue
		}
		reply, err := parseReply(words)
		if err != nil {
			return nil, fail(CodeProtocol, -1)
		}
		if reply.kind == "!fatal" {
			return nil, fail(CodeFatal, -1)
		}
		index, ok := tags[reply.tag]
		if !ok || completed[index] {
			return nil, fail(CodeProtocol, -1)
		}
		switch reply.kind {
		case "!empty":
			if empty[index] || len(reply.values) != 0 {
				return nil, fail(CodeProtocol, index)
			}
			empty[index] = true
		case "!trap":
			return nil, fail(CodeTrap, index)
		case "!done":
			if len(reply.values) > 1 {
				return nil, fail(CodeProtocol, index)
			}
			value, known := reply.values["ret"]
			if len(reply.values) == 1 && !known || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return nil, fail(CodeProtocol, index)
			}
			values[index] = PropertyValue{Known: known, Value: value}
			completed[index] = true
			done++
		default:
			return nil, fail(CodeProtocol, index)
		}
	}
	if err := <-writer; err != nil {
		writeWaited = true
		return nil, fail(CodeTransport, -1)
	}
	writeWaited = true
	return values, nil
}
