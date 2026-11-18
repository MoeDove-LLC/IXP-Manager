package terminal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadOnlyIdentifiersReturnSourceSpecificFingerprints(t *testing.T) {
	for query, command := range map[Query]string{
		LicenseSystemID:   ":put [/system/license/get system-id]",
		RouterBoardSerial: ":put [/system/routerboard/get serial-number]",
		LicenseSoftwareID: ":put [/system/license/get software-id]",
	} {
		c, f := newFixture(t, nil)
		result, err := c.Query(context.Background(), query)
		sum := sha256.Sum256([]byte("syntheticCHR7"))
		if err != nil || result.Query != query || result.ObservedLines != 1 || result.IdentifierFingerprint != hex.EncodeToString(sum[:]) {
			t.Fatal("strict identifier fingerprint was not returned", err)
		}
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "syntheticCHR7") {
			t.Fatal("raw device identifier escaped projection")
		}
		f.mu.Lock()
		commands := append([]string(nil), f.commands...)
		f.mu.Unlock()
		if len(commands) != 1 || !strings.Contains(commands[0], command) {
			t.Fatal("identity query did not use its fixed source")
		}
		if _, err = c.Query(context.Background(), Version); err != nil {
			t.Fatal("read-only query did not preserve the channel", err)
		}
	}
}

func TestIdentifierDoesNotAcceptMissingAmbiguousOrUnstructuredInventory(t *testing.T) {
	for _, payload := range []string{"", "sameID\r\nsameID", "system-id: raw-id", "two values", "id_underscore", "标识", strings.Repeat("a", 129), "validID\r\n" + fixtureSecret} {
		c, _ := newFixture(t, func(f *fixture) { f.identifier = payload })
		result, err := c.Query(context.Background(), LicenseSystemID)
		if codeOf(err) != ProtocolUnknown || result.IdentifierFingerprint != "" || !c.Snapshot().Closed {
			t.Fatal("ambiguous identifier did not fail closed")
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), fixtureSecret) || strings.Contains(err.Error(), fixtureSecret) {
			t.Fatal("identifier error disclosed target output")
		}
	}
}

func TestCHRSystemIDAcceptsOnlyBoundedOpaqueBase64Alphabet(t *testing.T) {
	for _, identifier := range []string{"a/b", "/LeadingSlash1", "+LeadingPlus1", "Ab+cD/efG12", strings.Repeat("a", 128)} {
		c, f := newFixture(t, func(f *fixture) { f.identifier = identifier })
		result, err := c.Query(context.Background(), LicenseSystemID)
		sum := sha256.Sum256([]byte(identifier))
		if err != nil || result.IdentifierFingerprint != hex.EncodeToString(sum[:]) || result.ObservedLines != 1 {
			t.Fatal("native System ID alphabet was rejected or normalized", err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), identifier) {
			t.Fatal("opaque System ID was returned as raw text")
		}
		f.mu.Lock()
		commands := append([]string(nil), f.commands...)
		f.mu.Unlock()
		if len(commands) != 1 || strings.Contains(commands[0], identifier) {
			t.Fatal("opaque System ID was interpolated into a command")
		}
	}
	for _, identifier := range []string{" id", "id ", "id\t", "id\tvalue", "\x00id", "id=", "id_", "id-", "id.", "id:", `id"`, "id'", "id;", "id\r\nextra", "id\nextra", strings.Repeat("a", 129)} {
		c, _ := newFixture(t, func(f *fixture) { f.identifier = identifier })
		result, err := c.Query(context.Background(), LicenseSystemID)
		if err == nil || result.IdentifierFingerprint != "" || !c.Snapshot().Closed {
			t.Fatal("unsafe or ambiguous System ID was accepted")
		}
	}
}

func TestOtherIdentitySourcesKeepTheirExistingStrictAlphabet(t *testing.T) {
	for _, query := range []Query{LicenseSoftwareID, RouterBoardSerial} {
		c, _ := newFixture(t, func(f *fixture) { f.identifier = "ABCD-EFGH" })
		if result, err := c.Query(context.Background(), query); err != nil || result.IdentifierFingerprint == "" {
			t.Fatal("existing hardware identifier was rejected", err)
		}
		for _, identifier := range []string{"a/b", "/LeadingSlash", "+LeadingPlus"} {
			c, _ := newFixture(t, func(f *fixture) { f.identifier = identifier })
			if result, err := c.Query(context.Background(), query); codeOf(err) != ProtocolUnknown || result.IdentifierFingerprint != "" {
				t.Fatal("CHR alphabet leaked into another identity source")
			}
		}
	}
}

func TestUnavailableIdentifierDoesNotInventIdentity(t *testing.T) {
	c, _ := newFixture(t, func(f *fixture) { f.reject = true })
	result, err := c.Query(context.Background(), RouterBoardSerial)
	if codeOf(err) != RemoteRejected || result.IdentifierFingerprint != "" || c.Snapshot().Closed {
		t.Fatal("unsupported identity was guessed or confused with transport failure")
	}
}
