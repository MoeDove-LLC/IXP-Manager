package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Review tokens cover the stored record, including hidden fields. They are
// transport metadata and never become part of a resource or its data export.
func (a *App) resourceETag(kind string, record Record) string {
	if record == nil {
		return ""
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("resource-v1\x00" + kind + "\x00" + str(record, "id") + "\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func validResourceETag(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func (a *App) resourceView(s *State, kind string, visible Record) Record {
	view := Record{}
	for key, value := range visible {
		view[key] = value
	}
	view["_etag"] = a.resourceETag(kind, find(s, kind, str(visible, "id")))
	return view
}

// Wildcards and weak tags defeat the reviewed-version guarantee.
func resourcePrecondition(r *http.Request, body Record) (string, int, error) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	bodyTag := ""
	if raw, present := body["_etag"]; present {
		delete(body, "_etag")
		value, ok := raw.(string)
		if !ok || !validResourceETag(value) || r.Method != "PUT" {
			return "", 400, errors.New("_etag is read-only review metadata and may only be echoed on PUT")
		}
		bodyTag = value
	}
	if r.Method != "PUT" && r.Method != "DELETE" {
		if header != "" {
			return "", 400, errors.New("If-Match applies only to an existing resource")
		}
		return "", 0, nil
	}
	expected := ""
	if header != "" {
		if len(r.Header.Values("If-Match")) != 1 || len(header) != 66 || header[0] != '"' || header[len(header)-1] != '"' || !validResourceETag(header[1:len(header)-1]) {
			return "", 400, errors.New("If-Match must contain one exact strong ETag from the reviewed resource")
		}
		expected = header[1 : len(header)-1]
	}
	if bodyTag != "" {
		if expected != "" && expected != bodyTag {
			return "", 400, errors.New("If-Match and _etag refer to different resource revisions")
		}
		expected = bodyTag
	}
	if expected == "" {
		return "", http.StatusPreconditionRequired, errors.New("read this resource first and send its reviewed ETag in If-Match")
	}
	return expected, 0, nil
}

// Recheck authorization inside the same transaction as the mutation.
func resourceWriter(s *State, user Record, session Session, kind string) (int, error) {
	id := str(user, "id")
	actor := find(s, "users", id)
	if actor == nil || id == "" || session.UserID != id || s.Security[id].Generation != session.Generation {
		return 401, errors.New("authentication changed; sign in again")
	}
	role := str(actor, "role")
	if (role != "admin" && role != "operator") || (kind == "users" && role != "admin") {
		return 403, errors.New("resource write permission changed")
	}
	if session.API {
		if kind == "users" {
			return 403, errors.New("API tokens cannot modify accounts")
		}
		for _, token := range s.Security[id].Tokens {
			if token.ID == session.TokenID && time.Now().Before(token.ExpiresAt) && contains(token.Scopes, "resources:write") {
				return 0, nil
			}
		}
		return 401, errors.New("API token expired or was revoked")
	}
	if session.Expires.IsZero() || !time.Now().Before(session.Expires) {
		return 401, errors.New("session expired; sign in again")
	}
	return 0, nil
}
