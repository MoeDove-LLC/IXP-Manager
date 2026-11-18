package provision

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/moedove/ixp-manager/internal/core"
)

func renderSwitchCore(in Input) (Artifact, error) {
	if in.CoreConfig == nil {
		return Artifact{}, errors.New("typed switch plan is missing")
	}
	raw, err := json.MarshalIndent(in.CoreConfig, "", "  ")
	if err != nil {
		return Artifact{}, err
	}
	notices := []Notice{{Severity: "warning", Code: "target-adapter-required", Message: "完整类型化核心网络计划，仅完成结构校验；必须经目标厂商适配、目标配置校验和独立应用流程。未修改交换机。"}}
	for _, note := range in.CoreConfig.Notices {
		notices = append(notices, Notice{Severity: "info", Code: note.Code, Message: note.Message, TargetID: in.CoreConfig.SwitchID})
	}
	return Artifact{Kind: "switch-core", Content: string(raw) + "\n", Filename: "moedove-core-" + shortID(in.Options.SwitchID) + ".json", ValidationLevel: "structural", Complete: false, TargetCount: len(in.CoreConfig.PhysicalLinks), Notices: notices}, nil
}

func checkSwitchCoreArtifact(a Artifact) error {
	if a.Complete || a.Zone != "" {
		return errors.New("a switch-core plan is not a native complete device configuration")
	}
	var plan core.SwitchPlan
	decoder := json.NewDecoder(strings.NewReader(a.Content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid typed switch-core JSON plan")
	}
	return core.ValidateSwitchPlan(plan)
}
