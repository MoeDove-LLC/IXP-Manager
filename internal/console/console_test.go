package console

import (
	"strings"
	"testing"
)

func TestOfflineHostnameNormalizationAndInvalidInputs(t *testing.T) {
	for input, want := range map[string]string{"CS1.Example.TEST.": "cs1.example.test", "BÜCHER.Example.": "xn--bcher-kva.example", "bücher.example": "xn--bcher-kva.example", "faß.example": "xn--fa-hia.example", "console": "console", "例子.测试": "xn--fsqu00a.xn--0zwm56d", "例子。测试。": "xn--fsqu00a.xn--0zwm56d"} {
		got, e := NormalizeHostname(input)
		if e != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, e)
		}
	}
	for _, input := range []string{"", " host.example", "host.example ", "host\n.example", "https://host.example", "user@host.example", "host:22", "127.0.0.1", "2001:db8::1", "127。0。0。1", "bad..example", "bad..", "例子。测试。。", "-bad.example", "bad-.example", "a_b.example", "ab\u200dcd.example", ".", strings.Repeat("a", 64) + ".test"} {
		if _, e := NormalizeHostname(input); e == nil {
			t.Fatalf("unsafe hostname accepted %q", input)
		}
	}
}
func TestConsolePortLabelsPreserveIdentifiers(t *testing.T) {
	for _, label := range []string{"01", "1", "ttyS0", "slot/2", "端口甲", strings.Repeat("端", 255)} {
		if e := ValidatePortLabel(label); e != nil {
			t.Fatalf("valid label rejected %q: %v", label, e)
		}
	}
	for _, label := range []string{"", " 01", "01 ", "01\n", "tty\x00S0", "tty\u200bS0", strings.Repeat("端", 256)} {
		if e := ValidatePortLabel(label); e == nil {
			t.Fatalf("invalid label accepted %q", label)
		}
	}
	if PortKey("01") == PortKey("1") || PortKey("TTY-A") != PortKey("tty-a") || PortKey("é") != PortKey("e\u0301") {
		t.Fatal("port comparison lost zeros, casefold or Unicode equivalence")
	}
	for _, rate := range BaudRates {
		if !ValidBaudRate(rate) {
			t.Fatal(rate)
		}
	}
	for _, rate := range []int{0, 1, 299, 12345, 230401} {
		if ValidBaudRate(rate) {
			t.Fatal("unknown rate accepted")
		}
	}
}
