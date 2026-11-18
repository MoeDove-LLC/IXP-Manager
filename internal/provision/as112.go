package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/as112"
)

func renderAS112DNS(input Input) (Artifact, error) {
	b, err := as112.RenderNode(input.Options.RouterID, input.AS112RouterCreatedAt, *input.AS112Service, *input.Options.AS112, input.AS112NodeAddresses)
	if err != nil {
		return Artifact{}, err
	}
	content, err := as112.Encode(b)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Kind: "as112-dns", Content: content, Filename: "as112-" + input.Options.RouterID + ".json", ValidationLevel: "native", Complete: true, TargetCount: len(b.ListenAddresses), Notices: []Notice{{Severity: "warning", Code: "as112-not-active", Message: "AS112权威DNS配置包；地址配置、服务启动、真实健康检查和BGP发布必须按顺序执行。解析通过不代表目标服务可用。"}, {Severity: "warning", Code: "as112-withdrawal-required", Message: "必须在DNS或任一服务地址失效时撤回相应AS112前缀；此配置包不会自动控制路由进程。"}, {Severity: "info", Code: "as112-host-return-path", Message: "AS112 BGP只发出本源服务前缀并拒收邻居路由；主机返回路径须单独配置和审阅。"}}}, nil
}
func validateAS112DNS(ctx context.Context, a Artifact, options ValidatorOptions) (Validation, error) {
	b, err := as112.Decode(a.Content)
	if err != nil {
		return Validation{}, err
	}
	parent, err := os.MkdirTemp("", "moedove-as112-check-")
	if err != nil {
		return Validation{}, errors.New("AS112 temporary check directory unavailable")
	}
	defer os.RemoveAll(parent)
	directory := filepath.Join(parent, "bundle")
	if err = as112.WriteNewDirectory(directory, b); err != nil {
		return Validation{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	named := options.NamedCheckzone
	if named == "" {
		named = "named-checkzone"
	}
	pdns := options.PDNSServer
	if pdns == "" {
		pdns = "pdns_server"
	}
	for i, zone := range b.Zones {
		file := b.Files[i+1]
		if !strings.HasPrefix(file.Path, "zones/") {
			return Validation{}, errors.New("AS112 zone layout invalid")
		}
		if _, err = command(ctx, named, "-i", "local", zone, filepath.Join(directory, file.Path)); err != nil {
			return Validation{}, errors.New("named-checkzone unavailable or rejected an AS112 zone")
		}
	}
	cmd := exec.CommandContext(ctx, pdns, "--config-dir="+directory, "--config=check")
	cmd.Dir = directory
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + parent, "TMPDIR=" + parent, "LANG=C"}
	cmd.WaitDelay = 2 * time.Second
	// Validator wrappers are private operator-selected executables, as with the
	// other native checkers. Raw daemon diagnostics are never returned to HTTP.
	if err = cmd.Run(); err != nil {
		return Validation{}, errors.New("PowerDNS Authoritative configuration check unavailable or failed")
	}
	return Validation{Level: "native", Message: "PowerDNS Authoritative settings and all AS112 zones passed local native parsing; address binding, DNS health, routing and activation were not tested"}, nil
}
