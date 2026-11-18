package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateReceiptPersistenceAndExclusiveCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	r := receipt{Version: 1, State: "prepared", Token: "secret-persist-token", Checksum: "reviewed-hash", KnownHostsSHA256: "pinned-hash", AllowGlobalRoutingChange: true}
	if err := saveReceipt(path, r, true); err != nil {
		t.Fatal(err)
	}
	if err := saveReceipt(path, r, true); err == nil {
		t.Fatal("overwrote existing receipt")
	}
	r.State = "staged"
	if err := saveReceipt(path, r, false); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("receipt permissions are not private")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got receipt
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "staged" || got.Token != r.Token || got.Checksum != r.Checksum || got.KnownHostsSHA256 != r.KnownHostsSHA256 || !got.AllowGlobalRoutingChange {
		t.Fatal("receipt lost recovery binding")
	}
}
func TestConfirmRejectsPermissiveReceiptBeforeConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"confirm", "--receipt", path}); err == nil {
		t.Fatal("public receipt accepted")
	}
}
