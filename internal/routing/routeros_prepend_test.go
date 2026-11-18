package routing

import (
	"strings"
	"testing"
)

func TestRouterOSPreviewZeroPrependAndHistoricalRedaction(t *testing.T) {
	router, peers := authenticatedFixture()
	config, e := RenderFor("routeros7", router, peers)
	if e != nil {
		t.Fatal(e)
	}
	connections := strings.Count(config, "/routing/bgp/connection add ")
	if connections == 0 || strings.Count(config, " output.default-prepend=0 ") != connections {
		t.Fatal("preview omitted transparent AS_PATH policy")
	}
	for _, candidate := range []string{config, strings.ReplaceAll(config, " output.default-prepend=0", "")} {
		redacted, e := RedactConfig("routeros7", candidate)
		if e != nil {
			t.Fatal(e)
		}
		for _, peer := range peers {
			if peer.Password != "" && strings.Contains(redacted, peer.Password) {
				t.Fatal("secret leaked through new/historical redaction")
			}
		}
		if !strings.Contains(redacted, RedactedBGPPassword) {
			t.Fatal("fixture missed secret redaction")
		}
	}
	for _, replacement := range []string{" output.default-prepend=1 ", " output.default-prepend=00 ", " output.default-prepend=0 output.default-prepend=0 ", " output.default-prepend=0;bad "} {
		candidate := strings.Replace(config, " output.default-prepend=0 ", replacement, 1)
		if text, e := RedactConfig("routeros7", candidate); e == nil || text != "" {
			t.Fatal("unexpected RouterOS command grammar leaked preview")
		}
	}
}
