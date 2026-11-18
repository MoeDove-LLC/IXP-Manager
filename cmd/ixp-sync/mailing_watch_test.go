package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWatchCommandRequiresExplicitAuthorizationAndDistinctModes(t *testing.T) {
	for _, args := range [][]string{
		{"mailing-watch-init", "--config", "absent", "--state-dir", "absent"},
		{"mailing-watch-init", "--config", "absent", "--state-dir", "absent", "--plan", "p", "--checksum", "hash", "--enable-automatic-sync", "--exclusive-access-acknowledged", "--max-changes", "0", "--interval-seconds", "5"},
		{"mailing-watch", "--config", "absent", "--state-dir", "absent", "--once", "--resume", "--checksum", "hash"},
		{"mailing-watch", "--config", "absent", "--state-dir", "absent", "--resume"},
		{"mailing-watch", "--config", "absent", "--state-dir", "absent", "--checksum", "hash"},
		{"mailing-watch", "--config", "absent", "--state-dir", "absent", "--prune-keep", "0", "--checksum", "hash"},
		{"mailing-watch", "--config", "absent", "--state-dir", "absent", "--enable-automatic-sync"},
	} {
		var out bytes.Buffer
		err := run(args, strings.NewReader(""), &out)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "private file unavailable") {
			t.Fatal("invalid authorization/modes reached config/network work", args, err, out.String())
		}
	}
}
