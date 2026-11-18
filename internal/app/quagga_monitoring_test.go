package app

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/telemetry"
)

func TestQuaggaMonitoringBindsTheLocalAndRemoteIdentities(t *testing.T) {
	f := monitoringFixture(t)
	id, peerID := str(f.router, "id"), str(f.peer, "id")
	bindingsPath := "/api/monitoring/bindings?routerId=" + url.QueryEscape(id)
	if w := bearerRequest(f.c.a, f.token, "GET", bindingsPath+"&routerIdentity=quagga", nil); w.Code != 422 {
		t.Fatal("another platform was presented as a Quagga BGP identity")
	}
	change := Record{"name": "Quagga route server", "platform": "quagga", "routerId": "192.0.2.1", "asn": 64500, "localIpv4": "192.0.2.1", "vlanId": f.router["vlanId"]}
	f.c.ok("PUT", "/api/resources/routers/"+id, change)
	if w := bearerRequest(f.c.a, f.token, "GET", bindingsPath, nil); w.Code != 200 || strings.Contains(w.Body.String(), "routerIdentity") {
		t.Fatal("legacy collector's bindings response changed shape")
	}
	if w := bearerRequest(f.c.a, f.token, "GET", "/api/monitoring/bindings?routerId="+url.QueryEscape(str(f.otherRouter, "id"))+"&routerIdentity=quagga", nil); w.Code != 403 {
		t.Fatal("collector token obtained another router's identity")
	}
	current := bearerRequest(f.c.a, f.token, "GET", bindingsPath+"&routerIdentity=quagga", nil)
	var bindings telemetry.Bindings
	if current.Code != 200 || json.Unmarshal(current.Body.Bytes(), &bindings) != nil || bindings.RouterIdentity == nil ||
		*bindings.RouterIdentity != (telemetry.RouterIdentity{Platform: "quagga", RouterID: "192.0.2.1", ASN: 64500}) || len(bindings.Sessions) != 1 {
		t.Fatal("scope did not bind the configured local BGP identity", current.Code, current.Body.String())
	}
	batch := f.batch(time.Now().UTC(), "10")
	delete(batch, "ports")
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 409 {
		t.Fatal("identity-less Quagga sample accepted")
	}
	wrong := *bindings.RouterIdentity
	wrong.ASN++
	batch["routerIdentity"] = wrong
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 409 {
		t.Fatal("Quagga sample accepted from a different local ASN")
	}
	batch["routerIdentity"] = bindings.RouterIdentity
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 200 || !strings.Contains(w.Body.String(), `"acceptedSessions":1`) {
		t.Fatal("bound Quagga observation did not reach ordinary monitoring", w.Code, w.Body.String())
	}
	if row := monitorSession(t, f.c, peerID); row["state"] != "Established" || row["receivedPrefixes"] != float64(10) {
		t.Fatal("validated session was not shown", row)
	}
	change["routerId"] = "192.0.2.99"
	f.c.ok("PUT", "/api/resources/routers/"+id, change)
	if row := monitorSession(t, f.c, peerID); row["state"] != "unknown" || row["receivedPrefixes"] != nil {
		t.Fatal("previous identity's recorded sample remained visible as the current session", row)
	}
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 409 {
		t.Fatal("pre-change bound sample could overwrite the new router identity")
	}
	change["platform"] = "bird2"
	f.c.ok("PUT", "/api/resources/routers/"+id, change)
	if w := bearerRequest(f.c.a, f.token, "POST", "/api/monitoring/ingest", batch); w.Code != 409 {
		t.Fatal("Quagga evidence survived a router platform change")
	}
}
