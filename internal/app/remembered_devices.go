package app

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const rememberedCookieName = "__Host-moedove_remember"
const rememberedLifetime = 30 * 24 * time.Hour
const rememberedRaceWindow = 30 * time.Second
const maxRememberedDevices = 10000
const maxUserRememberedDevices = 10

var errRememberedCapacity = errors.New("remembered device limit reached; sign in without remembering and revoke an old device")

// Validators are random browser secrets. Only hashes are persisted. The prior
// hash identifies a concurrent stale request; it never authorizes a login.
type RememberedDevice struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	UserCreatedAt  string    `json:"userCreatedAt"`
	Generation     uint64    `json:"generation"`
	AccountBinding string    `json:"accountBinding"`
	ValidatorHash  string    `json:"validatorHash"`
	PreviousHash   string    `json:"previousHash,omitempty"`
	RotatedAt      time.Time `json:"rotatedAt,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	LastUsedAt     time.Time `json:"lastUsedAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
	IP             string    `json:"ip"`
	UserAgent      string    `json:"userAgent"`
}

func (a *App) rememberDeviceAvailable(r *http.Request) bool { return a.secureCookie || r.TLS != nil }
func rememberedBinding(s *State, user Record) string {
	security := s.Security[str(user, "id")]
	raw, _ := json.Marshal(Record{"id": user["id"], "createdAt": user["createdAt"], "email": user["email"], "passwordHash": user["passwordHash"], "role": user["role"], "memberId": user["memberId"], "memberIds": user["memberIds"], "memberAdminIds": user["memberAdminIds"], "mfaEnabled": security.MFAEnabled, "mfaSecret": security.Secret})
	return digest(string(raw))
}
func rememberedDevice(s *State, id string) *RememberedDevice {
	for i := range s.RememberedDevices {
		if s.RememberedDevices[i].ID == id {
			return &s.RememberedDevices[i]
		}
	}
	return nil
}
func rememberedDeviceCurrent(s *State, d *RememberedDevice, now time.Time) bool {
	if d == nil || now.Before(d.CreatedAt) || !now.Before(d.ExpiresAt) {
		return false
	}
	u := find(s, "users", d.UserID)
	return u != nil && str(u, "createdAt") == d.UserCreatedAt && s.Security[d.UserID].Generation == d.Generation && rememberedBinding(s, u) == d.AccountBinding
}
func rememberedSessionCurrent(s *State, session Session, now time.Time) bool {
	if session.RememberDeviceID == "" {
		return true
	}
	d := rememberedDevice(s, session.RememberDeviceID)
	return d != nil && d.UserID == session.UserID && d.UserCreatedAt == session.UserCreatedAt && d.Generation == session.Generation && rememberedDeviceCurrent(s, d, now)
}
func pruneRememberedDevices(s *State) {
	if len(s.RememberedDevices) == 0 {
		return
	}
	kept := s.RememberedDevices[:0]
	now := time.Now().UTC()
	for _, d := range s.RememberedDevices {
		if rememberedDeviceCurrent(s, &d, now) {
			kept = append(kept, d)
		}
	}
	s.RememberedDevices = kept
}
func forgetRememberedDevice(s *State, id string) bool {
	for i, d := range s.RememberedDevices {
		if d.ID == id {
			s.RememberedDevices = append(s.RememberedDevices[:i], s.RememberedDevices[i+1:]...)
			return true
		}
	}
	return false
}
func forgetUserRememberedDevices(s *State, userID string) {
	kept := s.RememberedDevices[:0]
	for _, d := range s.RememberedDevices {
		if d.UserID != userID {
			kept = append(kept, d)
		}
	}
	s.RememberedDevices = kept
}
func rememberedHash(id, validator string) string {
	return digest("remembered-device:" + id + ":" + validator)
}
func rememberedCredential(r *http.Request) (string, string, bool) {
	cookie, err := r.Cookie(rememberedCookieName)
	if err != nil || len(cookie.Value) != 72 {
		return "", "", false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] != "rmd" || len(parts[1]) != 24 || len(parts[2]) != 43 || !identifier.MatchString(parts[1]) || !identifier.MatchString(parts[2]) {
		return "", "", false
	}
	return parts[1], rememberedHash(parts[1], parts[2]), true
}
func presentedRememberedDevice(s *State, r *http.Request) *RememberedDevice {
	id, hash, ok := rememberedCredential(r)
	if !ok {
		return nil
	}
	d := rememberedDevice(s, id)
	if d == nil || !hmac.Equal([]byte(d.ValidatorHash), []byte(hash)) {
		return nil
	}
	return d
}

