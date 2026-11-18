# BIRD 3 适配

`bird3` 是独立的平台目标，配置头、审批目标、原生解析器和本机恢复日志都区分 BIRD 2/3。已在 BIRD **3.4.0** 上对本项目实际使用的配置子集完成离线原生解析；没有借用 BIRD 2 的解析结果。生成器复用两代共有的受限策略编译逻辑，不开放任意 BIRD 源码、include、线程设置或 shell。

支持双栈 `route-server`、`route-collector` 的 `observe` / `authorized-only`，精确前缀、前缀/来源 ASN 配对、首 ASN 和路径长度限制、TCP-MD5 及严格 RPKI `require-valid`。收集器始终 `export none`；所有角色均不生成 kernel 协议。预策略缓存使用 `import table`、`import keep filtered` 和包含被过滤路由的 `receive limit ... action disable`；RPKI 使用 `rpki reload` 重评估，缓存过期后授权 RIB 拒绝未验证路由。BIRD 3 的 RPKI expire 最小为 600 秒，低于该值在生成前拒绝。观察数据与授权 RIB 的区别见 [COLLECTORS.md](COLLECTORS.md)。

这里使用 BIRD 3 默认线程组，不虚构线程数量或性能提升。官方 3.4 文档保留上述导入、过滤、认证和配置确认语法；新增的 EVPN、ASPA、TCP-AO、线程调优等能力不在本配置合同内。适配不等于已经完成真实邻居、RTR 连接、转发或负载测试。

Community 边界需单独审阅：BIRD 2/3 的 `rs client` **不会**自动关闭 `interpret communities`；默认开启会在导出过滤器之前处理 NO_EXPORT / NO_ADVERTISE。`Router.RFC1997Passthrough` 对应旧模板的 `rfc1997_passthru`：只有显式 `true` 才在路由服务器会话生成 `interpret communities off`；`false` 或未指定保留默认行为和既有字节。收集器始终禁止导出，该选项不会放宽限制。其他成员自定义 community 控制仍属于单独迁移域。

原生默认并非全部跨版本等价：BIRD 2.17.5 默认允许 AS_SET，BIRD 3.4.0 默认拒绝 AS_SET（各版本 `proto/bgp/config.Y` 的 `allow_as_sets` 初始值分别为1、0）。本适配没有擅自覆盖这一原生行为。依赖含 AS_SET 更新的旧网络必须在更换主版本前审阅，不应只比较共有配置语法。

独立 API：

- `RenderBIRD3(Router, []Peer)`：生成带 BIRD 3 目标标记的受限配置。
- `ValidateBIRD3(ctx, binary, config)`：执行目标二进制 `--version`，必须为 major 3，再执行该二进制的 `-p -c`。缺工具、错误 major、跨版本生成标记、占位符、错误语法均拒绝。
- `RedactBIRD3(config)`：仅识别本项目的 BIRD 3 输出，复用严格语法识别后保留 BIRD 3 头；未知源返回空预览。
- `ApplyOptions.BirdMajor`：`0` 或 `2` 保持原 BIRD 2 行为，`3` 选择 BIRD 3，其他值拒绝。

本机命令示例中的 `/opt/bird3` 为操作员配置的示例路径。发行版的软件包也可能仍将程序命名为 `/usr/sbin/bird`，不能按文件名猜测版本。

```sh
ixp-agent check --platform bird3 --validator /opt/bird3/sbin/bird \
  --file approved.conf --checksum FILE_SHA256
ixp-agent apply --platform bird3 --bird /opt/bird3/sbin/bird \
  --birdc /opt/bird3/sbin/birdc --file approved.conf --checksum FILE_SHA256 \
  --target /etc/bird3/bird.conf --socket /run/bird3/bird.ctl
ixp-agent recover --platform bird3 --bird /opt/bird3/sbin/bird \
  --birdc /opt/bird3/sbin/birdc --target /etc/bird3/bird.conf \
  --socket /run/bird3/bird.ctl
```

`FILE_SHA256` 来自授权下载返回的 `X-Config-SHA256`，不是控制台审查标识。带脱敏密码占位符的预览不能安装。目标必须已有可用配置，包含密钥时需要正确 owner/group 和 `0600` 或 `0640`。

BIRD 3 apply 在写入前校验候选与回退配置，并通过只读 `show status` 核对控制套接字确实连接到运行中的 BIRD 3。然后沿用独占文件锁、原子写、`configure ... timeout 60`、`configure status`、`configure confirm` 和失败回退。3.4.0 官方源码的完成、进行中、确认响应码分别为 0003、0004、0018，队列/退出/无操作不会视为成功。真正的 BIRD 3.4.0 `birdc` 已连接私密模拟 Unix socket 验证该协议流程；没有启动真实 daemon。确认发生在配置完成之后，不代表 BGP 会话或业务可达性已经健康。

恢复日志使用 `bird3:<原配置SHA256>`，默认 BIRD 2 恢复入口不能消费它。磁盘恢复与运行配置重载需要匹配的 BIRD 3 parser 和可验证的控制套接字；失败时保留日志/回退文件供修复环境后重试。切换 daemon 主版本是独立的主机维护操作：本执行器不升级软件包，也不把带 BIRD 2 生成标记的旧基线冒充 BIRD 3 回退配置。

已完成的专项：两角色/两收集策略、双栈配对前缀来源、认证、RPKI 缓存数量边界、空观察白名单；错误配置与版本拒绝；模拟失败回退与中断恢复；真实 BIRD 3 客户端和模拟控制端的命令交互。可通过 `BIRD3_BIN` / `BIRD3_BIRDC` 选择匹配的本机工具，运行 `go test -race ./internal/routing -run 'TestBIRD3|TestNativeBIRD3'`。未配置这些环境变量时，原生专项明确跳过。

本次工具来源：Debian `bird3_3.4.0-1_amd64.deb`，SHA-256 `3ca316e7ebbc3af01fc14429fbfcbd5809ea6574b25e5e01db70bf73605cbb65`，与官方 sid Packages 索引一致；官方 `bird-3.4.0.tar.gz` SHA-256 `ee8900c98de7b4da0f2b5480eb66adc3aca6961baeba4ac949481ccac3ed365f`。仅在 `/tmp` 解包使用，没有安装服务。

参考：[BIRD 3.4.0 用户手册](https://bird.nic.cz/doc/bird-3.4.0.html)、[官方发布及源码提交](https://bird.nic.cz/get-bird/)、[3.4.0 配置控制协议源码](https://gitlab.nic.cz/labs/bird/-/blob/c3669497e3949b942d86d07e343092cc439b4659/sysdep/unix/main.c)。
