package app

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStorageCheckHTTPReportsProtectedDirectoryWithoutChangingLiveFiles(t *testing.T) {
	admin, dir := setupClient(t)
	before := map[string][]byte{}
	for _, name := range []string{"state.json", "security.key", ".lock"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	names := func() []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var result []string
		for _, entry := range entries {
			result = append(result, entry.Name())
		}
		return result
	}
	baselineNames := names()
	revision := admin.a.store.revision
	result := admin.request(http.MethodPost, storageCheckPath, Record{})
	if result.Code != http.StatusOK || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unexpected storage check HTTP result", result.Code, result.Body.String())
	}
	report := responseRecord(t, result)
	if report["status"] != "ok" || str(report, "checkedAt") == "" {
		t.Fatal("expected usable private data directory", report)
	}
	steps := map[string]bool{}
	for _, item := range report["checks"].([]any) {
		step := Record(item.(map[string]any))
		if step["status"] != "passed" {
			t.Fatal("healthy check included failed stage", step)
		}
		steps[str(step, "id")] = true
	}
	for _, required := range []string{"binding", "directory", "create", "write", "sync-file", "rename", "readback", "cleanup", "sync-directory", "verify-binding"} {
		if !steps[required] {
			t.Fatalf("missing storage probe phase %s", required)
		}
	}
	if strings.Contains(result.Body.String(), dir) || strings.Contains(result.Body.String(), "security.key") || strings.Contains(result.Body.String(), "state.json") {
		t.Fatal("storage check exposed private file data")
	}
	if admin.a.store.revision != revision || !reflect.DeepEqual(names(), baselineNames) {
		t.Fatal("storage check changed an application revision or left files")
	}
	for name, want := range before {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("storage check altered a live file: %s", name)
		}
	}
}

func TestStorageCheckHTTPRequiresExplicitAdminRequestWithoutParameters(t *testing.T) {
	admin, _ := setupClient(t)
	guest := &client{a: admin.a, t: t}
	if reply := guest.request(http.MethodPost, storageCheckPath, Record{}); reply.Code != http.StatusUnauthorized {
		t.Fatal("anonymous storage check", reply.Code)
	}
	const email, password = "store-operator@example.test", "operator-storage-fixture-password"
	admin.create("users", Record{"name": "Store operator", "email": email, "password": password, "role": "operator"})
	operator := docLoginClient(t, admin.a, email, password)
	if reply := operator.request(http.MethodPost, storageCheckPath, Record{}); reply.Code != http.StatusForbidden {
		t.Fatal("operator storage check", reply.Code)
	}
	raw := str(admin.ok(http.MethodPost, "/api/account/tokens", Record{
		"name": "Storage check fixture", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read"},
	}), "token")
	if reply := bearerRequest(admin.a, raw, http.MethodPost, storageCheckPath, Record{}); reply.Code != http.StatusForbidden {
		t.Fatal("automation credential storage check", reply.Code)
	}
	if reply := admin.request(http.MethodGet, storageCheckPath, nil); reply.Code != http.StatusMethodNotAllowed || reply.Header().Get("Allow") != http.MethodPost {
		t.Fatal("read method unexpectedly ran the check", reply.Code)
	}
	csrf := admin.csrf
	admin.csrf = ""
	reply := admin.request(http.MethodPost, storageCheckPath, Record{})
	admin.csrf = csrf
	if reply.Code != http.StatusForbidden {
		t.Fatal("storage check skipped CSRF", reply.Code)
	}
	if reply := admin.request(http.MethodPost, storageCheckPath+"?path=/tmp", Record{}); reply.Code != http.StatusBadRequest {
		t.Fatal("storage check used caller-controlled query parameters", reply.Code)
	}
	if reply := admin.request(http.MethodPost, storageCheckPath, Record{"path": "/tmp"}); reply.Code != http.StatusBadRequest {
		t.Fatal("storage check accepted caller-controlled request parameters", reply.Code)
	}
	admin.a.storageCheckMu.Lock()
	reply = admin.request(http.MethodPost, storageCheckPath, Record{})
	admin.a.storageCheckMu.Unlock()
	if reply.Code != http.StatusConflict {
		t.Fatal("overlapping storage probe accepted", reply.Code)
	}
}
