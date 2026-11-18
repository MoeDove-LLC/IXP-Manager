package app

import (
	"os"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func TestQuaggaControllerNativeValidationApprovalAndScopedDownload(t *testing.T) {
	for _, family := range []int{4, 6} {
		t.Run(string(rune('0'+family)), func(t *testing.T) {
			c, _, router, _ := bird1ControllerFixture(t, family)
			edit := editableCredentialPeer(router)
			edit["platform"] = "quagga"
			router = c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
			revision := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
			path := "/api/deployments/" + str(revision, "id")
			preview := c.ok("GET", path, nil)
			if preview["redacted"] != true || strings.Contains(str(preview, "config"), "Synthetic_BIRD1_Controller_Key") || !strings.Contains(str(preview, "config"), routing.RedactedBGPPassword) || !strings.Contains(str(preview, "config"), "managed Quagga 1.2.4") {
				t.Fatal("Quagga preview leaked a credential or used another platform")
			}
			state := c.a.store.Snapshot()
			d := *deploymentByID(&state, str(revision, "id"))
			c.a.quaggaBinary = "/not-installed/quagga-bgpd"
			if readiness := readinessResponse(t, c, d); readiness.NativeCheckerAvailable || readiness.ActivationMode != "quagga-local-watchdog" || readiness.Actions["validate"].Allowed || readiness.Actions["download"].Allowed || readiness.TargetChecked {
				t.Fatal("missing Quagga checker or activation misreported", readiness)
			}
			if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
				t.Fatal("missing Quagga checker validated a deployment", w.Code)
			}
			c.a.quaggaBinary = "/usr/bin/true"
			if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
				t.Fatal("non-Quagga executable validated a deployment", w.Code)
			}
			actual := os.Getenv("QUAGGA_BGPD_BIN")
			if actual == "" {
				return
			}
			c.a.quaggaBinary = actual
			for _, raw := range c.ok("GET", "/api/router-platforms", nil)["items"].([]any) {
				cap := raw.(map[string]any)
				if cap["platform"] == "quagga" {
					if cap["render"] != true || cap["validate"] != true || cap["fetch"] != true || cap["apply"] != false || cap["rollback"] != false || cap["nativeCheckerAvailable"] != true {
						t.Fatal("Quagga capability overstated or unavailable", cap)
					}
				}
			}
			if w := c.request("GET", path+"/config", nil); w.Code != 409 && w.Code != 403 {
				t.Fatal("unapproved private config downloadable", w.Code)
			}
			c.ok("POST", path+"/validate", nil)
			if w := c.request("POST", path+"/approve", Record{"checksum": strings.Repeat("0", 64)}); w.Code != 409 {
				t.Fatal("wrong review identifier approved Quagga")
			}
			c.ok("POST", path+"/approve", Record{"checksum": revision["checksum"]})
			token := c.ok("POST", "/api/account/tokens", Record{"name": "Quagga file pull", "currentPassword": "very-long-test-password", "scopes": []string{"deployments:pull"}, "routerIds": []string{str(router, "id")}})
			download := bearerRequest(c.a, str(token, "token"), "GET", path+"/config", nil)
			if download.Code != 200 || download.Header().Get("X-Config-Platform") != "quagga" || download.Header().Get("X-Config-SHA256") != routing.Checksum(download.Body.String()) || routing.ValidateManagedQuagga(download.Body.String()) != nil {
				t.Fatal("scoped approved Quagga download lost identity or exact bytes", download.Code)
			}
			member := noteTestUser(t, c, "viewer", "quagga-viewer@example.test", nil)
			if w := member.request("GET", path+"/config", nil); w.Code != 403 {
				t.Fatal("viewer downloaded private credential artifact", w.Code)
			}
			if readiness := readinessResponse(t, c, d); readiness.TargetChecked || readiness.ActivationMode != "quagga-local-watchdog" || !readiness.Actions["download"].Allowed || len(readiness.Actions) != 4 {
				t.Fatal("approved native artifact used unexpected activation or download authority")
			}
			for _, action := range []string{"apply", "rollback"} {
				if w := c.request("POST", path+"/"+action, Record{"checksum": revision["checksum"]}); w.Code != 404 {
					t.Fatal("Quagga readiness introduced a remote execution endpoint", action, w.Code)
				}
			}
		})
	}
}
