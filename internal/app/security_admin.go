package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResetPassword is an offline recovery operation. The store's exclusive process
// lock requires the web service to be stopped, so all browser sessions are gone.
// MFA is preserved unless the operator explicitly requests its removal.
func ResetPassword(storeDir, email, password string, disableMFA bool) error {
	if len(password) < 12 || len(password) > 128 {
		return errors.New("password must contain 12 to 128 bytes")
	}
	st, err := os.Lstat(filepath.Join(storeDir, "state.json"))
	if err != nil || !st.Mode().IsRegular() {
		return errors.New("existing regular state.json is required; check --data-dir")
	}
	store, err := OpenStore(storeDir)
	if err != nil {
		return err
	}
	defer store.Close()
	hash := hashPassword(password)
	return store.Update(func(s *State) error {
		var user Record
		for _, u := range s.Resources["users"] {
			if strings.EqualFold(str(u, "email"), strings.TrimSpace(email)) {
				user = u
				break
			}
		}
		if user == nil {
			return errors.New("account not found")
		}
		id := str(user, "id")
		user["passwordHash"] = hash
		user["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		security := s.Security[id]
		security.Generation++
		security.Tokens = nil
		security.PendingSecret = ""
		security.PendingUntil = time.Time{}
		if disableMFA {
			security.MFAEnabled = false
			security.Secret = ""
			security.RecoveryHashes = nil
			security.LastStep = 0
		}
		s.Security[id] = security
		action := "offline-password-reset"
		if disableMFA {
			action = "offline-password-and-mfa-reset"
		}
		audit(s, "offline-admin", action, "users", id)
		return nil
	})
}
