package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMismatchedEncryptionKeyFailsBeforeServing(t *testing.T) {
	c, dir := setupClient(t)
	s := c.a.store.Snapshot()
	id := str(s.Resources["users"][0], "id")
	encrypted, err := c.a.encryptSecret(id, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.a.store.Update(func(s *State) error {
		sec := s.Security[id]
		sec.Secret = encrypted
		sec.MFAEnabled = true
		s.Security[id] = sec
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.a.Close()
	if err = os.WriteFile(filepath.Join(dir, "security.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := New(Config{DataDir: dir}); err == nil {
		a.Close()
		t.Fatal("mismatched encryption key accepted")
	}
}
