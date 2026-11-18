package routing

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
)

const RedactedBGPPassword = "[REDACTED-BGP-SECRET]"

var bgpPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{1,80}$`)
var authenticationRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// ValidateBGPPassword accepts an exact, portable TCP-MD5 byte string. It never
// trims, escapes, truncates or prints the supplied secret. Empty input is not a
// password; callers must use an explicit disable/delete operation instead.
func ValidateBGPPassword(password string) error {
	if !bgpPasswordPattern.MatchString(password) {
		return errors.New("BGP password must be 1..80 ASCII bytes using only letters, digits and + / = _ -")
	}
	return nil
}
func validatePeerAuthentication(peer Peer) error {
	if peer.AuthenticationRevision != "" && !authenticationRevisionPattern.MatchString(peer.AuthenticationRevision) {
		return errors.New("authentication revision must be a 22-character base64url identifier")
	}
	if peer.Password != "" {
		if err := ValidateBGPPassword(peer.Password); err != nil {
			return err
		}
		if peer.AuthenticationRevision == "" {
			return errors.New("enabled BGP authentication requires a non-secret revision")
		}
	}
	return nil
}
func authenticationComment(peer Peer, xml bool) string {
	if peer.AuthenticationRevision == "" {
		return ""
	}
	state := "disabled"
	if peer.Password != "" {
		state = "tcp-md5"
	}
	revision := "revision=" + peer.AuthenticationRevision
	if xml {
		revision = "revision-hex=" + hex.EncodeToString([]byte(peer.AuthenticationRevision))
	}
	return fmt.Sprintf("moedove-auth peer=%s %s state=%s", peerName(peer), revision, state)
}

// String/GoString are defensive logging representations; the password is also
// intentionally excluded from JSON serialization of the renderer input type.
func (peer Peer) String() string {
	type publicPeer Peer
	copy := peer
	copy.Password = ""
	return fmt.Sprintf("%+v", publicPeer(copy))
}
func (peer Peer) GoString() string { return peer.String() }

// Apply also accepts locally reviewed native BIRD files, whose statements may
// share one line. Be conservative: even a password keyword in a comment requires
// private permissions; this is not the generated-config redaction parser.
var nativeAuthenticationLine = regexp.MustCompile(`(?i)\bpasswords?\b`)

// Group-readable daemon configuration is supported, but adding a secret must
// never silently preserve world-readable permissions from an older profile.
func privateAuthenticationTarget(config string, mode os.FileMode) error {
	if mode.Perm()&0137 != 0 && nativeAuthenticationLine.MatchString(config) {
		return errors.New("BGP authentication requires private permissions: no execute, group-write or other-user access; set appropriate owner/group and 0600 or 0640 for the daemon first")
	}
	return nil
}
