package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fixturePassword = "fixture-login-secret=never-log"

type mockPeer struct {
	conn   net.Conn
	reader replyReader
}

func (p *mockPeer) read() ([]string, error) { return p.reader.sentence() }
func (p *mockPeer) send(words ...string) error {
	raw, e := encodeSentence(words, testLimits())
	if e != nil {
		return e
	}
	_, e = p.conn.Write(raw)
	return e
}
func requestTag(words []string) string {
	for _, word := range words {
		if strings.HasPrefix(word, ".tag=") {
			return strings.TrimPrefix(word, ".tag=")
		}
	}
	return ""
}
func requestValue(words []string, name string) string {
	prefix := "=" + name + "="
	for _, word := range words {
		if strings.HasPrefix(word, prefix) {
			return strings.TrimPrefix(word, prefix)
		}
	}
	return ""
}
func (p *mockPeer) response(kind, tag string, values Record) error {
	words := []string{kind}
	if tag != "" {
		words = append(words, ".tag="+tag)
	}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		words = append(words, "="+key+"="+values[key])
	}
	return p.send(words...)
}

type fixture struct {
	config  Config
	connect func(context.Context, string) (net.Conn, error)
	calls   *atomic.Int32
	done    <-chan error
}

func mockTLS(t *testing.T, handler func(*mockPeer) error) fixture {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "router.test"}, DNSNames: []string{"router.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	encoded, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certificate, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	left, right := net.Pipe()
	done := make(chan error, 1)
	calls := &atomic.Int32{}
	go func() {
		defer right.Close()
		secure := tls.Server(right, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if e := secure.HandshakeContext(ctx); e != nil {
			done <- e
			return
		}
		peer := &mockPeer{conn: secure, reader: replyReader{r: secure, limits: testLimits()}}
		done <- handler(peer)
	}()
	t.Cleanup(func() {
		left.Close()
		right.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("local TLS fixture did not terminate")
		}
	})
	return fixture{config: Config{Address: "router.test:8729", ServerName: "router.test", Username: "fixture-user", Password: []byte(fixturePassword), RootCAs: pool, SPKISHA256: hex.EncodeToString(sum[:]), DialTimeout: time.Second, CommandTimeout: time.Second}, connect: func(context.Context, string) (net.Conn, error) { calls.Add(1); return left, nil }, calls: calls, done: done}
}
func loginOK(p *mockPeer) error {
	words, e := p.read()
	if e != nil {
		return e
	}
	if len(words) != 3 || words[0] != "/login" || requestValue(words, "name") != "fixture-user" || requestValue(words, "password") != fixturePassword || requestTag(words) != "" {
		return errors.New("modern TLS login shape mismatch")
	}
	return p.send("!done")
}
func dialFixture(t *testing.T, f fixture) *Client {
	t.Helper()
	c, e := dial(context.Background(), f.config, f.connect)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func TestTLSCertificateAndSPKIAreBothRequired(t *testing.T) {
	for _, test := range []string{"valid", "wrong-name", "wrong-pin", "pin-without-trusted-ca"} {
		t.Run(test, func(t *testing.T) {
			var login atomic.Bool
			f := mockTLS(t, func(p *mockPeer) error { login.Store(true); return loginOK(p) })
			switch test {
			case "wrong-name":
				f.config.ServerName = "wrong.test"
			case "wrong-pin":
				f.config.SPKISHA256 = strings.Repeat("0", 64)
			case "pin-without-trusted-ca":
				f.config.RootCAs = x509.NewCertPool()
			}
			c, e := dial(context.Background(), f.config, f.connect)
			if test == "valid" {
				if e != nil {
					t.Fatal(e)
				}
				c.Close()
			} else {
				var failure *Error
				if e == nil || !errors.As(e, &failure) || failure.Code != CodeTLS && failure.Code != CodeCancelled {
					t.Fatal("certificate/pin failure did not fail closed", e)
				}
				if strings.Contains(fmt.Sprintf("%+v", e), fixturePassword) {
					t.Fatal("TLS error leaked credentials")
				}
				if c != nil {
					t.Fatal("unverified connection returned a client")
				}
				if login.Load() {
					t.Fatal("unverified TLS peer reached the credential exchange")
				}
			}
		})
	}
}
func TestEmptyThenDoneDoesNotDesynchronizeNextTaggedRead(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		first, e := p.read()
		if e != nil {
			return e
		}
		if first[0] != "/routing/table/print" || requestValue(first, ".proplist") == "" {
			return errors.New("read dictionary omitted proplist")
		}
		tag := requestTag(first)
		if e = p.response("!empty", tag, nil); e != nil {
			return e
		}
		if e = p.response("!done", tag, nil); e != nil {
			return e
		}
		second, e := p.read()
		if e != nil {
			return e
		}
		if requestTag(second) == tag {
			return errors.New("request tags were reused")
		}
		if e = p.response("!re", requestTag(second), Record{"name": "test identity", "password": fixturePassword}); e != nil {
			return e
		}
		return p.response("!done", requestTag(second), nil)
	})
	client := dialFixture(t, f)
	rows, e := client.Read(context.Background(), RoutingTable)
	if e != nil || len(rows) != 0 {
		t.Fatal("!empty did not produce a completed empty read", e)
	}
	rows, e = client.Read(context.Background(), Identity)
	if e != nil || len(rows) != 1 || rows[0]["name"] != "test identity" {
		t.Fatal("!done remained in stream", e)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), fixturePassword) || len(rows[0]) != 1 {
		t.Fatal("unrequested sensitive property escaped fixed read projection")
	}
}
func TestTrapFollowedByDoneIsFailureAndDoesNotLeakRemoteMessage(t *testing.T) {
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		words, e := p.read()
		if e != nil {
			return e
		}
		if e = p.response("!trap", requestTag(words), Record{"category": "1", "message": "bad argument contains " + fixturePassword}); e != nil {
			return e
		}
		if e = p.response("!done", requestTag(words), nil); e != nil {
			return e
		}
		words, e = p.read()
		if e != nil {
			return e
		}
		if e = p.response("!re", requestTag(words), Record{"name": "after-failure"}); e != nil {
			return e
		}
		return p.response("!done", requestTag(words), nil)
	})
	client := dialFixture(t, f)
	_, e := client.Apply(context.Background(), Mutation{Resource: RoutingTable, Action: Add, Attributes: []Attribute{{Name: "name", Value: "md_fixture"}}})
	var failure *Error
	if !errors.As(e, &failure) || failure.Code != CodeTrap || !IsUncertain(e) {
		t.Fatal("write trap was converted into success or certain no-effect", e)
	}
	if strings.Contains(fmt.Sprintf("%#v", e), fixturePassword) || strings.Contains(e.Error(), fixturePassword) {
		t.Fatal("remote message leaked into error")
	}
	rows, e := client.Read(context.Background(), Identity)
	if e != nil || len(rows) != 1 {
		t.Fatal("trap/done was not fully consumed", e)
	}
}
func TestLoginTrapAndLegacyChallengeNeverBecomeAuthenticated(t *testing.T) {
	for _, challenge := range []bool{false, true} {
		t.Run(fmt.Sprint(challenge), func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				_, e := p.read()
				if e != nil {
					return e
				}
				if challenge {
					return p.send("!done", "=ret=legacy-challenge")
				}
				if e = p.send("!trap", "=message="+fixturePassword); e != nil {
					return e
				}
				return p.send("!done")
			})
			client, e := dial(context.Background(), f.config, f.connect)
			var failure *Error
			if client != nil || !errors.As(e, &failure) || failure.Code != CodeAuthentication || strings.Contains(e.Error(), fixturePassword) {
				t.Fatal("failed login authenticated or leaked credentials", e)
			}
		})
	}
}
func TestWrongTagsFatalAndOversizedWordsDiscardPartialReads(t *testing.T) {
	for _, mode := range []string{"tag", "fatal", "oversized", "records"} {
		t.Run(mode, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				words, e := p.read()
				if e != nil {
					return e
				}
				tag := requestTag(words)
				switch mode {
				case "tag":
					return p.response("!done", "some-other-command", nil)
				case "fatal":
					return p.send("!fatal", "=message="+fixturePassword)
				case "oversized":
					_, e = p.conn.Write([]byte{0xf0, 0xff, 0xff, 0xff, 0xff})
					return e
				case "records":
					for i := 0; i < 2; i++ {
						if e = p.response("!re", tag, Record{"name": "row"}); e != nil {
							return e
						}
					}
					return p.response("!done", tag, nil)
				}
				return nil
			})
			if mode == "records" {
				f.config.Limits.MaxRecords = 1
			}
			client := dialFixture(t, f)
			rows, e := client.Read(context.Background(), Identity)
			if e == nil || rows != nil || !client.closed.Load() {
				t.Fatal("malformed/incomplete read was trusted", mode, rows, e)
			}
			if strings.Contains(fmt.Sprint(e), fixturePassword) {
				t.Fatal("fatal response leaked password")
			}
		})
	}
}

