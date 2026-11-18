package console

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	sourceSQLTimestamp   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?$`)
	sourceZonedTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)
)

// ValidateSourceTimestamp accepts the exact legacy timestamp text. An SQL wall
// time has no asserted zone, and the MySQL zero sentinel remains a source fact.
func ValidateSourceTimestamp(value string) error {
	if sourceSQLTimestamp.MatchString(value) {
		if suffix, zero := strings.CutPrefix(value, "0000-00-00 00:00:00"); zero && strings.Trim(suffix, ".0") == "" {
			return nil
		}
		if parsed, err := time.Parse("2006-01-02 15:04:05", value); err == nil && parsed.Year() > 0 {
			return nil
		}
	}
	if sourceZonedTimestamp.MatchString(value) {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && parsed.Year() > 0 {
			return nil
		}
	}
	return errors.New("source timestamp must be SQL datetime, SQL zero, or RFC3339 with an explicit offset")
}
