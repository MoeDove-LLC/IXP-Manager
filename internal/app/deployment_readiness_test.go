package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readinessFixture(t *testing.T, platform string) (*client, string, Deployment) {
	t.Helper()
	c, dir := setupClient(t)
	vlan := c.create("vlans", Record{"name": "Readiness fixture", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	router := c.create("routers", Record{"name": "Readiness fixture", "platform": platform, "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": vlan["id"]})
	view := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
	s := c.a.store.Snapshot()
	return c, dir, *deploymentByID(&s, str(view, "id"))
}

func readinessResponse(t *testing.T, c *client, d Deployment) deploymentReadiness {
	t.Helper()
	w := c.request("GET", "/api/deployments/"+d.ID+"/readiness", nil)
	if w.Code != 200 {
		t.Fatalf("readiness failed: %d %s", w.Code, w.Body.String())
	}
	var result deploymentReadiness
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// These status fixtures model already persisted metadata. They do not execute
// or claim native validation of any platform.
func readinessStatus(t *testing.T, c *client, d Deployment, status string) Deployment {
	t.Helper()
	if err := c.a.store.Update(func(s *State) error {
		current := deploymentByID(s, d.ID)
		current.Status = status
		d = *current
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDeploymentReadinessPlatformStatusMatrixIsReadOnly(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3", "frr", "openbgpd", "junos", "routeros7", "quagga"} {
		t.Run(platform, func(t *testing.T) {
			c, dir, d := readinessFixture(t, platform)
			marker := filepath.Join(dir, "checker-was-run")
			checker := filepath.Join(dir, "checker-fixture")
			if err := os.WriteFile(checker, []byte("#!/bin/sh\nprintf forbidden > "+readinessShellQuote(marker)+"\nexit 13\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c.a.birdBinary, c.a.bird3Binary, c.a.frrBinary, c.a.openBGPDBinary = checker, checker, checker, checker
			c.a.quaggaBinary = checker
			for _, status := range []string{"draft", "validated", "approved"} {
				d = readinessStatus(t, c, d, status)
				before, _ := json.Marshal(c.a.store.Snapshot())
				artifactPath := filepath.Join(dir, "deployments", d.ID+".enc")
				artifact, err := os.ReadFile(artifactPath)
				if err != nil {
					t.Fatal(err)
				}
				r := readinessResponse(t, c, d)
				if r.Inputs != "current" || r.Artifact != "verified" || r.TargetChecked || r.Platform != platform || r.Status != status || r.ReviewIdentifier != c.a.deploymentReviewToken(d) || r.ReviewIdentifier == d.Checksum {
					t.Fatal("readiness lost its observed revision or exposed a plaintext digest")
				}
				preview := platform == "routeros7"
				for action, wanted := range map[string]bool{"generate": true, "validate": status == "draft" && !preview, "approve": status == "validated" && !preview, "download": status == "approved" && !preview} {
					if actual, exists := r.Actions[action]; !exists || actual.Allowed != wanted || actual.Allowed != (len(actual.Reasons) == 0) {
						t.Fatalf("%s %s action %s disagrees with expected policy", platform, status, action)
					}
				}
				if platform == "junos" && (r.ValidationLevel != "structural" || r.NativeCheckerAvailable || r.ActivationMode != "netconf-confirmed-commit") {
					t.Fatal("Junos XML check was described as target/native verification")
				}
				if preview && (r.ValidationLevel != "none" || r.NativeCheckerAvailable || r.ActivationMode != "unavailable") {
					t.Fatal("RouterOS preview gained activation authority")
				}
				if platform == "quagga" && (r.ActivationMode != "quagga-local-watchdog" || len(r.Actions) != 4) {
					t.Fatal("Quagga readiness must expose local guidance with only artifact actions")
				}
				if !preview && platform != "junos" && (r.ValidationLevel != "native" || !r.NativeCheckerAvailable) {
					t.Fatal("configured executable availability was not reported")
				}
				after, _ := json.Marshal(c.a.store.Snapshot())
				nextArtifact, err := os.ReadFile(artifactPath)
				if err != nil || !bytes.Equal(before, after) || !bytes.Equal(artifact, nextArtifact) {
					t.Fatal("GET changed deployment state or immutable ciphertext")
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("GET readiness executed an external checker")
			}
		})
	}
}

func TestDeploymentReadinessRejectsDriftAndUnavailableArtifacts(t *testing.T) {
	for _, mode := range []string{"inputs", "platform", "invalid-inputs", "router-missing", "tampered", "pruned", "checker-missing"} {
		t.Run(mode, func(t *testing.T) {
			c, dir, d := readinessFixture(t, "bird2")
			want := ""
			switch mode {
			case "inputs", "platform", "invalid-inputs", "router-missing":
				err := c.a.store.Update(func(s *State) error {
					router := find(s, "routers", d.RouterID)
					switch mode {
					case "inputs":
						router["routerId"], want = "192.0.2.2", "inputs-changed"
					case "platform":
						router["platform"], want = "frr", "platform-changed"
					case "invalid-inputs":
						router["routerId"], want = "invalid-address", "inputs-invalid"
					case "router-missing":
						s.Resources["routers"], want = nil, "router-missing"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "tampered":
				if err := os.WriteFile(filepath.Join(dir, "deployments", d.ID+".enc"), []byte("fixture corrupted ciphertext"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "artifact-unavailable"
			case "pruned":
				c.ok("POST", "/api/deployments/"+d.ID+"/prune", Record{"checksum": c.a.deploymentReviewToken(d), "acknowledge": true, "reason": "fixture retention"})
				want = "artifact-pruned"
			case "checker-missing":
				c.a.birdBinary, want = "/missing/readiness-checker", "checker-missing"
			}
			r := readinessResponse(t, c, d)
			if r.Actions["validate"].Allowed || !contains(r.Actions["validate"].Reasons, want) {
				t.Fatalf("%s did not block validation with %s", mode, want)
			}
			if mode == "pruned" && r.Artifact != "pruned" || mode == "tampered" && r.Artifact != "unavailable" {
				t.Fatal("unavailable bytes were represented as verified")
			}
		})
	}
}

func TestDeploymentReadinessDoesNotExposeSecretsOrRequireNewMetadata(t *testing.T) {
	c, dir, peer := credentialFixture(t)
	secret := "ReadinessFixtureSecret_123456"
	path := "/api/peer-credentials/" + str(peer, "id")
	change := credentialChange(c.ok("GET", path, nil))
	change["secret"] = secret
	c.ok("PUT", path, change)
	dto := c.ok("POST", "/api/deployments", Record{"routerId": peer["routerId"]})
	s := c.a.store.Snapshot()
	d := *deploymentByID(&s, str(dto, "id"))
	if err := c.a.store.Update(func(s *State) error {
		current := deploymentByID(s, d.ID)
		current.RouterRole, current.RFC1997Mode = "", ""
		current.CredentialRevisions, current.CredentialPresence, current.RFC1997Passthrough = nil, nil, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := c.request("GET", "/api/deployments/"+d.ID+"/readiness", nil)
	if w.Code != 200 {
		t.Fatal("legacy optional metadata prevented readiness", w.Code)
	}
	for _, forbidden := range []string{secret, d.Checksum, d.ArtifactChecksum, dir, "ciphertext", "<configuration", "authentication-key"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("readiness disclosed private artifact or configuration information")
		}
	}
	var result deploymentReadiness
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Inputs != "current" || result.Artifact != "verified" {
		t.Fatal("missing optional legacy fields changed byte-currentness")
	}
}

func TestDeploymentReadinessBrowserAndTokenScopes(t *testing.T) {
	c, _, d := readinessFixture(t, "junos")
	d = readinessStatus(t, c, d, "approved")
	member := c.create("members", Record{"name": "Readiness member", "asn": 64501, "status": "active"})
	for _, role := range []string{"viewer", "member", "operator"} {
		user := Record{"name": role, "email": role + "@example.net", "role": role, "password": "Readiness-long-password"}
		if role == "member" {
			user["memberId"] = member["id"]
		}
		c.create("users", user)
		other := docLoginClient(t, c.a, role+"@example.net", "Readiness-long-password")
		w := other.request("GET", "/api/deployments/"+d.ID+"/readiness", nil)
		if role == "member" {
			if w.Code != 403 {
				t.Fatal("member read staff readiness")
			}
			continue
		}
		var result deploymentReadiness
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Actions["download"].Allowed != (role == "operator") || result.Actions["generate"].Allowed != (role == "operator") || result.Actions["approve"].Allowed {
			t.Fatal("browser role action permissions disagree")
		}
	}
	for _, mode := range []string{"read", "write", "pull", "read-write", "read-pull", "read-pull-other"} {
		raw := "mdt." + mode + ".readiness-fixture"
		scopes := []string{}
		for _, scope := range []string{"read", "write", "pull"} {
			if strings.Contains(mode, scope) {
				scopes = append(scopes, "deployments:"+scope)
			}
		}
		routers := []string{d.RouterID}
		if mode == "read-pull-other" {
			routers = []string{"another-router"}
		}
		if err := c.a.store.Update(func(s *State) error {
			id := accountIDUnsafe(s)
			security := s.Security[id]
			security.Tokens = append(security.Tokens, APIToken{ID: mode, Hash: digest(raw), Scopes: scopes, RouterIDs: routers, ExpiresAt: time.Now().Add(time.Hour)})
			s.Security[id] = security
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		w := bearerRequest(c.a, raw, "GET", "/api/deployments/"+d.ID+"/readiness", nil)
		if !contains(scopes, "deployments:read") {
			if w.Code != 403 {
				t.Fatal("non-read token bypassed readiness scope", mode)
			}
			continue
		}
		var result deploymentReadiness
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Actions["generate"].Allowed != contains(scopes, "deployments:write") || result.Actions["download"].Allowed != (mode == "read-pull") || result.Actions["approve"].Allowed {
			t.Fatal("token readiness action permissions disagree", mode)
		}
	}
}

func TestDeploymentReadinessBodyReadLogoutCannotMutate(t *testing.T) {
	for _, action := range []string{"preview", "approve", "prune", "delete"} {
		t.Run(action, func(t *testing.T) {
			c, _, d := readinessFixture(t, "junos")
			method, path, payload := "POST", "/api/deployments", Record{"routerId": d.RouterID}
			if action == "approve" {
				c.ok("POST", "/api/deployments/"+d.ID+"/validate", nil)
				path, payload = "/api/deployments/"+d.ID+"/approve", Record{"checksum": c.a.deploymentReviewToken(d)}
			}
			if action == "prune" || action == "delete" {
				path, payload = "/api/deployments/"+d.ID+"/prune", Record{"checksum": c.a.deploymentReviewToken(d), "acknowledge": true, "reason": "fixture retention"}
				if action == "delete" {
					c.ok("POST", path, payload)
					method, path = "DELETE", "/api/deployments/"+d.ID
				}
			}
			before, _ := json.Marshal(c.a.store.Snapshot())
			raw, _ := json.Marshal(payload)
			body := &legacyPatchReadHook{Reader: bytes.NewReader(raw), hook: func() { c.a.mu.Lock(); delete(c.a.sessions, c.cookie.Value); c.a.mu.Unlock() }}
			r := httptest.NewRequest(method, path, body)
			r.AddCookie(c.cookie)
			r.Header.Set("X-CSRF-Token", c.csrf)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			c.a.ServeHTTP(w, r)
			after, _ := json.Marshal(c.a.store.Snapshot())
			if w.Code != 401 || !bytes.Equal(before, after) {
				t.Fatal("request committed after logout during body read", w.Code)
			}
		})
	}
}

func TestDeploymentReadinessDownloadRechecksSavedAuthority(t *testing.T) {
	for _, mode := range []string{"logout", "generation", "role", "token-revoked", "token-expired", "token-router-changed"} {
		t.Run(mode, func(t *testing.T) {
			c, _, d := readinessFixture(t, "junos")
			d = readinessStatus(t, c, d, "approved")
			r := httptest.NewRequest("GET", "/api/deployments/"+d.ID+"/config", nil)
			r.AddCookie(c.cookie)
			if strings.HasPrefix(mode, "token-") {
				raw := "mdt.saved.readiness-fixture"
				if err := c.a.store.Update(func(s *State) error {
					id := accountIDUnsafe(s)
					sec := s.Security[id]
					sec.Tokens = []APIToken{{ID: "saved", Hash: digest(raw), Scopes: []string{"deployments:pull"}, RouterIDs: []string{d.RouterID}, ExpiresAt: time.Now().Add(time.Hour)}}
					s.Security[id] = sec
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+raw)
			}
			user, session, ok := c.a.authenticate(r)
			if !ok {
				t.Fatal("fixture authority was not initially valid")
			}
			if mode == "logout" {
				c.a.mu.Lock()
				delete(c.a.sessions, c.cookie.Value)
				c.a.mu.Unlock()
			} else if err := c.a.store.Update(func(s *State) error {
				id := session.UserID
				sec := s.Security[id]
				switch mode {
				case "generation":
					sec.Generation++
				case "role":
					find(s, "users", id)["role"] = "viewer"
				case "token-revoked":
					sec.Tokens = nil
				case "token-expired":
					sec.Tokens[0].ExpiresAt = time.Now().Add(-time.Second)
				case "token-router-changed":
					sec.Tokens[0].RouterIDs = []string{"another-router"}
				}
				s.Security[id] = sec
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			_, config, status, err := c.a.currentDeploymentDownload(r, user, session, d)
			if err == nil || config != "" || status < 400 {
				t.Fatal("saved authority released deployment plaintext after revocation")
			}
		})
	}
}

func readinessShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestDeploymentReadinessValidatorCompletionRechecksLogout(t *testing.T) {
	c, dir, d := readinessFixture(t, "bird2")
	started, release := filepath.Join(dir, "checker-started"), filepath.Join(dir, "checker-release")
	checker := filepath.Join(dir, "synchronized-checker-fixture")
	// A bounded fake process tests the authorization handoff only. Its success
	// is not evidence that any native daemon parsed the configuration.
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'BIRD 2.0.0\\n'; exit 0; fi\nprintf ready > %s\nn=0\nwhile [ ! -f %s ] && [ \"$n\" -lt 300 ]; do sleep 0.01; n=$((n+1)); done\n[ -f %s ]\n", readinessShellQuote(started), readinessShellQuote(release), readinessShellQuote(release))
	if err := os.WriteFile(checker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c.a.birdBinary = checker
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- c.request("POST", "/api/deployments/"+d.ID+"/validate", nil) }()
	defer func() { _ = os.WriteFile(release, []byte("release"), 0600) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture checker did not reach its synchronization point")
		}
		time.Sleep(time.Millisecond)
	}
	c.ok("POST", "/api/logout", nil)
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		s := c.a.store.Snapshot()
		if w.Code != http.StatusUnauthorized || deploymentByID(&s, d.ID).Status != "draft" {
			t.Fatal("external checker result committed after logout", w.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bounded checker fixture did not return")
	}
}
