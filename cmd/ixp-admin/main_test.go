package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasswordArgumentsNeverEchoSecret(t *testing.T) {
	secret := "super-private-password"
	var out bytes.Buffer
	for _, args := range [][]string{{"reset-password", "--password", secret}, {"reset-password", "--data-dir", "/missing", "--email", "admin@example.net", secret}} {
		err := run(args, strings.NewReader(""), &out)
		if err == nil {
			t.Fatal("password argument accepted")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
			t.Fatal("secret echoed")
		}
	}
}
func TestRequiresSingleBoundedStdinPassword(t *testing.T) {
	args := []string{"reset-password", "--data-dir", "/missing", "--email", "admin@example.net", "--password-stdin"}
	for _, password := range []string{"short", strings.Repeat("x", 129), "long-password-one\nlong-password-two"} {
		var out bytes.Buffer
		err := run(args, strings.NewReader(password), &out)
		if err == nil || out.Len() != 0 {
			t.Fatal("invalid stdin accepted or printed")
		}
	}
}
