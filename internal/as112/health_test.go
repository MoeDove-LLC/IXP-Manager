package as112

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestAS112HealthLongTXTUsesEDNSAndStillRejectsFalseAnswers(t *testing.T) {
	identity := identityFixture()
	identity.Facility = strings.Repeat("F", 200)
	identity.Location = strings.Repeat("L", 200)
	b, err := Render("health-fixture", Service{Both, true, true}, identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid-long-text", "recursion", "not-authoritative", "wrong-identity", "wrong-question"} {
		t.Run(mode, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan bool, 1)
			go func() {
				defer server.Close()
				buffer := make([]byte, 8192)
				n, e := server.Read(buffer)
				if e != nil {
					done <- false
					return
				}
				var q dnsmessage.Message
				if q.Unpack(buffer[:n]) != nil {
					done <- false
					return
				}
				edns := len(q.Additionals) == 1 && q.Additionals[0].Header.Type == dnsmessage.TypeOPT && q.Additionals[0].Header.Class == dnsmessage.Class(1232)
				response := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true}, Questions: q.Questions}
				texts := [][]string{{b.Identity.Facility, b.Identity.Location}, {"See https://www.as112.net/ for more information."}, {"Unique IP: " + b.Identity.UniqueIP}}
				for _, text := range texts {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 604800}, Body: &dnsmessage.TXTResource{TXT: text}})
				}
				switch mode {
				case "recursion":
					response.RecursionAvailable = true
				case "not-authoritative":
					response.Authoritative = false
				case "wrong-identity":
					response.Answers[0].Body = &dnsmessage.TXTResource{TXT: []string{"another node"}}
				case "wrong-question":
					response.Questions[0].Type = dnsmessage.TypeA
				}
				wire, e := response.Pack()
				if e != nil {
					done <- false
					return
				}
				if !edns && len(wire) > 512 {
					response.Truncated = true
					response.Answers = nil
					wire, _ = response.Pack()
				}
				_, e = server.Write(wire)
				done <- edns && e == nil
			}()
			runtime := healthRuntime{dial: func(_ context.Context, network, target string) (net.Conn, error) {
				if network != "udp" || target != "192.175.48.1:53" {
					t.Fatal("probe escaped its fixed target")
				}
				return client, nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := healthQuery(ctx, b, runtime, "192.175.48.1", "udp", "hostname.as112.net.", HealthCheck{Type: "TXT", RCode: "NOERROR"})
			if (err == nil) != (mode == "valid-long-text") {
				t.Fatal("unexpected health result", mode, err)
			}
			if !<-done {
				t.Fatal("probe did not advertise bounded EDNS0")
			}
		})
	}
}

func TestAS112BoundDualStackNodeRequiresBothBackendAddressesBeforeQuery(t *testing.T) {
	identity := identityFixture()
	b, err := RenderNode("router", "2026-01-01T00:00:00Z", Service{Both, true, true}, identity, []string{identity.UniqueIP, "2001:db8::112"})
	if err != nil {
		t.Fatal(err)
	}
	local := []net.Addr{}
	for _, raw := range append(append([]string{}, b.ListenAddresses...), identity.UniqueIP) {
		suffix := "/128"
		if !strings.Contains(raw, ":") {
			suffix = "/32"
		}
		_, address, e := net.ParseCIDR(raw + suffix)
		if e != nil {
			t.Fatal(e)
		}
		local = append(local, address)
	}
	dialled := false
	runtime := healthRuntime{addresses: func() ([]net.Addr, error) { return local, nil }, dial: func(context.Context, string, string) (net.Conn, error) {
		dialled = true
		return nil, errors.New("must not dial")
	}}
	result, err := probe(context.Background(), b, runtime)
	if err == nil || dialled || result.Queries != 0 || result.LocalAddressesPresent || result.Healthy {
		t.Fatal("one-node health covered a missing second-family backend")
	}
	bad := b
	bad.RequiredLocalAddresses = append([]string{}, b.RequiredLocalAddresses[:len(b.RequiredLocalAddresses)-1]...)
	if Validate(bad) == nil {
		t.Fatal("required backend address dropped from canonical manifest")
	}
	if _, err := RenderNode("router", "2026-01-01T00:00:00Z", Service{Both, true, true}, identity, []string{identity.UniqueIP}); err == nil {
		t.Fatal("bound dual-stack router omitted one backend family")
	}
	if _, err := RenderNode("router", "2026-01-01T00:00:00Z", Service{Both, true, false}, identity, []string{identity.UniqueIP, "2001:db8::112"}); err == nil {
		t.Fatal("bound single-stack router accepted an unselected backend family")
	}
}
