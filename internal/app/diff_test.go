package app

import (
	"strings"
	"testing"
)

func TestReviewDiffReconstructsArtifacts(t *testing.T) {
	for _, pair := range [][2]string{{"", "a\nb\n"}, {"a\nb\nc\n", "a\nchanged\nc\n"}, {"one\ntwo\n", "two\none\n"}, {"x\n", ""}} {
		d := configDiff("base", pair[0], pair[1])
		var old, new []string
		for _, line := range d.Changes {
			if line.Type != "add" {
				old = append(old, line.Text)
			}
			if line.Type != "remove" {
				new = append(new, line.Text)
			}
		}
		if strings.Join(old, "\n") != strings.TrimSuffix(pair[0], "\n") || strings.Join(new, "\n") != strings.TrimSuffix(pair[1], "\n") {
			t.Fatal("diff lost or fabricated lines")
		}
	}
}
func TestReviewDiffBounded(t *testing.T) {
	d := configDiff("", "", strings.Repeat("prefix\n", 21000))
	if !d.Truncated || len(d.Changes) != 20000 || d.Added != 21000 {
		t.Fatal("unbounded or misleading diff")
	}
}
