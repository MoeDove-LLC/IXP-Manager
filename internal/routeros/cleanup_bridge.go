package routeros

import "errors"

// NewCleanupBridge exposes only the Remove operations already contained in a
// complete canonical plan. It needs no retired or active plaintext password.
// The bridge itself grants no permission: the supervisor must prove the durable
// confirmation and current protected state before binding a cleanup runtime.
func NewCleanupBridge(plan Plan) (*APIBridge, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	b := &APIBridge{target: plan.Target, checksum: plan.Checksum, commands: map[string]CompiledCommand{}, order: []string{}}
	for _, op := range plan.Operations {
		if op.Phase != Cleanup {
			continue
		}
		cmd := CompiledCommand{OperationID: op.ID, Phase: Cleanup, Object: op.Object, Action: Remove, Attributes: []Attribute{}}
		switch op.Kind {
		case RemoveConnection:
			cmd.Resource = BGPConnection
		case RemoveRule:
			cmd.Resource = FilterRule
		default:
			return nil, errors.New("cleanup must contain only approved removals")
		}
		if op.Object.ID == "" {
			return nil, errors.New("cleanup requires the reviewed native object ID")
		}
		if _, err := mutationRequest(cmd, op.Object.ID); err != nil {
			return nil, err
		}
		b.commands[op.ID] = cmd
		b.order = append(b.order, op.ID)
	}
	return b, nil
}
