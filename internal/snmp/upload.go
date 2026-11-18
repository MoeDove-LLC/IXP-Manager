package snmp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func ReadToken(path string) (string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", errors.New("collector token file unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
		return "", errors.New("collector token must be a private regular file of at most256bytes")
	}
	b, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return "", errors.New("collector token could not be read")
	}
	return TokenFromBytes(b)
}
func TokenFromBytes(b []byte) (string, error) {
	token := strings.TrimSpace(string(b))
	if token == "" || len(token) > 256 || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("invalid collector token")
	}
	return token, nil
}
func Upload(ctx context.Context, client *http.Client, endpoint, token, mode string, snapshot Snapshot) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/api/devices" {
		return errors.New("device upload requires a trusted HTTPS /api/devices endpoint")
	}
	if mode != "discoveries" && mode != "observations" {
		return errors.New("unsupported device upload operation")
	}
	if _, err = TokenFromBytes([]byte(token)); err != nil {
		return err
	}
	if err = ValidateSnapshot(snapshot, time.Now()); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return errors.New("device snapshot could not be encoded")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + mode
	request, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(data))
	if err != nil {
		return errors.New("device upload request could not be created")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	safeClient := http.Client{Timeout: 20 * time.Second}
	if client != nil {
		safeClient = *client
	}
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("device upload redirects are disabled") }
	response, err := safeClient.Do(request)
	if err != nil {
		return errors.New("device snapshot upload failed; token and response details withheld")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("manager rejected device snapshot; verify scoped switch binding and reviewed ifIndex mappings")
	}
	return nil
}
func WriteSnapshot(path string, snapshot Snapshot) error {
	if err := ValidateSnapshot(snapshot, time.Now()); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("snapshot output must be a regular file")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".snmp-snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func Failure(c Config, err error) Snapshot {
	code := "transport"
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "cancelled") || strings.Contains(message, "deadline"):
		code = "timeout"
	case strings.Contains(message, "authentication"):
		code = "authentication"
	case strings.Contains(message, "mapped interface"):
		code = "mapping-mismatch"
	case strings.Contains(message, "invalid") || strings.Contains(message, "disagree") || strings.Contains(message, "unexpected"):
		code = "invalid-response"
	}
	security := "authPriv"
	if c.Version == "2c" {
		security = "community"
	}
	return Snapshot{Version: 1, SwitchID: c.SwitchID, ObservedAt: time.Now().UTC(), Security: security, ErrorCode: code, Interfaces: []Interface{}, Entities: []Entity{}, Sensors: []Sensor{}, FDB: []FDBEntry{}, Warnings: []string{}}
}

// FetchBindings runs BEFORE the device read. Only revisions are adopted; local
// operator-selected port IDs and ifIndexes are never automatically remapped.
func FetchBindings(ctx context.Context, client *http.Client, endpoint, token, switchID string, configured []PortMapping) ([]PortMapping, error) {
	if _, err := TokenFromBytes([]byte(token)); err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/api/devices" || !resourceID.MatchString(switchID) {
		return nil, errors.New("invalid trusted bindings endpoint")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/bindings"
	query := u.Query()
	query.Set("switchId", switchID)
	u.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errors.New("bindings request failed")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	safeClient := http.Client{Timeout: 20 * time.Second}
	if client != nil {
		safeClient = *client
	}
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("bindings redirects disabled") }
	response, err := safeClient.Do(request)
	if err != nil {
		return nil, errors.New("bindings request failed; credentials withheld")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("bindings request rejected; check device write scope")
	}
	var reply struct {
		SwitchID string        `json:"switchId"`
		Mappings []PortMapping `json:"mappings"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&reply); err != nil || reply.SwitchID != switchID {
		return nil, errors.New("invalid collector bindings response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("bindings response must be one bounded JSON document")
	}
	byID := map[string]PortMapping{}
	for _, mapping := range reply.Mappings {
		if _, duplicate := byID[mapping.PortID]; duplicate || !resourceID.MatchString(mapping.PortID) || mapping.IfIndex < 1 || mapping.IfIndex > 2147483647 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(mapping.OwnershipRevision) {
			return nil, errors.New("invalid collector bindings mapping")
		}
		byID[mapping.PortID] = mapping
	}
	result := make([]PortMapping, 0, len(configured))
	for _, mapping := range configured {
		current, exists := byID[mapping.PortID]
		if !exists || current.IfIndex != mapping.IfIndex {
			return nil, errors.New("mapped interface changed; explicit discovery review and local mapping update required")
		}
		result = append(result, current)
	}
	return result, nil
}