// A completed password/MFA login may retire the immediately preceding browser
// grant even when another tab just rotated it. This is revocation only: the
// previous validator never establishes a session or authorizes another action.
func presentedRememberedLoginCleanup(s *State, r *http.Request) *RememberedDevice {
	if d := presentedRememberedDevice(s, r); d != nil {
		return d
	}
	id, hash, ok := rememberedCredential(r)
	if !ok {
		return nil
	}
	d := rememberedDevice(s, id)
	now := time.Now().UTC()
	if d == nil || d.PreviousHash == "" || now.Before(d.RotatedAt) || !now.Before(d.RotatedAt.Add(rememberedRaceWindow)) || !hmac.Equal([]byte(d.PreviousHash), []byte(hash)) {
		return nil
	}
	return d
}
func rememberedCookie(id, validator string, expires time.Time) *http.Cookie {
	remaining := int(time.Until(expires).Seconds())
	if remaining < 1 {
		remaining = 1
	}
	return &http.Cookie{Name: rememberedCookieName, Value: "rmd." + id + "." + validator, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: remaining, Expires: expires}
}
func clearRememberedCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: rememberedCookieName, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func (a *App) newRememberedDevice(s *State, user Record, r *http.Request) (*RememberedDevice, *http.Cookie, error) {
	pruneRememberedDevices(s)
	count := 0
	for _, d := range s.RememberedDevices {
		if d.UserID == str(user, "id") {
			count++
		}
	}
	if len(s.RememberedDevices) >= maxRememberedDevices || count >= maxUserRememberedDevices {
		return nil, nil, errRememberedCapacity
	}
	now := time.Now().UTC()
	id, validator := token(18), token(32)
	ip, agent := a.sessionMetadata(r)
	d := RememberedDevice{ID: id, UserID: str(user, "id"), UserCreatedAt: str(user, "createdAt"), Generation: s.Security[str(user, "id")].Generation, AccountBinding: rememberedBinding(s, user), ValidatorHash: rememberedHash(id, validator), CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(rememberedLifetime), IP: ip, UserAgent: strings.ToValidUTF8(agent, "")}
	s.RememberedDevices = append(s.RememberedDevices, d)
	if err := validateRememberedDevices(s); err != nil {
		return nil, nil, err
	}
	return &d, rememberedCookie(id, validator, d.ExpiresAt), nil
}
func validateRememberedDevices(s *State) error {
	if len(s.RememberedDevices) > maxRememberedDevices {
		return errors.New("remembered device state exceeds capacity")
	}
	ids, users := map[string]bool{}, map[string]int{}
	for _, d := range s.RememberedDevices {
		users[d.UserID]++
		if len(d.ID) != 24 || !identifier.MatchString(d.ID) || ids[d.ID] || !identifier.MatchString(d.UserID) || !noteTimestamp(d.UserCreatedAt) || !digestPattern.MatchString(d.AccountBinding) || !digestPattern.MatchString(d.ValidatorHash) || d.CreatedAt.IsZero() || d.LastUsedAt.Before(d.CreatedAt) || !d.LastUsedAt.Before(d.ExpiresAt) || d.ExpiresAt.Sub(d.CreatedAt) != rememberedLifetime || len(d.IP) > 64 || len(d.UserAgent) > 256 || users[d.UserID] > maxUserRememberedDevices {
			return errors.New("invalid remembered device identity, expiry or metadata")
		}
		if d.PreviousHash == "" {
			if !d.RotatedAt.IsZero() {
				return errors.New("remembered rotation missing prior hash")
			}
		} else if !digestPattern.MatchString(d.PreviousHash) || d.PreviousHash == d.ValidatorHash || d.RotatedAt.Before(d.CreatedAt) || d.RotatedAt.After(d.LastUsedAt) || !d.RotatedAt.Before(d.ExpiresAt) {
			return errors.New("invalid remembered device rotation")
		}
		ids[d.ID] = true
	}
	return nil
}
func (a *App) revokeRememberedSessions(ids map[string]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, session := range a.sessions {
		if ids[session.RememberDeviceID] {
			delete(a.sessions, key)
		}
	}
}

// Restore deliberately does not reactivate browser grants from an older
// backup. Normal service restarts keep the current persisted grants.
func (a *App) InvalidateRememberedDevicesAfterRestore() error {
	if len(a.store.Snapshot().RememberedDevices) == 0 {
		return nil
	}
	return a.store.Update(func(s *State) error { s.RememberedDevices = nil; return nil })
}

func (a *App) detachRememberedSession(userID, cookie string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session, ok := a.sessions[cookie]; ok && session.UserID == userID {
		session.RememberDeviceID = ""
		a.sessions[cookie] = session
	}
}

func (a *App) rememberedActor(s *State, user Record, session Session) (Record, error) {
	if session.API {
		return nil, errors.New("remembered devices require a browser session")
	}
	actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
	if err != nil || str(actor, "createdAt") != str(user, "createdAt") {
		return nil, errors.New("account authority changed")
	}
	return actor, nil
}
