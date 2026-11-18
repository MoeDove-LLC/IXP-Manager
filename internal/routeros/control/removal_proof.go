package control

import "encoding/json"

// Missing/empty values are the authenticated legacy per-object journal format.
// New batches always persist one of the explicit names.
func normalizedRuleRemovalProof(mode RuleRemovalProofMode) RuleRemovalProofMode {
	if mode == "" {
		return RuleRemovalProofPerObject
	}
	return mode
}

func validRuleRemovalProof(mode RuleRemovalProofMode) bool {
	mode = normalizedRuleRemovalProof(mode)
	return mode == RuleRemovalProofPerObject || mode == RuleRemovalProofGlobalTail
}

func (mode *RuleRemovalProofMode) UnmarshalJSON(raw []byte) error {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || !validRuleRemovalProof(RuleRemovalProofMode(*value)) {
		return failure("rule-removal-proof-mode-invalid")
	}
	*mode = RuleRemovalProofMode(*value)
	return nil
}

func ruleRemovalStepCanAdvance(mode RuleRemovalProofMode, step ruleRemovalStep) bool {
	if !step.ResultRecorded || !step.Acknowledged {
		return false
	}
	if normalizedRuleRemovalProof(mode) == RuleRemovalProofGlobalTail {
		return !step.AbsentVerified
	}
	return normalizedRuleRemovalProof(mode) == RuleRemovalProofPerObject && step.AbsentVerified
}

func ruleRemovalStepCanRemainPending(mode RuleRemovalProofMode, step ruleRemovalStep) bool {
	if step.AbsentVerified {
		return false
	}
	return normalizedRuleRemovalProof(mode) == RuleRemovalProofPerObject || !step.ResultRecorded || !step.Acknowledged
}
