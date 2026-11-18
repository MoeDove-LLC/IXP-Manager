package routing

import (
	"context"
	"errors"
	"fmt"
)

type Capability struct {
	Platform                  string   `json:"platform"`
	Label                     string   `json:"label"`
	Render                    bool     `json:"render"`
	Validate                  bool     `json:"validate"`
	Apply                     bool     `json:"apply"`
	Rollback                  bool     `json:"rollback"`
	ValidationLevel           string   `json:"validationLevel"`
	Extension                 string   `json:"extension"`
	Notes                     string   `json:"notes"`
	Roles                     []string `json:"roles"`
	CollectorPolicies         []string `json:"collectorPolicies"`
	Fetch                     bool     `json:"fetch"`
	RFC1997Control            bool     `json:"rfc1997Control"`
	RFC1997DefaultPassthrough *bool    `json:"rfc1997DefaultPassthrough,omitempty"`
}

func Capabilities() []Capability {
	capabilities := []Capability{
		{Platform: "bird2", Label: "BIRD 2", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "native", Extension: "conf", Notes: "本机 BIRD 2 解析；支持严格 require-valid RTR 校验及自动重评估；ixp-agent 原子替换、失败回退与中断恢复。"},
		{Platform: "bird3", Label: "BIRD 3", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "native", Extension: "conf", Notes: "必须使用实际 BIRD 3 解析器；ixp-agent 检查守护进程主版本，支持限时配置确认与中断恢复。RPKI expire 至少600秒；AS_SET 等原生默认与 BIRD 2 有差异，迁移需审阅。"},
		{Platform: "frr", Label: "FRRouting", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "native", Extension: "conf", Notes: "bgpd 原生校验；专用 bgpd 本机 watchdog 暂存/确认/取消。超时恢复依赖看护进程运行，进程死亡后须由监督器重启恢复；不是设备原生原子事务。严格 RTR 校验需 RPKI 模块；解析需用户/网络命名空间。"},
		{Platform: "openbgpd", Label: "OpenBGPD", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "native", Extension: "conf", Notes: "实际 OpenBGPD 原生校验；ixp-openbgpd 只管理 Linux 独占专用实例，以私密控制socket/PID固定目标并提供监督器暂存、确认和回退。非设备原生事务，无法检测带外runtime修改；无已验证的运行时RTR适配。"},
		{Platform: "junos", Label: "Juniper Junos", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "structural", Extension: "xml", Notes: "控制台仅进行 XML 结构检查；ixp-netconf 必须在目标执行 candidate validate 和限时 confirmed-commit。尚无本项目运行时 RTR 适配。"},
		{Platform: "routeros7", Label: "MikroTik RouterOS 7", Render: true, ValidationLevel: "none", Extension: "rsc", Notes: "控制台仅预览默认禁用的连接与明确白名单。独立发布监督器正在完成原生验证，生产发布尚未开放，不能审批为可自动下发。"},
		{Platform: "cisco", Label: "Cisco", ValidationLevel: "none", Extension: "cfg", Notes: "需明确 IOS/IOS-XE/IOS-XR 及版本后实现专用适配器。"},
		{Platform: "quagga", Label: "Quagga 1.2.4 (legacy)", Render: true, Validate: true, ValidationLevel: "native", Extension: "conf", Notes: "独立 RS view，按来源匹配精确前缀、origin 和首 AS，保留直接邻居下一跳。原生检查需要实际 1.2.4 及私有用户和网络命名空间。控制台批准后可下载或拉取文件。目标机的 ixp-agent 由同 UID 管理固定专用实例，暂存或回退会中断 BGP 会话。GR、TCP-MD5 握手和生产目标仍待验收。AS112、RPKI 和旧 community 覆盖尚未适配。"},
		{Platform: "bird1", Label: "BIRD 1 (legacy)", Render: true, Validate: true, Apply: true, Rollback: true, ValidationLevel: "native", Extension: "conf", Notes: "单族RS/collector，实际bird/bird6 1.6.8。v2专用实例支持本机限时确认、回退与显式恢复：同UID私密target/socket、精确base/next校验和及运行指纹。v1仅预览/校验；旧community、宽松IRR、AS112、RPKI仍未适配。"},
	}
	for i := range capabilities {
		c := &capabilities[i]
		c.Fetch = c.Render && c.Validate
		c.RFC1997Control = RFC1997ControlSupported(c.Platform)
		if c.RFC1997Control {
			defaultValue := c.Platform == "openbgpd"
			c.RFC1997DefaultPassthrough = &defaultValue
		}
		c.Roles, c.CollectorPolicies = []string{}, []string{}
		if RoleSupported(c.Platform, RoleRouteServer) {
			c.Roles = append(c.Roles, RoleRouteServer)
		}
		if RoleSupported(c.Platform, RoleAS112) {
			c.Roles = append(c.Roles, RoleAS112)
		}
		if RoleSupported(c.Platform, RoleRouteCollector) {
			c.Roles = append(c.Roles, RoleRouteCollector)
			c.CollectorPolicies = []string{CollectorObserve, CollectorAuthorizedOnly}
		} else if c.Render {
			c.Notes += " 当前适配器不支持 route-collector：尚未验证无转发安装和过滤观察隔离。"
		}
	}
	return capabilities
}
func ValidationLevel(platform string) string {
	if platform == "" {
		platform = "bird2"
	}
	for _, c := range Capabilities() {
		if c.Platform == platform {
			return c.ValidationLevel
		}
	}
	return "none"
}
func RenderFor(platform string, router Router, peers []Peer) (string, error) {
	role, policy, err := normalizedRole(router)
	if err != nil {
		return "", err
	}
	if !RoleSupported(platform, role) {
		return "", fmt.Errorf("platform %q has no supported adapter for role %s", platform, role)
	}
	if platform == "" {
		platform = "bird2"
	}
	if role == RoleAS112 {
		return renderAS112(platform, router, peers)
	}
	if router.RFC1997Passthrough != nil && !RFC1997ControlSupported(platform) {
		return "", fmt.Errorf("platform %s has no verified RFC1997 passthrough control", platform)
	}
	if rpki, err := normalizeRPKI(router.RPKI); err != nil {
		return "", err
	} else if rpki != nil {
		if !RuntimeRPKISupported(platform) {
			return "", fmt.Errorf("platform %s has no verified runtime RPKI adapter", platform)
		}
		if platform == "frr" && rpki.ExpireSeconds < 600 {
			return "", fmt.Errorf("FRR runtime RPKI expiry must be at least600 seconds")
		}
	}
	// The common validator checks parsed addresses, ASN ranges and explicit prefix
	// policy before any vendor adapter receives the values.
	bird, err := Render(router, peers)
	if err != nil {
		return "", err
	}
	switch platform {
	case "bird2":
		return boundedRendered(bird, nil)
	case "bird3":
		return RenderBIRD3(router, peers)
	case "quagga":
		return RenderQuagga(router, peers)
	case "bird1":
		return RenderBIRD1(router, peers)
	case "openbgpd":
		return RenderOpenBGPD(router, peers)
	case "frr":
		if role == RoleRouteCollector {
			return boundedRendered(renderFRRCollector(router, peers, policy), nil)
		}
		return boundedRendered(renderFRR(router, peers), nil)
	case "routeros7":
		return boundedRendered(renderRouterOS(router, peers), nil)
	case "junos":
		result, err := renderJunos(router, peers)
		return boundedRendered(result, err)
	default:
		return "", fmt.Errorf("platform %q has no supported renderer", platform)
	}
}
func ValidateFor(ctx context.Context, platform, binary, config string) error {
	if err := rejectRedactedArtifact(config); err != nil {
		return err
	}
	switch platform {
	case "", "bird2":
		return Validate(ctx, binary, config)
	case "bird3":
		return ValidateBIRD3(ctx, binary, config)
	case "quagga":
		return ValidateQuagga(ctx, binary, config)
	case "bird1":
		return ValidateBIRD1(ctx, binary, config)
	case "openbgpd":
		return ValidateOpenBGPD(ctx, binary, config)
	case "frr":
		return validateFRR(ctx, binary, config)
	case "junos":
		return ValidateJunosXML(config)
	default:
		return errors.New("this platform has no verified validation adapter; preview cannot be promoted to a validated deployment")
	}
}

func boundedRendered(config string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if len(config) > maxConfigSize {
		return "", errors.New("rendered configuration exceeds16 MiB; split the router policy")
	}
	return config, nil
}
