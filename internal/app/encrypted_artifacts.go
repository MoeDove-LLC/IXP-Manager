package app

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const encryptedArtifactPlainLimit = 16 << 20
const encryptedArtifactCipherLimit = 24 << 20
const encryptedArtifactStorageLimit = 128 << 20

// The logical revision remains in its domain's state. Only this file envelope
// is shared between router deployments and other reviewed configurations.
type encryptedArtifactFile struct {
	Version  int
	Size     int64
	Checksum string
}

func validArtifactNamespace(namespace string) bool {
	return namespace == "deployments" || namespace == "provisioning" || namespace == "atlas" || namespace == "looking-glass"
}

func encryptedArtifactAAD(namespace, id, checksum string) string {
	if namespace == "deployments" {
		return "deployment:" + id + ":" + checksum
	}
	return namespace + ":" + id + ":" + checksum
}

func (a *App) encryptedArtifactDir(namespace string) string {
	return filepath.Join(filepath.Dir(a.store.path), namespace)
}

func (a *App) initEncryptedArtifactDir(namespace string) error {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	if a.store.closed || !validArtifactNamespace(namespace) {
		return errors.New("encrypted artifact store is unavailable")
	}
	path := a.encryptedArtifactDir(namespace)
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("encrypted artifacts require a private regular directory")
	}
	return nil
}

// All Locked methods require the caller's Store mutex, so Close cannot release
// its process lock while an old instance is reading or changing artifact files.
func (a *App) writeEncryptedArtifactLocked(namespace, id, checksum, content string) (encryptedArtifactFile, error) {
	var metadata encryptedArtifactFile
	if a.store.closed || !validArtifactNamespace(namespace) || !identifier.MatchString(id) {
		return metadata, errors.New("encrypted artifact store is unavailable")
	}
	if len(content) == 0 || len(content) > encryptedArtifactPlainLimit || !externalDigest(checksum) || digest(content) != checksum {
		return metadata, errors.New("artifact content does not match its checksum or size limit")
	}
	sealed, err := a.encryptSecret(encryptedArtifactAAD(namespace, id, checksum), content)
	if err != nil {
		return metadata, errors.New("could not encrypt artifact")
	}
	raw := []byte(sealed + "\n")
	root, err := os.OpenRoot(a.encryptedArtifactDir(namespace))
	if err != nil {
		return metadata, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return metadata, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return metadata, err
	}
	var used int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".enc") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return metadata, errors.New("unexpected encrypted artifact entry")
		}
		used += info.Size()
	}
	if len(raw) > encryptedArtifactCipherLimit || used+int64(len(raw)) > encryptedArtifactStorageLimit {
		return metadata, errors.New("artifact storage exceeds 128 MiB; prune older noncurrent revisions")
	}
	temp := ".deployment-" + token(16) + ".tmp"
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return metadata, errors.New("could not stage encrypted artifact")
	}
	defer root.Remove(temp)
	defer file.Close()
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		return metadata, err
	}
	if err = root.Link(temp, id+".enc"); err != nil {
		return metadata, errors.New("artifact revision already exists or cannot be published")
	}
	if err = root.Remove(temp); err != nil {
		return metadata, err
	}
	if err = directory.Sync(); err != nil {
		return metadata, err
	}
	return encryptedArtifactFile{Version: 1, Size: int64(len(raw)), Checksum: digest(string(raw))}, nil
}

func (a *App) readEncryptedArtifactLocked(namespace, id, checksum string, metadata encryptedArtifactFile) (string, error) {
	if a.store.closed || !validArtifactNamespace(namespace) || !identifier.MatchString(id) {
		return "", errors.New("encrypted artifact store is unavailable")
	}
	if metadata.Version != 1 || metadata.Size < 1 || metadata.Size > encryptedArtifactCipherLimit || !externalDigest(metadata.Checksum) || !externalDigest(checksum) {
		return "", errors.New("invalid encrypted artifact metadata")
	}
	root, err := os.OpenRoot(a.encryptedArtifactDir(namespace))
	if err != nil {
		return "", errors.New("encrypted artifact directory unavailable")
	}
	defer root.Close()
	file, err := root.OpenFile(id+".enc", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("encrypted artifact missing or inaccessible")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != metadata.Size {
		return "", errors.New("encrypted artifact permissions or size mismatch")
	}
	raw, err := io.ReadAll(io.LimitReader(file, encryptedArtifactCipherLimit+1))
	if err != nil || len(raw) > encryptedArtifactCipherLimit || digest(string(raw)) != metadata.Checksum {
		return "", errors.New("encrypted artifact integrity failure")
	}
	content, err := a.decryptSecret(encryptedArtifactAAD(namespace, id, checksum), strings.TrimSuffix(string(raw), "\n"))
	if err != nil || len(content) == 0 || len(content) > encryptedArtifactPlainLimit || digest(content) != checksum {
		return "", errors.New("artifact key or plaintext integrity mismatch")
	}
	return content, nil
}

func (a *App) removeEncryptedArtifactLocked(namespace, id, checksum string, metadata encryptedArtifactFile) (int64, error) {
	if a.store.closed || !validArtifactNamespace(namespace) || !identifier.MatchString(id) {
		return 0, errors.New("encrypted artifact store is unavailable")
	}
	root, err := os.OpenRoot(a.encryptedArtifactDir(namespace))
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if _, err := root.Lstat(id + ".enc"); os.IsNotExist(err) {
		return 0, nil
	}
	if _, err := a.readEncryptedArtifactLocked(namespace, id, checksum, metadata); err != nil {
		return 0, err
	}
	if err := root.Remove(id + ".enc"); err != nil {
		return 0, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer directory.Close()
	return metadata.Size, directory.Sync()
}

// Call only after validating every referenced artifact. Private files with our
// fixed staging/revision names are uncommitted crash leftovers, not user files.
func (a *App) cleanupEncryptedArtifactOrphansLocked(namespace string, ids []string) error {
	if a.store.closed || !validArtifactNamespace(namespace) {
		return errors.New("encrypted artifact store is unavailable")
	}
	root, err := os.OpenRoot(a.encryptedArtifactDir(namespace))
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	referenced := map[string]bool{}
	for _, id := range ids {
		referenced[id+".enc"] = true
	}
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if referenced[name] {
			continue
		}
		temporary := strings.HasPrefix(name, ".deployment-") && strings.HasSuffix(name, ".tmp") && identifier.MatchString(strings.TrimSuffix(strings.TrimPrefix(name, ".deployment-"), ".tmp"))
		encrypted := strings.HasSuffix(name, ".enc") && identifier.MatchString(strings.TrimSuffix(name, ".enc"))
		if !temporary && !encrypted {
			continue
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return errors.New("unsafe orphan artifact entry")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > encryptedArtifactCipherLimit {
			file.Close()
			return errors.New("invalid orphan artifact file")
		}
		if encrypted {
			raw, err := io.ReadAll(io.LimitReader(file, encryptedArtifactCipherLimit+1))
			file.Close()
			if err != nil {
				return err
			}
			text := strings.TrimSuffix(string(raw), "\n")
			decoded, err := base64.RawStdEncoding.DecodeString(text)
			if err != nil || len(decoded) < 29 || base64.RawStdEncoding.EncodeToString(decoded) != text {
				return errors.New("unrecognized orphan artifact ciphertext; inspect manually")
			}
		} else {
			file.Close()
		}
		if err := root.Remove(name); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return directory.Sync()
	}
	return nil
}
