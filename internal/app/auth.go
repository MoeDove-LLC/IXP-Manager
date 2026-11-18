package app

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Session struct {
	ProfileIDs                           []string
	SwitchIDs                            []string
	ID, IP, UserAgent, TokenID, MemberID string
	RouterIDs                            []string
	CreatedAt                            time.Time
	Generation                           uint64
	API                                  bool
	Scopes                               []string
	UserID, CSRF                         string
	UserCreatedAt, RememberDeviceID      string
	Expires                              time.Time
}
type attempt struct {
	Count int
	Until time.Time
}

func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hashPassword(password string) string {
	salt := token(16)
	key, _ := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return salt + "." + base64.RawStdEncoding.EncodeToString(key)
}
func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	key, _ := pbkdf2.Key(sha256.New, password, []byte(parts[0]), 600000, 32)
	return hmac.Equal(expected, key)
}
func (a *App) authenticate(r *http.Request) (Record, Session, bool) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if !strings.HasPrefix(auth, "Bearer ") {
			return nil, Session{}, false
		}
		return a.authenticateToken(strings.TrimPrefix(auth, "Bearer "))
	}
	cookie, err := r.Cookie("moedove_session")
	if err != nil {
		return nil, Session{}, false
	}
	a.mu.Lock()
	session, ok := a.sessions[cookie.Value]
	if ok && time.Now().After(session.Expires) {
		delete(a.sessions, cookie.Value)
		ok = false
	}
	a.mu.Unlock()
	if !ok {
		return nil, Session{}, false
	}
	s := a.store.Snapshot()
	u := find(&s, "users", session.UserID)
	if u == nil || s.Security[session.UserID].Generation != session.Generation || session.UserCreatedAt != "" && str(u, "createdAt") != session.UserCreatedAt || !rememberedSessionCurrent(&s, session, time.Now()) {
		return nil, Session{}, false
	}
	if str(u, "role") == "member" {
		if session.MemberID == "" {
			session.MemberID = selectedMember(u, "")
		}
		if !contains(membershipIDs(u), session.MemberID) {
			return nil, Session{}, false
		}
	}
	return withMember(u, session.MemberID), session, true
}
func (a *App) issue(w http.ResponseWriter, r *http.Request, user Record) {
	s := a.store.Snapshot()
	a.issueVersion(w, r, user, s.Security[str(user, "id")].Generation)
}
func (a *App) issueVersion(w http.ResponseWriter, r *http.Request, user Record, generation uint64) {
	a.issueDeviceSession(w, r, user, generation, "", nil)
}
func (a *App) issueDeviceSession(w http.ResponseWriter, r *http.Request, user Record, generation uint64, deviceID string, rememberCookie *http.Cookie) {
	ip, agent := a.sessionMetadata(r)
	session := Session{ID: token(16), UserID: str(user, "id"), UserCreatedAt: str(user, "createdAt"), RememberDeviceID: deviceID, CSRF: token(32), Expires: time.Now().Add(12 * time.Hour), CreatedAt: time.Now().UTC(), IP: ip, UserAgent: agent, Generation: generation, MemberID: selectedMember(user, "")}
	a.mu.Lock()
	for k, s := range a.sessions {
		if s.Expires.Before(time.Now()) {
			delete(a.sessions, k)
		}
	}
	id := token(32)
	a.sessions[id] = session
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "moedove_session", Value: id, Path: "/", HttpOnly: true, Secure: a.secureCookie || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	if rememberCookie != nil {
		http.SetCookie(w, rememberCookie)
	}
	respond(w, 200, Record{"user": withMember(user, session.MemberID), "csrfToken": session.CSRF})
}

