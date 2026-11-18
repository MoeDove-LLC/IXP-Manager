package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMailingCommandsRequireReviewAndKeepLegacyDispatch(t *testing.T) {
	for _, args := range [][]string{{"mailing-plan"}, {"mailing-apply", "--config", "missing", "--plan", "missing", "--journal", "missing"}, {"mailing-reconcile", "--config", "missing", "--plan", "missing", "--journal", "missing"}, {"mailing-plan", "--config", "missing", "--out", "new", "--exclusive-access-acknowledged"}, {"mailing-status", "--config", "missing", "--plan", "missing", "--journal", "missing", "--checksum", "bad"}, {"mailing-other"}} {
		var out bytes.Buffer
		if e := run(args, strings.NewReader(""), &out); e == nil || out.Len() != 0 {
			t.Fatal("invalid mailing invocation accepted", args)
		}
	}
	if e := run(nil, strings.NewReader(""), &bytes.Buffer{}); e == nil || !strings.Contains(e.Error(), "--provider-config") {
		t.Fatal("legacy ixp-sync dispatch changed", e)
	}
}
