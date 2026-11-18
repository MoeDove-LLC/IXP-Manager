package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routing"
)

// Credentials are separate from generic resources and exports. Empty ciphertext
// is a tombstone: removing a key must not reset its concurrency revision.
type BGPCredential struct {
	Revision     string `json:"revision"`
	PeerChecksum string `json:"peerChecksum"`
	Ciphertext   string `json:"ciphertext,omitempty"`
	UpdatedAt    string `json:"updatedAt"`
	UpdatedBy    string `json:"updatedBy"`
}

var credentialRevision = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

func peerCredentialChecksum(p Record) string {
	address := str(p, "address")
	if parsed, err := netip.ParseAddr(address); err == nil {
		address = parsed.String()
	}
	b, _ := json.Marshal([]any{str(p, "routerId"), str(p, "memberId"), address, number(p, "asn")})
	return digest(string(b))
}

func bgpCredentialAAD(peerID string, c BGPCredential) string {
	return "bgp-credential:" + peerID + ":" + c.Revision + ":" + c.PeerChecksum
}

func (a *App) bgpPassword(peerID string, c BGPCredential) (string, error) {
	secret, err := a.decryptSecret(bgpCredentialAAD(peerID, c), c.Ciphertext)
	if err != nil {
		return "", errors.New("BGP credential cannot be decrypted")
	}
	if err := routing.ValidateBGPPassword(secret); err != nil || secret == "" {
		return "", errors.New("stored BGP credential is invalid")
	}
	return secret, nil
}

func validateBGPCredentials(s *State) error {
	for id, c := range s.PeerCredentials {
		p := find(s, "peers", id)
		if p == nil || !credentialRevision.MatchString(c.Revision) || len(c.PeerChecksum) != 64 || c.UpdatedBy == "" {
			return errors.New("invalid persisted BGP credential metadata")
		}
		if _, err := time.Parse(time.RFC3339Nano, c.UpdatedAt); err != nil {
			return errors.New("invalid persisted BGP credential timestamp")
		}
		if c.Ciphertext != "" && (len(c.Ciphertext) > 1024 || peerCredentialChecksum(p) != c.PeerChecksum) {
			return errors.New("BGP credential no longer matches its configured peer")
		}
	}
	return nil
}

func guardPeerCredentialMutation(s *State, kind, method string, old, body Record) error {
	if kind != "peers" || method != "PUT" || old == nil {
		return nil
	}
	if c := s.PeerCredentials[str(old, "id")]; c.Ciphertext != "" && peerCredentialChecksum(body) != c.PeerChecksum {
		return errors.New("remove the BGP credential before changing the peer router, member, address or ASN")
	}
	return nil
}

func publicBGPCredential(peerID string, p Record, c BGPCredential) Record {
	return Record{"peerId": peerID, "configured": c.Ciphertext != "", "revision": c.Revision,
		"peerChecksum": peerCredentialChecksum(p), "updatedAt": c.UpdatedAt,
		"peer": Record{"id": peerID, "name": p["name"], "routerId": p["routerId"], "memberId": p["memberId"], "address": p["address"], "asn": p["asn"]}}
}

