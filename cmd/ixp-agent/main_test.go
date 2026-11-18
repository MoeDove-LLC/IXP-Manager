package main

import "testing"

func TestUnsafePlatformCommandsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"apply", "--platform", "frr"},
		{"recover", "--platform", "frr"},
		{"stage", "--platform", "bird2"},
		{"stage", "--platform", "frr"},
		{"confirm", "--platform", "frr"},
		{"apply", "--file", "unreviewed.conf"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("unsafe command accepted: %v", args)
		}
	}
}
func TestHelpDoesNotRunCommands(t *testing.T) {
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"serve", "--help"}); err != nil {
		t.Fatal(err)
	}
}
