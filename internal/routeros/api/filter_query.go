package api

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// ReadFilterRuleDisabled selects exactly one fixed boolean predicate. Returned
// IDs are native query evidence, not an inference from a missing print flag.
// Callers must prove completeness against their separately read inventory.
func (c *Client) ReadFilterRuleDisabled(ctx context.Context, disabled bool) ([]string, error) {
	words := []string{"/routing/filter/rule/print", "=.proplist=.id,disabled"}
	if disabled {
		words = append(words, "?disabled=true")
	} else {
		// On the measured 7.24.6 profile, false includes both an explicit
		// false value and the absent flag. Query order is significant.
		words = append(words, "?disabled=false", "?-disabled", "?#|")
	}
	reply, err := c.command(ctx, words, false, false)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			copy := *e
			copy.Resource = FilterRule
			return nil, &copy
		}
		return nil, err
	}
	fail := func() ([]string, error) { c.Close(); return nil, &Error{Code: CodeProtocol, Resource: FilterRule} }
	if len(reply.done) != 0 {
		return fail()
	}
	ids := make([]string, 0, len(reply.rows))
	seen := map[string]bool{}
	for _, row := range reply.rows {
		id := strings.ToUpper(row[".id"])
		if !objectID.MatchString(id) || seen[id] {
			return fail()
		}
		if raw, exists := row["disabled"]; exists {
			actual, known := boolValue(raw)
			if !known || actual != disabled {
				return fail()
			}
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
