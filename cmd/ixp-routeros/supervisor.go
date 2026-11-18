package main

import (
	"errors"

	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/live"
)

// Only startup's private configuration chooses service budgets and automatic
// cleanup policy. The target is derived from the authenticated runtime.
func openSupervisor(cfg live.Config, state string, runtime control.Runtime) (*control.Supervisor, error) {
	if runtime == nil {
		return nil, errors.New("fixed target runtime is required")
	}
	controller, err := cfg.Supervisor.ControllerConfig()
	if err != nil {
		return nil, err
	}
	controller.Dir, controller.Target = state, runtime.Binding().Target
	return control.Open(controller, runtime)
}