func (a *App) limited(r *http.Request) bool {
	ip := a.clientIP(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[ip]
	if v.Count == 0 {
		v.Until = now.Add(10 * time.Minute)
	}
	v.Count++
	a.attempts[ip] = v
	return v.Count > 20
}
func (a *App) authRoute(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/session/restore":
		a.restoreRememberedSession(w, r)
		return true
	case "/api/setup":
		if r.Method == "GET" {
			respond(w, 200, Record{"required": len(a.store.Snapshot().Resources["users"]) == 0, "rememberDeviceAvailable": a.rememberDeviceAvailable(r), "rememberDeviceDays": 30})
			return true
		}
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return true
		}
		if a.limited(r) {
			problem(w, 429, "too many attempts; retry in 10 minutes")
			return true
		}
		var body struct {
			Name       string  `json:"name"`
			Email      string  `json:"email"`
			Password   string  `json:"password"`
			IXPName    string  `json:"ixpName"`
			ASN        float64 `json:"asn"`
			SetupToken string  `json:"setupToken"`
		}
		if !decode(w, r, &body) {
			return true
		}
		if subtle.ConstantTimeCompare([]byte(body.SetupToken), []byte(a.setupToken)) != 1 {
			problem(w, 403, "invalid setup token; use the token printed when the service started")
			return true
		}
		user := Record{"id": token(16), "name": body.Name, "email": strings.ToLower(body.Email), "role": "admin", "password": body.Password, "createdAt": time.Now().UTC().Format(time.RFC3339)}
		if len(body.Password) < 12 || len(body.Password) > 128 {
			problem(w, 400, "password must contain 12 to 128 bytes")
			return true
		}
		user["passwordHash"] = hashPassword(body.Password)
		err := a.store.Update(func(s *State) error {
			if len(s.Resources["users"]) != 0 {
				return fmt.Errorf("setup already completed")
			}
			if err := validate(s, "users", user); err != nil {
				return err
			}
			if body.ASN < 1 || body.ASN > 4294967294 || body.ASN != float64(uint32(body.ASN)) {
				return fmt.Errorf("invalid IXP ASN")
			}
			if strings.TrimSpace(body.IXPName) == "" {
				return fmt.Errorf("IXP name is required")
			}
			delete(user, "password")
			s.Resources["users"] = append(s.Resources["users"], user)
			s.Settings = Record{"name": body.IXPName, "asn": body.ASN}
			audit(s, str(user, "id"), "setup", "settings", "")
			return nil
		})
		if err != nil {
			problem(w, 400, err.Error())
		} else {
			a.issue(w, r, user)
		}
		return true
	case "/api/login":
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return true
		}
		if a.limited(r) {
			problem(w, 429, "too many attempts; retry in 10 minutes")
			return true
		}
		var body struct {
			Email          string `json:"email"`
			Password       string `json:"password"`
			Code           string `json:"code"`
			RememberDevice bool   `json:"rememberDevice"`
		}
		if !decode(w, r, &body) {
			return true
		}
		if body.RememberDevice && !a.rememberDeviceAvailable(r) {
			problem(w, 400, "remembered sign-in requires HTTPS and secure-cookie configuration")
			return true
		}
		if len(body.Password) > 128 {
			problem(w, 401, "invalid email or password")
			return true
		}
		s := a.store.Snapshot()
		var user Record
		hash := a.dummyHash
		for _, u := range s.Resources["users"] {
			if strings.EqualFold(str(u, "email"), body.Email) {
				user = u
				hash = str(u, "passwordHash")
				break
			}
		}
		if !checkPassword(hash, body.Password) || user == nil {
			problem(w, 401, "invalid email or password")
			return true
		}
		userID := str(user, "id")
		if s.Security[userID].MFAEnabled && body.Code == "" {
			respond(w, 401, Record{"error": "authentication code required", "mfaRequired": true})
			return true
		}
		generationBefore := s.Security[userID].Generation
		var generation uint64
		var deviceID string
		var rememberCookie *http.Cookie
		removed := map[string]bool{}
		err := a.store.Update(func(s *State) error {
			current := find(s, "users", userID)
			if current == nil || str(current, "passwordHash") != hash || str(current, "createdAt") != str(user, "createdAt") || s.Security[userID].Generation != generationBefore {
				return fmt.Errorf("account changed")
			}
			security := s.Security[userID]
			if err := a.consumeMFA(userID, &security, body.Code, time.Now()); err != nil {
				return err
			}
			s.Security[userID] = security
			generation = security.Generation
			if previous := presentedRememberedLoginCleanup(s, r); previous != nil {
				removed[previous.ID] = true
				forgetRememberedDevice(s, previous.ID)
			}
			if body.RememberDevice {
				device, cookie, err := a.newRememberedDevice(s, current, r)
				if err != nil {
					return err
				}
				deviceID, rememberCookie = device.ID, cookie
			} else if _, err := r.Cookie(rememberedCookieName); err == nil {
				rememberCookie = &http.Cookie{Name: rememberedCookieName, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1}
			}
			audit(s, userID, "login", "users", userID)
			user = current
			return nil
		})
		if err != nil {
			if errors.Is(err, errRememberedCapacity) {
				problem(w, 409, err.Error())
				return true
			}
			problem(w, 401, "invalid credentials or authentication code")
			return true
		}
		a.revokeRememberedSessions(removed)
		a.issueDeviceSession(w, r, user, generation, deviceID, rememberCookie)
		return true
	}
	return false
}
