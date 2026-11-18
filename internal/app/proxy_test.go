package app

import (
	"net/http/httptest"
	"testing"
)

func TestProxyIPDoesNotTrustClientSpoofing(t *testing.T) {
	proxies, err := parseTrustedProxies([]string{"127.0.0.1", "10.0.0.2/32"})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{trustedProxies: proxies}
	tests := []struct{ remote, forwarded, want string }{{"203.0.113.10:443", "1.1.1.1", "203.0.113.10"}, {"127.0.0.1:1234", "1.1.1.1, 203.0.113.10", "203.0.113.10"}, {"127.0.0.1:1234", "203.0.113.10, 10.0.0.2", "203.0.113.10"}, {"127.0.0.1:1234", "203.0.113.10, malformed", "127.0.0.1"}, {"127.0.0.1:1234", "2001:db8::10", "2001:db8::10"}}
	for _, tc := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := a.clientIP(r); got != tc.want {
			t.Fatalf("%s -> %s expected %s", tc.forwarded, got, tc.want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Add("X-Forwarded-For", "1.1.1.1")
	r.Header.Add("X-Forwarded-For", "203.0.113.12")
	if a.clientIP(r) != "203.0.113.12" {
		t.Fatal("multiple headers spoofed IP")
	}
	if _, err := parseTrustedProxies([]string{"0.0.0.0/0"}); err == nil {
		t.Fatal("universal proxy trust allowed")
	}
}
