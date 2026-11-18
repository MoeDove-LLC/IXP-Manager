package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCoreProbeRequiresBothBoundFreshPhysicalEndpoints(t *testing.T) {
	now := time.Now().UTC()
	port := func(id, sw string) map[string]any {
		return map[string]any{"portId": id, "switchId": sw, "status": "fresh", "source": "snmp-if-mib", "observedAt": now, "identityVerified": true, "operStatus": "up"}
	}
	left, right := port("port-a", "switch-a"), port("port-b", "switch-b")
	physical := map[string]any{"status": "up", "source": "snmp-if-mib", "observedAt": now, "identityVerified": true}
	desired := map[string]any{"enabled": true, "type": "ecmp", "enabledLinks": 1, "totalLinks": 1}
	body := map[string]any{"bundleId": "bundle", "bindingRevision": strings.Repeat("a", 22), "desired": desired, "physical": physical, "bgp": map[string]any{"status": "unsupported"}, "links": []any{map[string]any{"id": "link", "enabled": true, "sideA": left, "sideB": right}}}
	check := func(code int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		result := probeCore(raw, "bundle", 120*time.Second, now)
		if result.Code != code {
			t.Fatal(result)
		}
		if result.Code == 0 && !strings.Contains(result.Message, "not assessed") {
			t.Fatal("physical Up claimed protocol health")
		}
	}
	check(0)
	right["operStatus"] = "down"
	physical["status"] = "degraded"
	check(2)
	right["status"] = "stale"
	check(3)
	right["status"] = "fresh"
	right["observedAt"] = now.Add(-time.Hour)
	check(3)
	right["observedAt"] = now
	right["identityVerified"] = false
	check(3)
	right["identityVerified"] = true
	right["operStatus"] = "up"
	physical["status"] = "up"
	right["portId"] = "port-a"
	check(3)
	right["portId"] = "port-b"
	desired["enabledLinks"] = 0
	check(3)
	desired["enabledLinks"] = 1
	desired["enabled"] = false
	check(3)
	desired["enabled"] = true
	body["bundleId"] = "other-bundle"
	check(3)
}
