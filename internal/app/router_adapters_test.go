package app

import (
	"os"
	"strings"
	"testing"
)

func TestBIRD3AndOpenBGPDControllerContracts(t *testing.T) {
	for _, platform := range []string{"bird3", "openbgpd"} {
		t.Run(platform, func(t *testing.T) {
			c, _, peer := credentialFixture(t)
			routerID := str(peer, "routerId")
			router := c.ok("GET", "/api/resources/routers/"+routerID, nil)
			edit := editableCredentialPeer(router)
			delete(edit, "_etag")
			edit["platform"] = platform
			edit["rfc1997Passthrough"] = true
			c.ok("PUT", "/api/resources/routers/"+routerID, edit)
			revision := c.ok("POST", "/api/deployments", Record{"routerId": routerID})
			if revision["platform"] != platform || revision["rfc1997Passthrough"] != true || revision["rfc1997Mode"] != "explicit" {
				t.Fatal("explicit platform/community choice not frozen")
			}
			path := "/api/deployments/" + str(revision, "id")
			preview := c.ok("GET", path, nil)
			if preview["redacted"] != true || str(preview, "config") == "" {
				t.Fatal("new adapter lacks safe preview")
			}
			if platform == "bird3" && !strings.Contains(str(preview, "config"), "interpret communities off;") {
				t.Fatal("BIRD3 ignored explicit RFC1997 passthrough")
			}
			binary := os.Getenv("BIRD3_BIN")
			if platform == "openbgpd" {
				binary = os.Getenv("OPENBGPD_BIN")
				c.a.openBGPDBinary = "/not-installed/openbgpd"
			} else {
				c.a.bird3Binary = "/not-installed/bird3"
			}
			caps := c.ok("GET", "/api/router-platforms", nil)["items"].([]any)
			for _, raw := range caps {
				cap := raw.(map[string]any)
				if cap["platform"] == platform && (cap["fetch"] != true || cap["nativeCheckerAvailable"] != false || cap["rfc1997Control"] != true) {
					t.Fatal("missing native checker falsely advertised")
				}
			}
			if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
				t.Fatal("new adapter used another platform's checker")
			}
			if binary != "" {
				if platform == "bird3" {
					c.a.bird3Binary = binary
				} else {
					c.a.openBGPDBinary = binary
				}
				c.ok("POST", path+"/validate", nil)
				c.ok("POST", path+"/approve", Record{"checksum": revision["checksum"]})
				file := c.request("GET", path+"/config", nil)
				if file.Code != 200 || file.Header().Get("X-Config-Platform") != platform || file.Header().Get("X-Config-SHA256") != digest(file.Body.String()) {
					t.Fatal("new adapter download lost platform or byte binding")
				}
			}
			edit["rfc1997Passthrough"] = false
			c.ok("PUT", "/api/resources/routers/"+routerID, edit)
			old := c.ok("GET", path, nil)
			if old["rfc1997Passthrough"] != true {
				t.Fatal("historical community metadata followed a mutable router")
			}
			changed := c.ok("POST", "/api/deployments", Record{"routerId": routerID})
			if changed["checksum"] == revision["checksum"] || changed["rfc1997Passthrough"] != false {
				t.Fatal("community policy change invisible to review")
			}
			changedPreview := c.ok("GET", "/api/deployments/"+str(changed, "id"), nil)
			if platform == "openbgpd" {
				for _, community := range []string{"NO_EXPORT", "NO_ADVERTISE", "NO_EXPORT_SUBCONFED"} {
					if !strings.Contains(str(changedPreview, "config"), "community "+community) || strings.Contains(str(preview, "config"), "community "+community) {
						t.Fatal("OpenBGPD explicit community restriction was only metadata, or passthrough was implicitly restricted", community)
					}
				}
			}
			edit["platform"] = "frr"
			if w := c.request("PUT", "/api/resources/routers/"+routerID, edit); w.Code != 400 {
				t.Fatal("unverified vendor accepted explicit RFC1997 control")
			}
			delete(edit, "rfc1997Passthrough")
			c.ok("PUT", "/api/resources/routers/"+routerID, edit)
		})
	}
}

func TestBIRD3RPKIExpiryAndOpenBGPDRejection(t *testing.T) {
	c, _, peer := credentialFixture(t)
	id := str(peer, "routerId")
	router := c.ok("GET", "/api/resources/routers/"+id, nil)
	edit := editableCredentialPeer(router)
	delete(edit, "_etag")
	edit["platform"] = "bird3"
	edit["rpkiPolicy"] = "require-valid"
	edit["rpkiHost"] = "192.0.2.200"
	edit["rpkiExpireSeconds"] = 60
	if w := c.request("PUT", "/api/resources/routers/"+id, edit); w.Code != 400 {
		t.Fatal("BIRD3 accepted unimplemented low RPKI expiry")
	}
	edit["rpkiExpireSeconds"] = 600
	c.ok("PUT", "/api/resources/routers/"+id, edit)
	edit["platform"] = "openbgpd"
	if w := c.request("PUT", "/api/resources/routers/"+id, edit); w.Code != 400 {
		t.Fatal("OpenBGPD silently discarded required RPKI")
	}
}
