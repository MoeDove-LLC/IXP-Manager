// Package console validates inventory labels and serial settings. It never
// resolves names, opens devices or produces credentials/connection commands.
package console

import (
	"errors"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// IDNA maps/validates Unicode labels; DNS byte bounds are checked below after
// removing at most one mapped absolute-root separator.
var hostnameProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.StrictDomainName(true), idna.ValidateLabels(true), idna.CheckHyphens(true), idna.CheckJoiners(true), idna.BidiRule())
var BaudRates = []int{300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 28800, 38400, 57600, 115200, 230400}

func ValidBaudRate(value int) bool {
	for _, rate := range BaudRates {
		if value == rate {
			return true
		}
	}
	return false
}

func NormalizeHostname(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || len(value) > 1024 {
		return "", errors.New("hostname must be nonempty without surrounding whitespace")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("hostname contains control characters")
		}
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return "", errors.New("use managementIp for a literal IP address")
	}
	name, err := hostnameProfile.ToASCII(value)
	if err != nil {
		return "", errors.New("hostname is not a valid nontransitional IDNA name")
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "" || len(name) > 253 {
		return "", errors.New("hostname exceeds DNS bounds")
	}
	if _, err = netip.ParseAddr(name); err == nil {
		return "", errors.New("use managementIp for a literal IP address")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid hostname label")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", errors.New("invalid hostname label")
			}
		}
	}
	return name, nil
}
func ValidatePortLabel(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return errors.New("portLabel must contain1..255 characters without boundary whitespace")
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("portLabel cannot contain control or invisible format characters")
		}
	}
	return nil
}

// Comparison only: the stored/displayed label retains original case and zeros.
func PortKey(value string) string { return cases.Fold().String(norm.NFC.String(value)) }
