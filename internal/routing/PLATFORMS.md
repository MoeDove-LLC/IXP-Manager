# 多平台路由配置

控制台采用同一套结构化路由器、成员、连接和对等体数据，按平台生成不可变配置快照。ASN、IP、前缀白名单、最大前缀数的共同规则先校验，再进入厂商适配器。平台类型也是审批目标的一部分；禁止将一个厂商的校验结果用于另一个厂商。

角色另由 `Capabilities.roles` 指定：BIRD 1/2/3、FRR、OpenBGPD 支持 `route-server` 与 `route-collector`；BIRD 1 必须拆分单族实例，Junos / RouterOS 目前只支持 `route-server`。收集器可选 `observe` 或 `authorized-only`，含义及验证边界见 [COLLECTORS.md](COLLECTORS.md)。不支持的角色直接拒绝，不会退化为路由服务器。

| 平台 | 配置生成 | 控制台校验 | 执行与恢复 |
|---|---|---|---|
| BIRD 2 | 完整双栈 route-server 配置 | 本机 `bird -p`，实测 2.17.5 | `ixp-agent --platform bird2` 原子安装、确认与失败回退、中断恢复日志 |
| BIRD 3 | 单独版本头、server/collector、前缀-origin 与认证/RPKI | 必须实际 BIRD 3，实测 3.4.0 | `ixp-agent --platform bird3` 还检查 daemon major，恢复标记绑定版本，见 BIRD3.md |
| FRRouting | 独立 BGP view、route-server-client、精确前缀和 ASN/path 过滤 | 本机 `bgpd --dryrun`，实测 10.3 | 专用本机 watchdog：stage/confirm/cancel；超时与重启恢复依赖监督器，见 FRR-WATCHDOG.md |
| OpenBGPD | 受管独立 RIB、server/collector、origin-set、TCP-MD5 | 实际 `bgpd -n -f`，实测 8.8 | `ixp-openbgpd` 绑定 Linux root parent/socket 与专用配置，监督器支持恢复，见 OPENBGPD.md |
| Junos | 独立 managed group 的候选 XML | 仅结构检查，界面明确标注 | `ixp-netconf` 在目标执行 candidate/validate/confirmed-commit；必须再次确认，否则由设备计时回退 |
| RouterOS 7.20+ | 独立表、实例、白名单、默认禁用连接 | 无目标原生校验适配，保持预览状态 | 无自动导入；不能把脚本生成成功当作可安全上线 |
| BIRD 1 | 单族 RS/collector，IPv4/IPv6 必须拆成独立实例 | 实际 1.6.8 bird/bird6；校验 major 和正反族探针 | v2 专用实例支持精确运行读回、native 限时确认、回退与显式恢复；v1 仍仅 preview/check，见 [BIRD1.md](BIRD1.md) |
| Quagga 1.2.4 | 独立双族 RS view，来源绑定 import/export、精确前缀/origin、直接邻居下一跳 | 实际 stock 1.2.4；强制私有 user/net namespace 与 managed profile重建 | 仅审批下载、fetch/check；无 apply/recover/rollback，见 [QUAGGA.md](QUAGGA.md) |
| Cisco | 显式展示尚未适配 | 不允许用其他平台的解析器替代 | 不允许静默回退为 BIRD |

`Capabilities.fetch` 表示授权文件可由统一拉取工具下载，不能由 `apply` 推测。控制面另外提供 `nativeCheckerAvailable` 表示本地可执行文件是否存在；版本及配置必须实际校验。BIRD3/OpenBGPD 的原生二进制不随标准管理容器自动安装，需要通过命令行配置相应解析器。原生解析和 mock 控制事务的测试不等于真实设备发布验收。

`RFC1997Passthrough` 是可空的明确选择。BIRD 2/3 的原生默认处理这些标准社区，显式 true 才生成 `interpret communities off`；OpenBGPD 在透明模式下默认透传，只有显式 false 才增加相应标准社区限制。未设置时保留旧字节或平台原生行为；其他平台尚未验证此控制，因此拒绝显式值。当前默认行为差异及 AS_SET 等原生版本差异需在迁移时审阅，不能声称所有厂商默认完全相同。

FRR 生成独立 `view MOEDOVE`，不生成 network/redistribute 命令。普通邻居输入前缀/AS-path 过滤和所有客户端共享的显式授权 route-map 共同限制路由来源；默认拒绝不匹配的策略。AS 路径仅接受邻居 ASN 起始、最多 64 个数字 ASN 的序列。

FRR 的 `--dryrun` 本身仍会初始化部分运行目录。执行器固定追加 `--bgp_port 0 --no_zebra --no_kernel`，不监听 BGP、不连接 Zebra、不安装内核路由。应以非特权用户运行验证器，并准备其运行目录；不要通过 root 权限放宽失败的校验。控制台只向解析器传递自身生成的受限配置，CLI `check` 则要求本地操作员提供已审阅的文件和散列值。

```sh
ixp-agent check --platform frr --validator /usr/lib/frr/bgpd \
  --file approved.conf --checksum <授权下载返回的文件SHA-256>
```

RouterOS 目标明确为 7.20+ 的 instance 模型；其他 7.x 版本不能默认视为兼容。重复导入已存在的 managed 实例/表/策略会被脚本拒绝，所有新连接均 `disabled=yes`。前缀接收限制使用设备提供的 `input.limit-process-routes-*`，该数值并非精确已安装路由数。实际启用仍需设备本机校验和 Safe Mode 事务适配，当前尚未实现。

CHR7.24.6原生实验已纠正两项预览细节：非FIB表应省略`fib`开关，`fib=no`不是合法CLI形式；连接还必须显式指定与实例相同的`routing-table`，否则默认main。历史预览正文不改写，旧/新格式均保留严格凭据脱敏。新的类型化计划、API-SSL、状态读取和终端模块及已验证范围见[RouterOS原生记录](../routeros/NATIVE-7.24.6.md)；现有平台仍保持预览能力，不把局部原生测试当作完整自动发布验收。

原生测试无需启动路由守护进程：

```sh
BIRD_BIN=/usr/sbin/bird FRR_BIN=/usr/lib/frr/bgpd go test ./internal/routing -run TestNative -v
```

在只读开发沙箱中，可使用已安装的 bubblewrap 将 FRR 初始化的目录隔离在 tmpfs；这不要求提升宿主权限：

```sh
bwrap --ro-bind / / --dev /dev --tmpfs /var/tmp --tmpfs /var/lib --tmpfs /run \
  --bind /tmp /tmp env FRR_BIN=/usr/lib/frr/bgpd \
  GOCACHE=/tmp/go-cache go test ./internal/routing -run TestNativeFRRIntegration -v
```

上述测试证明原生解析器接受双栈配置并拒绝错误命令；不证明实际邻居建立、厂商设备特性或路由转发结果。Junos/RouterOS 未在真实设备执行变更。

官方语法参考：

- [FRR BGP 与 Route Server 文档](https://docs.frrouting.org/en/latest/bgp.html)
- [MikroTik BGP 参数](https://help.mikrotik.com/docs/spaces/ROS/pages/331612228/routing+bgp)
- [MikroTik 路由过滤器](https://help.mikrotik.com/docs/spaces/ROS/pages/74678285/Route+Selection+and+Filters)
- Junos 细节见 [JUNOS.md](JUNOS.md)。