func (a *App) peerCredentials(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	role := str(user, "role")
	if session.API || (role != "admin" && role != "operator") {
		problem(w, 403, "BGP credential management requires an administrator or operator browser session")
		return
	}
	base := "/api/peer-credentials"
	id := strings.TrimPrefix(r.URL.Path, base+"/")
	if r.URL.Path == base && r.Method == "GET" {
		id = r.URL.Query().Get("peerId")
	} else if r.URL.Path == base || strings.Contains(id, "/") || id == "" {
		problem(w, 404, "credential route not found")
		return
	}
	snapshot := a.store.Snapshot()
	peer := find(&snapshot, "peers", id)
	if peer == nil {
		problem(w, 404, "peer not found")
		return
	}
	if r.Method == "GET" {
		respond(w, 200, publicBGPCredential(id, peer, snapshot.PeerCredentials[id]))
		return
	}
	if r.Method != "PUT" && r.Method != "DELETE" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.limited(r) {
		problem(w, 429, "too many attempts; retry in 10 minutes")
		return
	}
	var body struct {
		Secret               string  `json:"secret"`
		Generate             bool    `json:"generate"`
		ExpectedRevision     *string `json:"expectedRevision"`
		ExpectedPeerChecksum string  `json:"expectedPeerChecksum"`
		CurrentPassword      string  `json:"currentPassword"`
		Code                 string  `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExpectedRevision == nil || len(body.ExpectedPeerChecksum) != 64 {
		problem(w, 400, "expectedRevision and expectedPeerChecksum are required")
		return
	}
	secret := body.Secret
	if r.Method == "DELETE" {
		if secret != "" || body.Generate {
			problem(w, 400, "credential removal cannot supply a secret")
			return
		}
	} else {
		if body.Generate && secret != "" {
			problem(w, 400, "choose generation or an explicit secret")
			return
		}
		if body.Generate {
			secret = token(32)
		}
		if secret == "" {
			problem(w, 400, "supply a BGP credential or choose generation; use DELETE to remove authentication")
			return
		}
		if err := routing.ValidateBGPPassword(secret); err != nil {
			problem(w, 400, err.Error())
			return
		}
	}
	status := http.StatusForbidden
	reauthenticated := false
	var response Record
	err := a.reauth(str(user, "id"), body.CurrentPassword, body.Code, func(s *State, _ *SecurityUser) error {
		reauthenticated = true
		status = http.StatusInternalServerError
		// Recheck authority and identity under the same transaction as mutation.
		currentRole := str(find(s, "users", str(user, "id")), "role")
		if currentRole != "admin" && currentRole != "operator" {
			status = http.StatusForbidden
			return errors.New("credential management permission changed")
		}
		p := find(s, "peers", id)
		if p == nil {
			status = http.StatusNotFound
			return errors.New("peer not found")
		}
		previous := s.PeerCredentials[id]
		if previous.Revision != *body.ExpectedRevision || peerCredentialChecksum(p) != body.ExpectedPeerChecksum {
			status = http.StatusConflict
			return errors.New("peer or credential changed; refresh before trying again")
		}
		if r.Method == "DELETE" && previous.Ciphertext == "" {
			status = http.StatusConflict
			return errors.New("peer has no configured BGP credential")
		}
		next := BGPCredential{Revision: token(16), PeerChecksum: peerCredentialChecksum(p), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedBy: str(user, "id")}
		action := "remove-bgp-credential"
		if r.Method == "PUT" {
			var err error
			next.Ciphertext, err = a.encryptSecret(bgpCredentialAAD(id, next), secret)
			if err != nil {
				status = http.StatusInternalServerError
				return errors.New("could not encrypt BGP credential")
			}
			action = "set-bgp-credential"
		}
		if s.PeerCredentials == nil {
			s.PeerCredentials = map[string]BGPCredential{}
		}
		s.PeerCredentials[id] = next
		auditDetails(s, str(user, "id"), action, "peers", id, Record{"previousRevision": previous.Revision, "revision": next.Revision})
		response = publicBGPCredential(id, p, next)
		if body.Generate {
			response["generatedSecret"] = secret
		}
		return nil
	})
	if err != nil {
		if !reauthenticated {
			problem(w, status, "current password or authentication code was not accepted")
		} else if status == http.StatusInternalServerError {
			problem(w, status, "could not save BGP credential")
		} else {
			problem(w, status, err.Error())
		}
		return
	}
	respond(w, 200, response)
}

func (a *App) peerAuthentication(s *State, peer Record) (string, string, error) {
	id := str(peer, "id")
	c := s.PeerCredentials[id]
	if c.Ciphertext == "" {
		return "", c.Revision, nil
	}
	if c.PeerChecksum != peerCredentialChecksum(peer) {
		return "", "", fmt.Errorf("BGP credential binding for peer %s changed", id)
	}
	secret, err := a.bgpPassword(id, c)
	if err != nil {
		return "", "", err
	}
	return secret, c.Revision, nil
}
