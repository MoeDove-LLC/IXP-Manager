package app

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const monitoringHealthPath = "/api/provisioning/monitoring-health"

func TestMonitoringHealthShowsLatestApprovalAndAuthorizedResponseEvidence(t *testing.T) {
	admin, _, _ := credentialFixture(t)
	nagios := createMonitorProfile(admin, "Member monitoring", "nagios")
	smoke := createMonitorProfile(admin, "Latency monitoring", "smokeping")
	nagiosID, smokeID := str(nagios, "id"), str(smoke, "id")
	byProfile := func(profileID string) Record {
		t.Helper()
		report := admin.ok(http.MethodGet, monitoringHealthPath, nil)
		for _, raw := range report["items"].([]any) {
			item := Record(raw.(map[string]any))
			if item["profileId"] == profileID {
				return item
			}
		}
		t.Fatal("monitoring profile missing from report", profileID)
		return nil
	}
	if item := byProfile(smokeID); item["approvalStatus"] != "missing" || item["pullStatus"] != "not-observed" {
		t.Fatal("unapproved profile had delivery evidence", item)
	}
	generated := admin.ok(http.MethodPost, "/api/provisioning", Record{"profileId": nagiosID})
	path := approveProvision(admin, generated)
	if item := byProfile(nagiosID); item["approvalStatus"] != "current" || item["revisionId"] != generated["id"] || item["lastPulledAt"] != nil || item["lastDownloadedAt"] != nil {
		t.Fatal("approval was mistaken for delivery", item)
	}
	if response := admin.request(http.MethodGet, path+"/config", nil); response.Code != http.StatusOK {
		t.Fatal("manual provision response failed", response.Code)
	}
	if item := byProfile(nagiosID); item["lastDownloadedAt"] == nil || item["lastPulledAt"] != nil || item["pullStatus"] != "not-observed" {
		t.Fatal("manual request was mistaken for a scoped automated pull", item)
	}
	raw := str(admin.ok(http.MethodPost, "/api/account/tokens", Record{
		"name": "Monitoring fixture", "currentPassword": "very-long-test-password",
		"scopes": []string{"provisioning:read", "provisioning:pull"}, "profileIds": []string{nagiosID},
	}), "token")
	if response := bearerRequest(admin.a, raw, http.MethodGet, path+"/config", nil); response.Code != http.StatusOK {
		t.Fatal("bound provisioning pull failed", response.Code)
	}
	if item := byProfile(nagiosID); item["pullStatus"] != "recent" || item["lastPulledAt"] == nil || item["lastDownloadedAt"] == nil {
		t.Fatal("missing separately tracked delivery timestamps", item)
	}
	if response := bearerRequest(admin.a, raw, http.MethodGet, monitoringHealthPath, nil); response.Code != http.StatusForbidden {
		t.Fatal("profile credential gained access to the full operations view", response.Code)
	}

	admin.ok(http.MethodPut, "/api/resources/provisioning-profiles/"+nagiosID, Record{
		"name": "Revised monitoring policy", "options": nagios["options"], "expectedRevision": nagios["revision"],
	})
	if item := byProfile(nagiosID); item["approvalStatus"] != "stale" || item["revisionId"] != generated["id"] {
		t.Fatal("changed current rule failed to flag the reviewed version", item)
	}
	next := admin.ok(http.MethodPost, "/api/provisioning", Record{"profileId": nagiosID})
	if item := byProfile(nagiosID); item["revisionId"] != generated["id"] {
		t.Fatal("draft changed the selected approval", item)
	}
	approveProvision(admin, next)
	if item := byProfile(nagiosID); item["approvalStatus"] != "current" || item["revisionId"] != next["id"] || item["pullStatus"] != "not-observed" || item["lastDownloadedAt"] != nil {
		t.Fatal("fresh approval borrowed earlier revision delivery", item)
	}
}

func TestMonitoringHealthIsPaginatedAndBrowserAdminOnly(t *testing.T) {
	admin, _ := setupClient(t)
	createMonitorProfile(admin, "First monitor", "smokeping")
	createMonitorProfile(admin, "Second monitor", "nagios")
	first := admin.ok(http.MethodGet, monitoringHealthPath+"?limit=1", nil)
	items := first["items"].([]any)
	if len(items) != 1 || first["checkedAt"] == nil || first["pullStaleAfterSeconds"] != float64(86400) {
		t.Fatal("unexpected monitoring health page", first)
	}
	id := str(Record(items[0].(map[string]any)), "profileId")
	if str(first, "nextAfter") != id || id == "" {
		t.Fatal("next page cursor did not refer to the last item")
	}
	second := admin.ok(http.MethodGet, monitoringHealthPath+"?limit=1&after="+url.QueryEscape(id), nil)
	remaining := second["items"].([]any)
	if len(remaining) != 1 || str(Record(remaining[0].(map[string]any)), "profileId") <= id || str(second, "nextAfter") != "" {
		t.Fatal("cursor repeated or dropped the second profile", second)
	}
	for _, query := range []string{"?limit=0", "?limit=26", "?limit=1&limit=2", "?after=", "?after=../anything", "?unexpected=yes"} {
		if response := admin.request(http.MethodGet, monitoringHealthPath+query, nil); response.Code != http.StatusBadRequest {
			t.Fatal("invalid report query accepted", query, response.Code)
		}
	}
	if response := admin.request(http.MethodPost, monitoringHealthPath, nil); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatal("report accepted non-read method", response.Code)
	}
	if response := admin.request(http.MethodGet, monitoringHealthPath, nil); response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), `"config"`) {
		t.Fatal("unexpected report cache or content coverage", response.Code)
	}
	const operatorEmail, operatorPassword = "monitoring-health-operator@example.test", "monitoring-health-fixture-password"
	admin.create("users", Record{"name": "Monitor operator", "email": operatorEmail, "password": operatorPassword, "role": "operator"})
	operator := docLoginClient(t, admin.a, operatorEmail, operatorPassword)
	if response := operator.request(http.MethodGet, monitoringHealthPath, nil); response.Code != http.StatusForbidden {
		t.Fatal("operator gained access to operations report", response.Code)
	}
}
