# 路由配置与本机执行器

统一平台能力、FRR 与 RouterOS 的支持边界见 [PLATFORMS.md](PLATFORMS.md)，Junos 候选事务和 NETCONF 用法见 [JUNOS.md](JUNOS.md)。FRR 暂存、确认、超时与进程恢复见 [FRR-WATCHDOG.md](FRR-WATCHDOG.md)。以下为 BIRD 本机执行器说明。

BIRD 1 的独立单族生成、实际 bird/bird6 校验及 v2 专用本机事务见 [BIRD1.md](BIRD1.md)；其 runtime 指纹、私密 target/socket 与双校验和契约独立于下方 BIRD 2 的 apply/recover，v1 历史配置仍仅 preview/check。

BIRD 2 与 FRR 还支持明确的 `route-collector` 角色：观察模式保留被策略拒绝的路由供本机诊断，所有模式禁止导出且不安装内核路由。角色、收集策略、数量限制和旧功能迁移边界见 [COLLECTORS.md](COLLECTORS.md)。空角色仍按 route-server 配置生成；未启用运行时 RPKI 的既有字节与校验和保持不变。旧 RPKI 配置增加过滤缓存限额，需要重新生成和审批，历史快照不重写。

目标为 **BIRD 2**。配置来自结构化路由器和对等体数据，不接受自定义模板、include 路径、BIRD 表达式或远程 shell 命令。

- `Render(Router, []Peer)` 生成稳定配置；名称不进入配置。IPv4/IPv6 分开校验，启用的对等体必须提供精确前缀白名单、有效 ASN 和最大前缀数。
- 白名单按精确前缀匹配。例如 `198.51.100.0/24` 不授权更具体的 `/25`；需要逐项登记。默认路由、非规范前缀、地址族混用、空白名单都会被拒绝。
- 路由服务器使用 `rs client`，限制第一跳 ASN 与 AS path 长度，只导出 BGP 学到的路由，无内核路由写入协议。
- `Validate` 必须调用真正的 BIRD 2 `--version` 和 `-p -c`。工具不存在、版本不符、超时或语法错误均不能获得已验证状态。
- 控制台生成不可变快照，展示脱敏内容及审查标识，先原生校验，再审批和授权下载。文件 SHA-256 由授权下载响应的 `X-Config-SHA256` 返回，下载成功后才展示；审查标识不是文件 SHA。修改资源之后需要重新生成快照。

```sh
ixp-agent check --platform bird2 --file approved.conf --checksum <授权下载返回的文件SHA-256>
ixp-agent apply --platform bird2 --file approved.conf --checksum <授权下载返回的文件SHA-256> \
  --target /etc/bird/bird.conf --socket /run/bird/bird.ctl
```

`check` 不改变配置；`apply` 是操作员在路由主机上明确执行的变更。Web 服务不调用 apply，也不需要路由主机的 root 权限。SHA-256 需要通过已登录控制台的授权下载等可信渠道取得，散列本身不证明来源或审批身份。

四个平台均支持结构化 TCP-MD5 密钥：BIRD 的 `authentication md5`/`password`、FRR 的邻居 `password`、Junos 的 `authentication-key`、RouterOS 的 `tcp-md5-key`。共同输入限定为 **1–80 字节 ASCII 字母、数字和 `+ / = _ -`**；不修剪、不截断、不插入任意厂商语法。空值表示关闭认证，凭据 API 只允许显式删除来关闭。认证 revision 为 22 字符 base64url 公共标识，轮换和删除都会改变；Junos 注释将其编码为十六进制以避免 XML 注释中的双连字符。配置差异只展示 revision 和脱敏占位符，不暴露密钥。

`RedactConfig` 仅接受本项目的已知生成格式；未知命令、意外 XML 或无法安全识别的密码行均返回空结果和固定错误。带 `[REDACTED-BGP-SECRET]` 的预览无法通过校验或安装，即使重新计算 SHA 也不会放行。原生解析器、BIRD 控制命令、FRR reload 的原始输出可能包含密钥，执行器只返回固定错误类别/退出码，不转发原始诊断。授权下载与路由主机运行配置仍含明文密钥，须按私密配置管理；TCP-MD5 轮换需要协调双方，会话无中断轮换和 TCP-AO 不在本实现范围内。

执行器要求目标为已有的普通文件，拒绝符号链接路径，并保存原所有权和权限。首次使用前应由主机管理员建立可工作的基础 BIRD 配置和控制套接字。执行器持有目标独占锁，写入已同步的回退副本，通过原子重命名安装配置，再调用固定参数并核对命令对应的 BIRD 成功响应码的 `birdc configure ... timeout 60`、`configure status`、`configure confirm`。命令失败会恢复旧文件并重新加载。回退副本为 `<目标>.moedove-previous`。

含密钥的新配置或回退配置不能写入具有执行权限、组写权限或其他用户权限的文件。先由操作员设置正确的 owner/group 和 `0600` 或 `0640`，确保路由守护进程可读；执行器不会悄悄扩大访问范围。该检查同样覆盖 BIRD 恢复快照、FRR 离线 bootstrap 和 watchdog 回退；若有人在事务期间放宽目标权限，恢复会明确失败并保留恢复日志，收紧权限后可重试。

语法校验和守护进程响应不证明 BGP 邻居已经建立或端到端网络可达；应使用实验环境和实际网络监控检查策略。IRR/RPKI 离线收集、审批策略和严格运行时 RPKI 支持见 ../policy/README.md；这些检查不替代业务连通性健康探针。若进程或机器在安装途中终止，BIRD 的未确认配置会在计时结束后撤销，但磁盘恢复可能尚未执行。执行器会保留 `.moedove-pending` 恢复日志并阻止下一次覆盖；运行 `ixp-agent recover --platform bird2 --target /etc/bird/bird.conf --socket /run/bird/bird.ctl`，执行器将核对旧快照校验和、原生解析、恢复磁盘配置并重新加载。内核文件锁会在进程退出时自动释放，无需手动删除锁文件。恢复命令仍需要操作员在主机上执行，不是开机自动恢复服务。

测试：

```sh
go test ./internal/routing ./cmd/ixp-agent
BIRD_BIN=/usr/sbin/bird go test ./internal/routing -run TestNativeBIRD2Integration -v
BIRD_BIN=/usr/sbin/bird FRR_BIN=/usr/lib/frr/bgpd go test ./internal/routing -run TestNativeBGPAuthentication -v
```

原生测试只进行解析，不启动或重载守护进程。单元测试使用模拟控制命令验证回退；不应把它当作实网演练。

语法和 `configure timeout/confirm` 的官方参考：[BIRD 2.17.1 用户手册](https://bird.nic.cz/doc/bird-2.17.1.html)。

TCP-MD5 厂商语法参考：[FRR neighbor password](https://docs.frrouting.org/en/latest/bgp.html)、[Junos authentication-key](https://www.juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/statement/authentication-key-edit-protocols-bgp.html)、[RouterOS tcp-md5-key](https://help.mikrotik.com/docs/spaces/ROS/pages/331612228/routing+bgp)。80 字节是本项目统一的可移植上限，兼容 Linux `TCP_MD5SIG_MAXKEYLEN`；并非声称每个厂商都只允许 80 字节。已用 BIRD 2.17.5 和 FRR 10.3 对 1 字节、80 字节及上述特殊字符进行离线原生解析；Junos/RouterOS 仅有生成/结构检查，仍需目标设备验证。