type failingWriteConn struct {
	net.Conn
	calls             atomic.Int32
	shortWithoutError bool
}

func (c *failingWriteConn) Write(data []byte) (int, error) {
	c.calls.Add(1)
	n, e := c.Conn.Write(data[:3])
	if e != nil {
		return n, e
	}
	if c.shortWithoutError {
		return n, nil
	}
	return n, errors.New("unsafe transport text " + fixturePassword)
}
func TestPartialWriteIsUncertainClosedAndNeverRetried(t *testing.T) {
	for _, shortWithoutError := range []bool{false, true} {
		t.Run(fmt.Sprint(shortWithoutError), func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				_, e := p.read()
				return e
			})
			client := dialFixture(t, f)
			wrapped := &failingWriteConn{Conn: client.conn, shortWithoutError: shortWithoutError}
			client.conn = wrapped
			mutation := Mutation{Resource: BGPConnection, Action: Disable, ID: "*1"}
			_, e := client.Apply(context.Background(), mutation)
			if !IsUncertain(e) || !client.closed.Load() || wrapped.calls.Load() != 1 || f.calls.Load() != 1 || strings.Contains(fmt.Sprint(e), fixturePassword) {
				t.Fatal("partial write retry/uncertainty/redaction failed", e)
			}
			if _, e = client.Apply(context.Background(), mutation); e == nil || wrapped.calls.Load() != 1 {
				t.Fatal("closed client retried mutation")
			}
		})
	}
}
func TestCancellationAfterSubmissionIsUncertain(t *testing.T) {
	received := make(chan struct{})
	f := mockTLS(t, func(p *mockPeer) error {
		if e := loginOK(p); e != nil {
			return e
		}
		_, e := p.read()
		if e != nil {
			return e
		}
		close(received)
		var b [1]byte
		_, e = p.conn.Read(b[:])
		return e
	})
	client := dialFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, e := client.Apply(ctx, Mutation{Resource: BGPConnection, Action: Disable, ID: "*1"})
		result <- e
	}()
	<-received
	cancel()
	select {
	case e := <-result:
		if !IsUncertain(e) || !client.closed.Load() {
			t.Fatal("submitted cancellation was reported as safe to retry", e)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt transport")
	}
}
func TestSensitiveGetterKeepsUnknownDistinctFromVerifiedEmpty(t *testing.T) {
	for _, value := range []string{"private-bgp-key", "", "********", "missing"} {
		t.Run(value, func(t *testing.T) {
			f := mockTLS(t, func(p *mockPeer) error {
				if e := loginOK(p); e != nil {
					return e
				}
				words, e := p.read()
				if e != nil {
					return e
				}
				if words[0] != "/routing/bgp/connection/get" || requestValue(words, ".id") != "*A" || requestValue(words, "value-name") != "tcp-md5-key" {
					return errors.New("sensitive getter escaped fixed path/property")
				}
				values := Record{}
				if value != "missing" {
					values["ret"] = value
				}
				return p.response("!done", requestTag(words), values)
			})
			client := dialFixture(t, f)
			credential, e := client.ReadCredential(context.Background(), "*A")
			if e != nil {
				t.Fatal(e)
			}
			known := value != "missing" && value != "********"
			if credential.Known != known {
				t.Fatal("missing/masked value became verified")
			}
			if known && !credential.Matches([]byte(value)) {
				t.Fatal("known credential equality failed")
			}
			raw, _ := json.Marshal(credential)
			formatted := fmt.Sprintf("%#v %v", credential, credential.Value)
			if value == "private-bgp-key" && (strings.Contains(string(raw), value) || strings.Contains(formatted, value)) {
				t.Fatal("credential escaped JSON/formatter")
			}
			if !known && credential.Matches(nil) {
				t.Fatal("unknown credential verified absent authentication")
			}
			copy := credential.Value.CopyBytes()
			if len(copy) > 0 {
				copy[0] ^= 0xff
				if credential.Matches(copy) {
					t.Fatal("credential copy aliases internal bytes")
				}
			}
		})
	}
}
func TestCredentialAttributesForceRedactionByPropertyName(t *testing.T) {
	attribute := Attribute{Name: "tcp-md5-key", Value: fixturePassword, Sensitive: false}
	raw, e := json.Marshal(Mutation{Resource: BGPConnection, Action: Set, ID: "*1", Attributes: []Attribute{attribute}})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), fixturePassword) || strings.Contains(fmt.Sprintf("%#v", attribute), fixturePassword) || strings.Contains(fmt.Sprintf("%#v", Config{Username: "fixture-user", Password: []byte(fixturePassword)}), fixturePassword) {
		t.Fatal("caller-controlled Sensitive=false bypassed redaction")
	}
}
