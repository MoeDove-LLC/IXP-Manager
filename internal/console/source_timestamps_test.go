package console

import "testing"

func TestValidateSourceTimestampPreservesEvidenceFormats(t *testing.T) {
	for _, value := range []string{
		"2020-02-29 23:59:01", "2020-02-29 23:59:01.000123", "0000-00-00 00:00:00", "0000-00-00 00:00:00.000000",
		"2020-10-25T01:15:00.123456789+01:00", "2020-01-02T03:04:05Z", "2020-01-02T03:04:05-00:00",
	} {
		if err := ValidateSourceTimestamp(value); err != nil {
			t.Errorf("valid source timestamp %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		"", "2020-01-02", "2020-01-02T03:04:05", "2020-01-02 3:04:05", " 2020-01-02 03:04:05", "2020-01-02 03:04:05.1234567",
		"2021-02-29 03:04:05", "2020-00-02 03:04:05", "0000-00-00 00:00:00.000001", "2020-01-02T03:04:05+24:00", "2020-01-02T03:04:05,123Z",
	} {
		if err := ValidateSourceTimestamp(value); err == nil {
			t.Errorf("invalid source timestamp %q accepted", value)
		}
	}
}
