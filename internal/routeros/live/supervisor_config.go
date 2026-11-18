package live

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// SupervisorConfig is private service policy. It is never accepted in a stage
// request and does not grant native capabilities or alter API/SSH command limits.
type SupervisorConfig struct {
	OperationTimeoutSeconds       int  `json:"operationTimeoutSeconds,omitempty"`
	PrepareTimeoutSeconds         int  `json:"prepareTimeoutSeconds,omitempty"`
	MaxConfirmationTimeoutSeconds int  `json:"maxConfirmationTimeoutSeconds,omitempty"`
	ManualCleanup                 bool `json:"manualCleanup,omitempty"`
}

func (cfg *SupervisorConfig) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return fail("invalid-supervisor-config")
	}
	var decoded SupervisorConfig
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return fail("invalid-supervisor-config")
		}
		seen[name] = true
		var target any
		switch name {
		case "operationTimeoutSeconds":
			target = &decoded.OperationTimeoutSeconds
		case "prepareTimeoutSeconds":
			target = &decoded.PrepareTimeoutSeconds
		case "maxConfirmationTimeoutSeconds":
			target = &decoded.MaxConfirmationTimeoutSeconds
		case "manualCleanup":
			target = &decoded.ManualCleanup
		default:
			return fail("invalid-supervisor-config")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, target) != nil {
			return fail("invalid-supervisor-config")
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return fail("invalid-supervisor-config")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return fail("invalid-supervisor-config")
	}
	*cfg = decoded
	return nil
}

// ControllerConfig preserves the controller's existing defaults when a field
// is omitted or zero. Target and state-directory binding are installed by the
// local service from its already verified runtime and private command line.
func (cfg SupervisorConfig) ControllerConfig() (control.Config, error) {
	operation, prepare, confirmation := cfg.OperationTimeoutSeconds, cfg.PrepareTimeoutSeconds, cfg.MaxConfirmationTimeoutSeconds
	if operation == 0 {
		operation = 10
	}
	if prepare == 0 {
		prepare = 600
	}
	if confirmation == 0 {
		confirmation = 300
	}
	if operation < 1 || operation > 30 || prepare < 1 || prepare > 900 || confirmation < 10 || confirmation > 300 {
		return control.Config{}, fail("invalid-supervisor-bounds")
	}
	return control.Config{
		OperationTimeout:       time.Duration(operation) * time.Second,
		PrepareTimeout:         time.Duration(prepare) * time.Second,
		MinConfirmationTimeout: 10 * time.Second,
		MaxConfirmationTimeout: time.Duration(confirmation) * time.Second,
		ManualCleanup:          cfg.ManualCleanup,
	}, nil
}
