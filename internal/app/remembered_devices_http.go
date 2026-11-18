package app

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func (a *App) restoreRememberedSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		problem(w, 403, "cross-origin restore rejected")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host || parsed.Scheme != "https" || parsed.User != nil {
			problem(w, 403, "origin rejected")
			return
		}
	}
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if user, session, ok := a.authenticate(r); ok && !session.API {
		if _, err := a.atlasActorCurrent(user, session, "admin", "operator", "viewer", "member"); err == nil {
			respond(w, 200, Record{"user": user, "csrfToken": session.CSRF})
			return
		}
	}
	if !a.rememberDeviceAvailable(r) {
		problem(w, 401, "remembered sign-in unavailable; sign in again")
		return
	}
	id, hash, ok := rememberedCredential(r)
	if !ok {
		problem(w, 401, "remembered sign-in unavailable; sign in again")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry in 10 minutes")
		return
	}
	var user Record
	var generation uint64
	var cookie *http.Cookie
	retrySession := false
	err := a.store.Update(func(s *State) error {
		d := rememberedDevice(s, id)
		now := time.Now().UTC()
		if !rememberedDeviceCurrent(s, d, now) {
			return errors.New("remembered sign-in unavailable")
		}
		if !hmac.Equal([]byte(d.ValidatorHash), []byte(hash)) {
			retrySession = d.PreviousHash != "" && now.Before(d.RotatedAt.Add(rememberedRaceWindow)) && hmac.Equal([]byte(d.PreviousHash), []byte(hash))
			return errors.New("remembered sign-in unavailable")
		}
		user = find(s, "users", d.UserID)
		if !contains([]string{"admin", "operator", "viewer", "member"}, str(user, "role")) {
			return errors.New("remembered account unavailable")
		}
		validator := token(32)
		d.PreviousHash, d.ValidatorHash = d.ValidatorHash, rememberedHash(d.ID, validator)
		d.RotatedAt, d.LastUsedAt = now, now
		d.IP, d.UserAgent = a.sessionMetadata(r)
		d.UserAgent = strings.ToValidUTF8(d.UserAgent, "")
		generation = d.Generation
		cookie = rememberedCookie(d.ID, validator, d.ExpiresAt)
		if err := validateRememberedDevices(s); err != nil {
			return err
		}
		audit(s, d.UserID, "restore-remembered-session", "users", d.UserID)
		return nil
	})
	if err != nil {
		// Never clear cookies or revoke a family on a rejected rotation. A stale
		// request from another tab must not erase the winning response's cookies.
		respond(w, 401, Record{"error": "remembered sign-in unavailable; sign in again", "retrySession": retrySession})
		return
	}
	a.issueDeviceSession(w, r, user, generation, id, cookie)
}
func (a *App) rememberedDevicesRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	path := r.URL.Path
	if path != "/api/account/remembered-devices" && !strings.HasPrefix(path, "/api/account/remembered-devices/") {
		return false
	}
	if session.API {
		problem(w, 403, "remembered devices require a browser session")
		return true
	}
	if r.Method == "GET" && path == "/api/account/remembered-devices" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		s := &a.store.state
		actor, err := a.rememberedActor(s, user, session)
		if err != nil || a.store.closed {
			problem(w, 403, "account authority changed")
			return true
		}
		items := []Record{}
		now := time.Now().UTC()
		for _, d := range s.RememberedDevices {
			if d.UserID == str(actor, "id") && d.UserCreatedAt == str(actor, "createdAt") && rememberedDeviceCurrent(s, &d, now) {
				items = append(items, Record{"id": d.ID, "createdAt": d.CreatedAt, "lastUsedAt": d.LastUsedAt, "expiresAt": d.ExpiresAt, "ip": d.IP, "userAgent": d.UserAgent, "current": d.ID == session.RememberDeviceID})
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i]["createdAt"].(time.Time).After(items[j]["createdAt"].(time.Time)) })
		respond(w, 200, Record{"items": items, "maxDevices": maxUserRememberedDevices, "hardExpiryDays": 30})
		return true
	}
	if r.Method != "DELETE" {
		problem(w, 405, "method not allowed")
		return true
	}
	id := strings.TrimPrefix(path, "/api/account/remembered-devices/")
	all := path == "/api/account/remembered-devices"
	if !all && !identifier.MatchString(id) {
		problem(w, 404, "remembered device not found")
		return true
	}
	status := 404
	removed := map[string]bool{}
	signedOut := false
	err := a.store.Update(func(s *State) error {
		actor, err := a.rememberedActor(s, user, session)
		if err != nil {
			status = 403
			return err
		}
		kept := s.RememberedDevices[:0]
		for _, d := range s.RememberedDevices {
			if d.UserID == str(actor, "id") && d.UserCreatedAt == str(actor, "createdAt") && (all || d.ID == id) {
				removed[d.ID] = true
				signedOut = signedOut || d.ID == session.RememberDeviceID
			} else {
				kept = append(kept, d)
			}
		}
		if !all && len(removed) == 0 {
			return errors.New("remembered device not found")
		}
		s.RememberedDevices = kept
		audit(s, str(actor, "id"), "revoke-remembered-devices", "users", str(actor, "id"))
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return true
	}
	a.revokeRememberedSessions(removed)
	// Revocation changes authority, not browser cookies: a delayed response
	// cannot erase a different device created by a concurrent full login.
	respond(w, 200, Record{"ok": true, "signedOut": signedOut})
	return true
}
func (a *App) logoutSession(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	removed := map[string]bool{}
	err := a.store.Update(func(s *State) error {
		if _, err := a.rememberedActor(s, user, session); err != nil {
			return err
		}
		if session.RememberDeviceID != "" && forgetRememberedDevice(s, session.RememberDeviceID) {
			removed[session.RememberDeviceID] = true
		}
		if d := presentedRememberedDevice(s, r); d != nil {
			removed[d.ID] = true
			forgetRememberedDevice(s, d.ID)
		}
		return nil
	})
	if err != nil {
		problem(w, 403, "account authority changed; sign in again")
		return
	}
	a.revokeRememberedSessions(removed)
	cookie, _ := r.Cookie("moedove_session")
	a.mu.Lock()
	if cookie != nil {
		delete(a.sessions, cookie.Value)
	}
	a.mu.Unlock()
	clearCookie(w, a.secureCookie)
	respond(w, 200, Record{"ok": true})
}
