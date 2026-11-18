package app

import (
	"strings"
	"testing"
	"time"
)

func TestOfflineResetRequiresStoppedStoreAndPreservesMFA(t *testing.T) {
	c, dir := setupClient(t)
	secret, _ := enableMFA(t, c)
	id := accountID(c.a)
	if err := c.a.store.Update(func(s *State) error {
		u := s.Security[id]
		u.Tokens = []APIToken{{ID: "test", Hash: digest("test"), ExpiresAt: time.Now().Add(time.Hour)}}
		s.Security[id] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := c.a.store.Snapshot().Security[id]
	if err := ResetPassword(dir, "admin@example.net", "new-offline-password", false); err == nil {
		t.Fatal("reset modified running store")
	}
	c.a.Close()
	if err := ResetPassword(dir, "missing@example.net", "new-offline-password", false); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := ResetPassword(dir, "admin@example.net", "new-offline-password", false); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	user := find(&state, "users", id)
	after := state.Security[id]
	if !checkPassword(str(user, "passwordHash"), "new-offline-password") || checkPassword(str(user, "passwordHash"), "very-long-test-password") {
		t.Fatal("password not replaced")
	}
	if !after.MFAEnabled || after.Secret != before.Secret || len(after.RecoveryHashes) != len(before.RecoveryHashes) {
		t.Fatal("MFA changed without explicit flag")
	}
	if len(after.Tokens) != 0 || after.Generation <= before.Generation || str(user, "role") != "admin" {
		t.Fatal("reset failed revocation or changed administrator role")
	}
	if strings.Contains(after.Secret, secret) {
		t.Fatal("secret leaked")
	}
	if state.Audit[len(state.Audit)-1].Action != "offline-password-reset" {
		t.Fatal("recovery audit missing")
	}
	store.Close()
	if err = ResetPassword(dir, "admin@example.net", "second-offline-password", true); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after = store.Snapshot().Security[id]
	if after.MFAEnabled || after.Secret != "" || len(after.RecoveryHashes) != 0 || len(after.Tokens) != 0 {
		t.Fatal("explicit MFA removal incomplete")
	}
}
