package app

import (
	"errors"
	"time"
)

var errAtlasAuthentication = errors.New("Atlas authentication or authority changed; sign in again")

// Call while holding the Store read/write lock. Lock order is Store -> sessions,
// matching security mutations; no session mutex is held during provider I/O.
func (a *App) atlasActor(s *State, user Record, session Session, roles ...string) (Record, error) {
	if session.UserID == "" || session.UserID != str(user, "id") {
		return nil, errAtlasAuthentication
	}
	actor, err := lookingGlassActor(s, user, session)
	if err != nil || !contains(roles, str(actor, "role")) {
		return nil, errAtlasAuthentication
	}
	if !session.API {
		valid := false
		a.mu.Lock()
		for _, current := range a.sessions {
			if current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation && time.Now().Before(current.Expires) {
				valid = true
				break
			}
		}
		a.mu.Unlock()
		if !valid {
			return nil, errAtlasAuthentication
		}
	}
	return actor, nil
}
func (a *App) atlasActorCurrent(user Record, session Session, roles ...string) (Record, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return nil, errAtlasAuthentication
	}
	return a.atlasActor(&a.store.state, user, session, roles...)
}
