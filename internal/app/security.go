package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SecurityUser struct {
	Generation     uint64     `json:"generation"`
	MFAEnabled     bool       `json:"mfaEnabled"`
	Secret         string     `json:"secret,omitempty"`
	PendingSecret  string     `json:"pendingSecret,omitempty"`
	PendingUntil   time.Time  `json:"pendingUntil,omitempty"`
	LastStep       int64      `json:"lastStep"`
	RecoveryHashes []string   `json:"recoveryHashes,omitempty"`
	Tokens         []APIToken `json:"tokens,omitempty"`
}
type APIToken struct {
	ProfileIDs []string  `json:"profileIds,omitempty"`
	MemberID   string    `json:"memberId,omitempty"`
	RouterIDs  []string  `json:"routerIds,omitempty"`
	SwitchIDs  []string  `json:"switchIds,omitempty"`
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hash       string    `json:"hash"`
	Scopes     []string  `json:"scopes"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }

func loadSecurityKey(dir string, s *State) ([]byte, error) {
	path := filepath.Join(dir, "security.key")
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if s.ObservationKeyHash != "" {
			return nil, errors.New("security.key missing; restore the matching observation history key")
		}
		if atlasRequiresKey(*s) || lookingGlassRequiresKey(*s) {
			return nil, errors.New("security.key missing; restore matching encrypted diagnostic artifacts")
		}
		for _, d := range s.ProvisionRevisions {
			if !d.Pruned {
				return nil, errors.New("security.key missing; restore matching encrypted provisioning backup")
			}
		}
		for _, d := range s.Deployments {
			if d.ArtifactVersion != 0 && !d.Pruned {
				return nil, errors.New("security.key missing; restore matching encrypted deployment backup")
			}
		}
		for _, c := range s.PeerCredentials {
			if c.Ciphertext != "" {
				return nil, errors.New("security.key missing; restore matching encrypted BGP credential backup")
			}
		}
		if len(s.Outbox.Entries) > 0 {
			return nil, errors.New("security.key missing; restore matching encrypted outbox backup")
		}
		for _, u := range s.Security {
			if u.Secret != "" || u.PendingSecret != "" {
				return nil, errors.New("security.key is missing; restore it with the encrypted account state")
			}
		}
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err = f.Write(key); err != nil {
			return nil, err
		}
		if err = f.Sync(); err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		d, err := os.Open(dir)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		if err = d.Sync(); err != nil {
			return nil, err
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() != 32 {
		return nil, errors.New("security.key must be a private 32-byte regular file (0600)")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if s.ObservationKeyHash != "" && s.ObservationKeyHash != digest(string(key)) {
		return nil, errors.New("security.key does not match observation history")
	}
	return key, nil
}
func (a *App) encryptSecret(userID, secret string) (string, error) {
	block, err := aes.NewCipher(a.store.securityKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(secret), []byte(userID))), nil
}
func (a *App) decryptSecret(userID, encoded string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(a.store.securityKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("encrypted MFA secret is invalid")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(userID))
	return string(plain), err
}
func totp(secret []byte, step int64, digits int) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 15
	value := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	mod := uint32(1000000)
	if digits == 8 {
		mod = 100000000
	}
	return fmt.Sprintf("%0*d", digits, value%mod)
}
func matchingStep(secret, code string, now time.Time, last int64) (int64, bool) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(code) != 6 {
		return 0, false
	}
	for _, delta := range []int64{0, -1, 1} {
		step := now.Unix()/30 + delta
		if step > last && subtle.ConstantTimeCompare([]byte(totp(key, step, 6)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
func (a *App) consumeMFA(userID string, u *SecurityUser, code string, now time.Time) error {
	if !u.MFAEnabled {
		return nil
	}
	normalized := strings.ToUpper(strings.TrimSpace(code))
	hash := digest(normalized)
	for i, h := range u.RecoveryHashes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(hash)) == 1 {
			u.RecoveryHashes = append(u.RecoveryHashes[:i], u.RecoveryHashes[i+1:]...)
			return nil
		}
	}
	secret, err := a.decryptSecret(userID, u.Secret)
	if err != nil {
		return errors.New("MFA verification unavailable")
	}
	step, ok := matchingStep(secret, strings.TrimSpace(code), now, u.LastStep)
	if !ok {
		return errors.New("invalid or already used authentication code")
	}
	u.LastStep = step
	return nil
}
func recoveryCodes() ([]string, []string) {
	codes := make([]string, 10)
	hashes := make([]string, 10)
	for i := range codes {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		s := strings.ToUpper(hex.EncodeToString(b))
		codes[i] = s[:5] + "-" + s[5:10] + "-" + s[10:15] + "-" + s[15:]
		hashes[i] = digest(codes[i])
	}
	return codes, hashes
}
func invalidateSecurity(s *State, id string) {
	u := s.Security[id]
	u.Generation++
	u.Tokens = nil
	s.Security[id] = u
}
func (a *App) revokeSessions(id, exceptCookie string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, session := range a.sessions {
		if session.UserID == id && key != exceptCookie {
			delete(a.sessions, key)
		}
	}
}

// Preserve only the authenticated current session at the new generation. This
// also invalidates logins whose password check finished before the change but
// whose session cookie is issued after the in-memory cleanup.
func (a *App) keepSession(id, cookie string, generation uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, session := range a.sessions {
		if session.UserID != id {
			continue
		}
		if key != cookie {
			delete(a.sessions, key)
		} else {
			session.Generation = generation
			session.RememberDeviceID = ""
			a.sessions[key] = session
		}
	}
}

func clearCookie(w http.ResponseWriter, secure bool) {
	defer clearRememberedCookie(w)
	http.SetCookie(w, &http.Cookie{Name: "moedove_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}
func (a *App) reauth(id, password, code string, change func(*State, *SecurityUser) error) error {
	if len(password) > 128 {
		return errors.New("invalid current password")
	}
	snapshot := a.store.Snapshot()
	user := find(&snapshot, "users", id)
	expected := str(user, "passwordHash")
	if user == nil || !checkPassword(expected, password) {
		return errors.New("invalid current password")
	}
	return a.store.Update(func(s *State) error {
		current := find(s, "users", id)
		if current == nil || str(current, "passwordHash") != expected {
			return errors.New("account changed; sign in again")
		}
		security := s.Security[id]
		if err := a.consumeMFA(id, &security, code, time.Now()); err != nil {
			return err
		}
		if err := change(s, &security); err != nil {
			return err
		}
		s.Security[id] = security
		return nil
	})
}
func availableScopes(role string) []string {
	scopes := []string{"resources:read", "documents:read", "monitoring:read", "looking-glass:read", "traffic:read", "sflow:read"}
	if role == "admin" {
		scopes = append(scopes, "mailing-lists:read")
	}
	if role != "member" {
		scopes = append(scopes, "deployments:read", "provisioning:read", "audit:read", "exports:read", "policy:read", "integrations:read", "devices:read", "atlas:read", "core:read")
	}
	if role == "admin" || role == "operator" {
		scopes = append(scopes, "resources:write", "documents:write", "deployments:write", "deployments:pull", "provisioning:write", "provisioning:pull", "monitoring:write", "policy:write", "integrations:write", "devices:write", "looking-glass:run", "sflow:write", "sflow:forward")
	}
	return scopes
}
func contains(items []string, v string) bool {
	for _, x := range items {
		if x == v {
			return true
		}
	}
	return false
}
func bearerScope(r *http.Request) string {
	read := r.Method == "GET" || r.Method == "HEAD"
	path := r.URL.Path
	if _, _, _, _, export := mailingExportRoute(path); export && r.Method == "GET" {
		return "mailing-lists:read"
	}
	if path == "/api/sflow/delivery-receipts" && r.Method == "POST" {
		return "sflow:forward"
	}
	if path == "/api/sflow/bindings" && read || path == "/api/sflow/ingest" && r.Method == "POST" {
		return "sflow:write"
	}
	if path == "/api/sflow" || strings.HasPrefix(path, "/api/sflow/") {
		if read {
			return "sflow:read"
		}
		return ""
	}
	if strings.HasPrefix(path, "/api/traffic/") {
		if read {
			return "traffic:read"
		}
		return ""
	}
	if path == "/api/core" || strings.HasPrefix(path, "/api/core/") {
		if read {
			return "core:read"
		}
		return ""
	}
	if path == "/api/atlas" || strings.HasPrefix(path, "/api/atlas/") {
		if read {
			return "atlas:read"
		}
		return ""
	}
	if path == "/api/looking-glass/claim" || path == "/api/looking-glass/complete" {
		if r.Method == "POST" {
			return "looking-glass:run"
		}
		return ""
	}
	if strings.HasPrefix(path, "/api/looking-glass/") {
		if read || r.Method == "POST" && path == "/api/looking-glass/queries" {
			return "looking-glass:read"
		}
		return ""
	}
	if path == "/api/devices/bindings" {
		return "devices:write"
	}
	if strings.HasPrefix(path, "/api/integrations") {
		if strings.HasSuffix(path, "/apply") {
			return ""
		}
		if read {
			return "integrations:read"
		}
		return "integrations:write"
	}
	if strings.HasPrefix(path, "/api/devices") {
		if read {
			return "devices:read"
		}
		if path == "/api/devices/discoveries" || path == "/api/devices/observations" {
			return "devices:write"
		}
		return ""
	}
	if ipamScope(path) {
		if read {
			return "resources:read"
		}
		return "resources:write"
	}
	if path == "/api/monitoring/bindings" || path == "/api/devices/bindings" {
		if path == "/api/devices/bindings" {
			return "devices:write"
		}
		return "monitoring:write"
	}
	if strings.HasPrefix(path, "/api/monitoring/") {
		if read {
			return "monitoring:read"
		}
		return "monitoring:write"
	}
	if strings.HasPrefix(path, "/api/policy/") {
		if read || path == "/api/policy/simulate" || path == "/api/policy/candidates" {
			return "policy:read"
		}
		return "policy:write"
	}
	if strings.HasPrefix(path, "/api/resources/") {
		kind := strings.Split(strings.TrimPrefix(path, "/api/resources/"), "/")[0]
		if kind == provisionProfileKind {
			if read {
				return "provisioning:read"
			}
			return ""
		}
		if kind == "users" {
			return ""
		}
		if read {
			return "resources:read"
		}
		return "resources:write"
	}
	if path == "/api/dashboard" || path == "/api/schema" || path == "/api/router-platforms" {
		if read {
			return "resources:read"
		}
		return ""
	}
	if path == "/api/deployments" || strings.HasPrefix(path, "/api/deployments/") {
		if strings.HasSuffix(path, "/approve") {
			return ""
		}
		if strings.HasSuffix(path, "/config") && read {
			return "deployments:pull"
		}
		if read {
			return "deployments:read"
		}
		return "deployments:write"
	}
	if path == "/api/provisioning" || strings.HasPrefix(path, "/api/provisioning/") {
		if strings.HasSuffix(path, "/approve") || strings.HasSuffix(path, "/prune") || r.Method == "DELETE" {
			return ""
		}
		if read && strings.HasSuffix(path, "/config") {
			return "provisioning:pull"
		}
		if read {
			return "provisioning:read"
		}
		return "provisioning:write"
	}
	if path == "/api/audit" && read {
		return "audit:read"
	}
	_, publicIXF, isIXF := ixfRoute(path)
	if (isIXF && !publicIXF || path == "/api/export") && read {
		return "exports:read"
	}
	if path == "/api/documents" || strings.HasPrefix(path, "/api/documents/") || path == "/api/directories" || strings.HasPrefix(path, "/api/directories/") {
		if read {
			return "documents:read"
		}
		return "documents:write"
	}
	return ""
}
func (a *App) authenticateToken(raw string) (Record, Session, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "mdt" || len(raw) > 256 {
		return nil, Session{}, false
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return nil, Session{}, false
	}
	s := &a.store.state
	hash := digest(raw)
	for id, security := range s.Security {
		for _, t := range security.Tokens {
			if t.ID != parts[1] || !time.Now().Before(t.ExpiresAt) || subtle.ConstantTimeCompare([]byte(hash), []byte(t.Hash)) != 1 {
				continue
			}
			user := find(s, "users", id)
			if user == nil {
				return nil, Session{}, false
			}
			memberID := t.MemberID
			if str(user, "role") == "member" {
				if memberID == "" && len(membershipIDs(user)) == 1 {
					memberID = selectedMember(user, "")
				}
				if !contains(membershipIDs(user), memberID) {
					return nil, Session{}, false
				}
			}
			// Detach this principal, not the entire control-plane inventory, for
			// each collector request. Mutable session slices remain private.
			encoded, _ := json.Marshal(withMember(user, memberID))
			detached := Record{}
			_ = json.Unmarshal(encoded, &detached)
			return detached, Session{ProfileIDs: append([]string(nil), t.ProfileIDs...), UserID: id, API: true, Scopes: append([]string(nil), t.Scopes...), Generation: security.Generation, TokenID: t.ID, MemberID: memberID, RouterIDs: append([]string(nil), t.RouterIDs...), SwitchIDs: append([]string(nil), t.SwitchIDs...)}, true
		}
	}
	return nil, Session{}, false
}
func publicToken(t APIToken) Record {
	return Record{"id": t.ID, "name": t.Name, "scopes": t.Scopes, "createdAt": t.CreatedAt, "expiresAt": t.ExpiresAt, "memberId": t.MemberID, "routerIds": t.RouterIDs, "switchIds": t.SwitchIDs, "profileIds": t.ProfileIDs}
}

func (a *App) account(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	id := str(user, "id")
	path := strings.TrimPrefix(r.URL.Path, "/api/account/")
	if r.Method == "GET" {
		s := a.store.Snapshot()
		security := s.Security[id]
		switch path {
		case "security":
			respond(w, 200, Record{"mfaEnabled": security.MFAEnabled, "recoveryCodesRemaining": len(security.RecoveryHashes), "availableScopes": availableScopes(str(user, "role"))})
			return
		case "tokens":
			items := []Record{}
			for _, t := range security.Tokens {
				items = append(items, publicToken(t))
			}
			respond(w, 200, Record{"items": items})
			return
		case "sessions":
			items := []Record{}
			a.mu.Lock()
			for _, v := range a.sessions {
				if v.UserID == id && v.Generation == security.Generation && time.Now().Before(v.Expires) {
					items = append(items, Record{"id": v.ID, "createdAt": v.CreatedAt, "expiresAt": v.Expires, "ip": v.IP, "userAgent": v.UserAgent, "current": v.ID == session.ID})
				}
			}
			a.mu.Unlock()
			sort.Slice(items, func(i, j int) bool { return items[i]["createdAt"].(time.Time).After(items[j]["createdAt"].(time.Time)) })
			respond(w, 200, Record{"items": items})
			return
		}
		problem(w, 404, "account route not found")
		return
	}
	if r.Method == "DELETE" && (strings.HasPrefix(path, "sessions/") || strings.HasPrefix(path, "tokens/")) {
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			problem(w, 404, "not found")
			return
		}
		found := false
		self := false
		if parts[0] == "sessions" {
			var selectedKey string
			removed := map[string]bool{}
			err := a.store.Update(func(s *State) error {
				if _, err := a.rememberedActor(s, user, session); err != nil {
					return err
				}
				a.mu.Lock()
				for key, v := range a.sessions {
					if v.UserID == id && v.ID == parts[1] {
						selectedKey, found, self = key, true, v.ID == session.ID
						if v.RememberDeviceID != "" {
							removed[v.RememberDeviceID] = true
							forgetRememberedDevice(s, v.RememberDeviceID)
						}
						break
					}
				}
				a.mu.Unlock()
				return nil
			})
			if err != nil {
				problem(w, 403, "session authority changed")
				return
			}
			a.revokeRememberedSessions(removed)
			a.mu.Lock()
			delete(a.sessions, selectedKey)
			a.mu.Unlock()
		} else {
			err := a.store.Update(func(s *State) error {
				u := s.Security[id]
				for i, t := range u.Tokens {
					if t.ID == parts[1] {
						u.Tokens = append(u.Tokens[:i], u.Tokens[i+1:]...)
						found = true
						break
					}
				}
				s.Security[id] = u
				if found {
					audit(s, id, "revoke-token", "users", id)
				}
				return nil
			})
			if err != nil {
				problem(w, 500, "could not persist revocation")
				return
			}
		}
		if !found {
			problem(w, 404, "not found")
			return
		}
		if self {
			clearCookie(w, a.secureCookie)
		}
		respond(w, 200, Record{"ok": true})
		return
	}
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry in 10 minutes")
		return
	}
	var body struct {
		CurrentPassword string   `json:"currentPassword"`
		NewPassword     string   `json:"newPassword"`
		Code            string   `json:"code"`
		Name            string   `json:"name"`
		Scopes          []string `json:"scopes"`
		RouterIDs       []string `json:"routerIds"`
		SwitchIDs       []string `json:"switchIds"`
		ProfileIDs      []string `json:"profileIds"`
		ExpiresInDays   int      `json:"expiresInDays"`
	}
	if !decode(w, r, &body) {
		return
	}
	var response = Record{"ok": true}
	logout := false
	revokeOthers := false
	var generation uint64
	if path == "totp/verify" {
		err := a.store.Update(func(s *State) error {
			u := s.Security[id]
			if u.MFAEnabled || u.PendingSecret == "" || time.Now().After(u.PendingUntil) {
				return errors.New("enrollment is missing or expired")
			}
			secret, err := a.decryptSecret(id, u.PendingSecret)
			if err != nil {
				return err
			}
			step, ok := matchingStep(secret, body.Code, time.Now(), -1)
			if !ok {
				return errors.New("invalid authentication code")
			}
			u.Secret = u.PendingSecret
			u.PendingSecret = ""
			u.PendingUntil = time.Time{}
			u.MFAEnabled = true
			u.LastStep = step
			codes, hashes := recoveryCodes()
			u.RecoveryHashes = hashes
			u.Tokens = nil
			u.Generation++
			generation = u.Generation
			s.Security[id] = u
			audit(s, id, "enable-mfa", "users", id)
			response = Record{"recoveryCodes": codes}
			return nil
		})
		if err != nil {
			problem(w, 403, err.Error())
			return
		}
		cookie, _ := r.Cookie("moedove_session")
		a.keepSession(id, cookie.Value, generation)
		respond(w, 200, response)
		return
	}
	if path == "password" && (len(body.NewPassword) < 12 || len(body.NewPassword) > 128) {
		problem(w, 400, "new password must contain 12 to 128 bytes")
		return
	}
	var newHash string
	if path == "password" {
		newHash = hashPassword(body.NewPassword)
	}
	var secret, encrypted string
	if path == "totp/enroll" {
		raw := make([]byte, 20)
		if _, err := rand.Read(raw); err != nil {
			problem(w, 500, "entropy unavailable")
			return
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		var err error
		encrypted, err = a.encryptSecret(id, secret)
		if err != nil {
			problem(w, 500, "could not encrypt enrollment")
			return
		}
	}
	err := a.reauth(id, body.CurrentPassword, body.Code, func(s *State, u *SecurityUser) error {
		switch path {
		case "password":
			find(s, "users", id)["passwordHash"] = newHash
			u.Generation++
			u.Tokens = nil
			logout = true
		case "totp/enroll":
			if u.MFAEnabled {
				return errors.New("MFA already enabled")
			}
			u.PendingSecret = encrypted
			u.PendingUntil = time.Now().Add(10 * time.Minute)
			issuer := "MoeDove IXP"
			values := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
			response = Record{"secret": secret, "otpauthUrl": "otpauth://totp/" + url.PathEscape(issuer+":"+str(user, "email")) + "?" + values.Encode(), "expiresAt": u.PendingUntil}
		case "totp/disable":
			if !u.MFAEnabled {
				return errors.New("MFA is not enabled")
			}
			u.MFAEnabled = false
			u.Secret = ""
			u.PendingSecret = ""
			u.RecoveryHashes = nil
			u.Generation++
			u.Tokens = nil
			logout = true
		case "recovery-codes":
			forgetUserRememberedDevices(s, id)
			if !u.MFAEnabled {
				return errors.New("MFA is not enabled")
			}
			codes, hashes := recoveryCodes()
			u.RecoveryHashes = hashes
			response = Record{"recoveryCodes": codes}
		case "sessions/revoke-others":
			revokeOthers = true
			u.Generation++
			generation = u.Generation
		case "tokens":
			if len(u.Tokens) >= 50 {
				return errors.New("revoke an existing token before creating more")
			}
			if strings.TrimSpace(body.Name) == "" || len(body.Name) > 100 {
				return errors.New("token name must be 1..100 bytes")
			}
			days := body.ExpiresInDays
			if days == 0 {
				days = 30
			}
			if days < 1 || days > 365 {
				return errors.New("token expiry must be 1..365 days")
			}
			scopes := body.Scopes
			if len(scopes) == 0 {
				scopes = []string{"resources:read"}
			}
			issuerRole := str(find(s, "users", id), "role")
			allowed := availableScopes(issuerRole)
			seen := map[string]bool{}
			for _, scope := range scopes {
				if !contains(allowed, scope) || seen[scope] {
					return errors.New("invalid or unavailable token scope")
				}
				seen[scope] = true
			}
			if seen["sflow:forward"] && !seen["sflow:write"] {
				return errors.New("sflow:forward also requires sflow:write and explicit switch bindings")
			}
			if issuerRole == "member" && len(body.RouterIDs)+len(body.SwitchIDs)+len(body.ProfileIDs) > 0 {
				return errors.New("member tokens use their membership scope, not staff resource bindings")
			}
			routerIDs := []string{}
			for _, routerID := range body.RouterIDs {
				if contains(routerIDs, routerID) || find(s, "routers", routerID) == nil {
					return errors.New("routerIds must contain distinct configured routers")
				}
				routerIDs = append(routerIDs, routerID)
			}
			if (seen["monitoring:write"] || seen["deployments:pull"] || seen["looking-glass:run"]) && len(routerIDs) == 0 {
				return errors.New("collector, looking-glass runner and deployment pull tokens require explicit routerIds")
			}
			if !seen["monitoring:write"] && !seen["deployments:pull"] && !seen["looking-glass:run"] && len(routerIDs) > 0 {
				return errors.New("routerIds requires monitoring:write, looking-glass:run or deployments:pull scope")
			}
			switchIDs := []string{}
			for _, switchID := range body.SwitchIDs {
				if contains(switchIDs, switchID) || find(s, "switches", switchID) == nil {
					return errors.New("switchIds must contain distinct configured switches")
				}
				switchIDs = append(switchIDs, switchID)
			}
			if (seen["devices:read"] || seen["devices:write"] || seen["core:read"] || seen["sflow:write"] || seen["sflow:read"] && issuerRole != "member") && len(switchIDs) == 0 {
				return errors.New("device, core and staff sFlow tokens require explicit switchIds")
			}
			if !seen["devices:read"] && !seen["devices:write"] && !seen["core:read"] && !seen["sflow:read"] && !seen["sflow:write"] && len(switchIDs) > 0 {
				return errors.New("switchIds requires a devices, core:read or sFlow scope")
			}
			sort.Strings(scopes)
			profileIDs := []string{}
			for _, profileID := range body.ProfileIDs {
				if contains(profileIDs, profileID) || find(s, provisionProfileKind, profileID) == nil {
					return errors.New("profileIds must contain distinct configured profiles")
				}
				profileIDs = append(profileIDs, profileID)
			}
			if (seen["provisioning:write"] || seen["provisioning:pull"]) && len(profileIDs) == 0 {
				return errors.New("provisioning write and pull tokens require explicit profileIds")
			}
			if !seen["provisioning:write"] && !seen["provisioning:pull"] && len(profileIDs) > 0 {
				return errors.New("profileIds requires provisioning:write or provisioning:pull")
			}
			tid := token(12)
			raw := "mdt." + tid + "." + token(32)
			t := APIToken{ProfileIDs: profileIDs, SwitchIDs: switchIDs, MemberID: str(user, "memberId"), RouterIDs: routerIDs, ID: tid, Name: strings.TrimSpace(body.Name), Hash: digest(raw), Scopes: scopes, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)}
			u.Tokens = append(u.Tokens, t)
			response = Record{"token": raw, "item": publicToken(t)}
		default:
			return errors.New("account action not found")
		}
		audit(s, id, path, "users", id)
		return nil
	})
	if err != nil {
		problem(w, 403, err.Error())
		return
	}
	if path == "recovery-codes" {
		cookie, _ := r.Cookie("moedove_session")
		if cookie != nil {
			a.detachRememberedSession(id, cookie.Value)
		}
	}
	if logout {
		a.revokeSessions(id, "")
		clearCookie(w, a.secureCookie)
	} else if revokeOthers {
		cookie, _ := r.Cookie("moedove_session")
		a.keepSession(id, cookie.Value, generation)
	}
	respond(w, 200, response)
}

// Forwarded client addresses are accepted only through configured proxy hops.
func (a *App) sessionMetadata(r *http.Request) (string, string) {
	ip := a.clientIP(r)
	agent := r.UserAgent()
	if len(agent) > 256 {
		agent = agent[:256]
	}
	return ip, agent
}

// A well-formed but wrong key is as unusable as a missing key. Detect mismatched
// restores before accepting requests instead of locking MFA users out later.
func (a *App) validateSecurityEncryption() error {
	s := a.store.Snapshot()
	for id, c := range s.PeerCredentials {
		if c.Ciphertext == "" {
			continue
		}
		if _, err := a.bgpPassword(id, c); err != nil {
			return errors.New("security.key cannot decrypt BGP credentials; restore the matching key and state backup")
		}
	}
	for id, security := range s.Security {
		for _, encrypted := range []string{security.Secret, security.PendingSecret} {
			if encrypted == "" {
				continue
			}
			if _, err := a.decryptSecret(id, encrypted); err != nil {
				return errors.New("security.key cannot decrypt account secrets; restore the matching key and state backup")
			}
		}
	}
	return nil
}
