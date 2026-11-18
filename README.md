# MoeDove IXP Manager

<img src="web/public/moedove-logo.png" alt="MoeDove" width="180" />

Go + React + shadcn/ui 重写中的 IXP 管理平台。新服务不依赖 PHP、Composer、MySQL 或 Redis；前端使用本地静态资源。原 IXP Manager 的 GPL-2.0 许可和版权声明继续保留。

**状态：进行中的完整重写，尚未达到旧系统功能等价。** 已实现初始化、认证、资源管理、基础成员隔离、审计、导入导出和多平台路由配置版本流程。原系统代码保留作业务与数据迁移依据。完整差距和验收项见 [迁移清单](docs/MIGRATION.md)，不要直接替换正在运行的 IXP 管理系统。

## 本地运行

需要 Linux、Go 1.25+、Node.js 22+。构建会下载锁定的 Go 依赖；中文 PDF 字体随程序内嵌，运行时不依赖远程字体服务。

```sh
make build
./dist/ixp-manager
```

访问 `http://127.0.0.1:8080`，首次打开填写启动终端显示的初始化令牌，再设置管理员、IXP 名称和 ASN。没有默认密码。密码至少 12 字节；所有首次初始化请求均需要令牌。新数据保存在 `./var/state.json`，目录与文件分别使用 0700 / 0600 权限。数据目录有排他锁，同一目录只运行一个服务进程。

可以在启动前设置高强度随机 `IXP_SETUP_TOKEN`，并在初始化页面填写同一个令牌。未设置时服务会生成随机令牌，仅在未初始化时显示在启动日志中；保护这些日志。自定义令牌通过环境变量提供，不要放到命令行、版本库或分享链接中。公网访问请使用 HTTPS 反向代理并启用 `-secure-cookie`。代理应保留原始 Host；仅在明确配置 `-trusted-proxies`（例如 `127.0.0.1/32`）后才接受该代理追加的 X-Forwarded-For，用于登录限速和会话记录，默认忽略客户端伪造的转发头。

```sh
# 两个终端分别启动；Vite 将 /api 转发到 Go 服务。
go run ./cmd/ixp-manager
cd web && npm ci && npm run dev
```

BIRD 原生校验需要本机安装 BIRD 2，可通过 `-bird /usr/sbin/bird` 指定路径。缺少对应解析器时可管理资源和预览，但无法通过该平台原生校验。FRR 可通过 `-frr /usr/lib/frr/bgpd` 指定解析器。

BIRD 1 的路由服务器和收集器每个实例只填一个本地地址族。IPv4 用 `-bird1 /path/to/bird` 指定实际 BIRD 1 检查器，IPv6 用 `-bird1-ipv6 /path/to/bird6`。控制台按工件所属地址族校验和审批下载；v2 专用实例可由本机 `ixp-agent --platform bird1` 完成限时确认、回退与显式恢复，要求私密 target/socket、精确 base/next 文件校验和及运行指纹。v1 历史配置仅预览/校验，初次 v2 基线由运维离线建立，详见 [BIRD1.md](internal/routing/BIRD1.md)。

需要在另一台同架构的 Linux 主机运行时，构建后执行 `make bundle`。将生成的 `dist/moedove-ixp-manager.tar.gz` 解压后运行 `./ixp-manager -data /path/to/instance`。程序会从二进制旁读取前端文件。发布包包含全部 Go 工具和已构建的 UI，目标机可按所用路由或 DNS 平台单独提供原生检查器。升级时将新包解压到独立目录，停止旧进程并创建备份，再用新版 `ixp-backup verify` 检查归档。首次演练可恢复到新目录，确认能以新版启动后再切换。

## 容器

先设置随机 `IXP_SETUP_TOKEN`，再执行 `docker compose up --build`。访问地址同上。容器以非 root 用户运行，数据保存在命名卷。构建定义安装 BIRD 2、FRR、OpenBGPD、BIND 区文件检查器与 PowerDNS Authoritative/BIND backend，供本机离线校验使用；管理服务不启动这些路由或 DNS 守护进程。初始化页面同样需要填写设置令牌。

## 多平台配置发布

每份配置版本固定其平台、内容和文件 SHA-256，以独立密文文件保存。列表与预览只提供脱敏内容和带密钥的审批标识；授权下载才返回真实文件校验和，避免利用公开摘要猜测旧密码。不同平台使用各自适配器，不能混用校验结果。当前能力如下，其他平台仍属于迁移范围：

| 平台 | 本地检查 | 应用机制 |
| --- | --- | --- |
| BIRD 2 | 对应版本原生解析 | 原子替换、确认超时、失败回退、中断恢复 |
| BIRD 3 | 实际 BIRD 3 原生解析，不能降级用 BIRD 2 | 主版本绑定、限时确认、失败回退及版本绑定的中断恢复 |
| FRR | bgpd 原生 dry-run、运行配置漂移检查 | 本机看护服务 stage/confirm/cancel、超时回退、监督器重启恢复 |
| OpenBGPD | 实际 OpenBGPD 原生解析及严格受管配置检查 | Linux 独占实例看护、stage/confirm/cancel、超时及重启恢复；不是原生 confirmed-commit |
| Junos | XML 结构检查；不等于设备语义校验 | NETCONF candidate validate → confirmed-commit → 操作员确认，超时由设备回滚 |
| RouterOS 7 | 配置预览 | 目标校验和安全应用尚未完成，禁止标记为已校验 |
| BIRD 1 | 实际 `bird`/`bird6` 单族解析、审批下载；v1/v2 精确区分 | v2 专用本机限时确认、强读回、失败回退和显式恢复；不接管旧任意配置或共享实例 |
| Quagga 1.2.4 | 专用 RS 视图，使用实际 Quagga `bgpd` 隔离解析 | 控制台批准后交付文件。目标机通过专用守护程序暂存、确认或回退，进程替换会中断 BGP 会话 |
| Cisco | 适配进行中 | 按设备型号和实际软件版本核对 |

Junos 下发工具是 `ixp-netconf`；固定 SSH 主机密钥验证、候选配置锁和设备能力检查保护每次事务。部署前需确认设备实际支持的 NETCONF 能力，不能跳过设备校验。具体命令与限制见 [Junos 操作说明](internal/routing/JUNOS.md) 和 [平台能力说明](internal/routing/PLATFORMS.md)。

BIRD 2/3、FRR 和 OpenBGPD 还支持路由收集器角色。`observe` 保留过滤前/被拒绝路由供本机诊断，`authorized-only` 只承诺授权路由表；两者都禁止向邻居导出，并且不安装内核转发表。观察模式可使用空白名单，此时收到的路由不会进入授权 RIB。Junos/RouterOS 尚未满足该角色的隔离与观察要求，界面和后端会拒绝选择。详见[收集器说明](internal/routing/COLLECTORS.md)、[BIRD 3](internal/routing/BIRD3.md) 和 [OpenBGPD](internal/routing/OPENBGPD.md)。

RouterOS 已有分代计划、API-SSL、持久 SSH、固定目标观察器和持久发布监督器。[`ixp-routeros`](cmd/ixp-routeros/README.md) 提供只读检查及同用户私密 socket 管理。隔离 CHR 7.24.6 已验证 IPv4 和 IPv6 的生成策略、真实 BGP 通告与撤回。两套用例在正常通告、策略切换和取消恢复时都确认测试前缀没有进入 `main` 或其他表，专用表的 `fib` 开关未设置。完整生产发布仍在验收，控制台目前提供预览。[原生验证记录与复现方法](internal/routeros/NATIVE-7.24.6.md)。

管理员和操作员可在「配置中心」下载当前 RouterOS 草稿的规划 JSON。`ixp-routeros plan --handoff` 用这份文件和本机 `review` 生成可审阅的阶段计划，省去重录对等会话、地址和前缀策略。认证密钥通过本机私密文件提供，下载的 JSON 只携带认证版本。[RouterOS 规划流程](cmd/ixp-routeros/README.md#plan-from-ixp-manager)。

新平台的本机检查器通过 `-bird3 /path/to/bird`、`-openbgpd /path/to/bgpd` 配置。容器构建已配置发行版 OpenBGPD 检查器路径；BIRD 3 仍需另行提供匹配的可执行文件及依赖，不能用内置 BIRD 2 冒充。界面会显示检查器缺失，实际检查还验证主版本。BIRD 3 的 RPKI expire 至少600秒；OpenBGPD 的本适配器不支持运行时 RTR，不能忽略 `require-valid` 要求。AS112 DNS 包使用 `-pdns-server` 与 `-named-checkzone` 指定的原生检查器。

Quagga 路由服务器用 `-quagga-bgpd /path/to/bgpd` 指定真实的 1.2.4 检查器。检查使用独立用户和网络命名空间。批准后可下载或用 `ixp-agent fetch --platform quagga` 拉取配置及其实际 SHA-256，再在目标机检查并通过同 UID 私密 socket 交给固定的专用 `bgpd` 守护程序。隔离双栈验证已覆盖进程替换及回退，GR、TCP-MD5 握手和生产目标仍待验收。 [Quagga 平台说明](internal/routing/QUAGGA.md) · [本机事务操作](docs/QUAGGA-WATCHDOG.md)

BIRD 2/3 与 OpenBGPD 可明确选择 RFC1997 标准社区透传或由服务器处理，也可保留平台默认；未指定时分别保留各自原生行为，不暗加限制。旧 BIRD 模板的 `rfc1997_passthru` 可对应这个明确选择。其他厂商尚未验证该控制，不会接受同名选项冒充兼容。生成版本冻结所选语义，收集器无论如何都不导出路由。OpenBGPD 看护还要求私密固定 control socket、真实进程身份和独占实例；它不能检测其他 root 管理员的所有带外运行时修改。

升级提示：启用运行时 RPKI 的旧路由服务器会新增包含过滤路由的缓存限额。历史文件和摘要保持不变，但升级后的生成规则变化会使旧批准版本拒绝下载，需要重新生成、校验和审批；未启用 RPKI 的旧默认模板字节保持兼容。配置升级不会自动操作设备。

在「BGP 对等会话」可对已保存的会话执行「检查配置条件」。检查按当前库存核对成员、地址族、同 VLAN 的接入和前缀策略。停用的会话保持原状态，通过检查后可编辑启用，再在配置中心生成并校验版本。

### BIRD 2 流程

1. 添加机房、交换机、端口、VLAN、成员和接入连接；连接地址必须属于 VLAN 网段。
2. 添加路由服务器及 Peer。启用 Peer 必须有活跃成员，并在生成配置时匹配同 VLAN 的活跃连接。允许前缀是精确 CIDR 匹配，必须显式填写，不隐含更具体路由。
3. 在「配置发布」生成版本并审阅文本；使用实际 BIRD 2 执行 `bird -p`。
4. 管理员确认该版本的审批标识。如果网络资料或认证凭据已经变化，批准失败，需要重新生成。
5. 管理员或操作员下载已批准配置及文件 SHA-256，传到目标路由器，用 Go 代理再次校验和应用。网页批准不表示路由器已经更新。文件校验和与页面审批标识用途不同，不能互换。

```sh
ixp-agent check --platform bird2 --file reviewed.conf --checksum <授权下载的文件SHA256> --bird /usr/sbin/bird
# 仅在计划应用配置的目标路由器运行：
ixp-agent apply --platform bird2 --file reviewed.conf --checksum <授权下载的文件SHA256> \
  --target /etc/bird/bird.conf --bird /usr/sbin/bird \
  --birdc /usr/sbin/birdc --socket /run/bird/bird.ctl
```

代理要求现有的普通配置文件；拒绝符号链接和不安全路径。应用流程包括排他锁、备份、原子替换、BIRD 配置超时确认及失败恢复。代理的 `configure status` 检查只证明配置事务已完成，不证明所有 BGP 会话已建立；现有版本没有自动会话健康回执。IRR/RPKI 自动调度、收集器完整原因标签、Looking Glass 的其他厂商视图和其他设备适配仍在迁移清单中。

BGP 会话的认证操作要求管理员或操作员浏览器会话，并再次验证密码及已启用的 MFA。可生成随机 TCP-MD5 密钥（仅当次显示）或设置 1–80 字节的兼容密钥，支持字母、数字及 `+ / = _ -`；凭据管理接口不回显旧值，授权下载的生产配置仍包含实际密钥。更改路由器、成员、对端地址或 ASN 前必须先显式移除凭据。保存仅更新期望配置，两端需协调变更并重新生成、审核和发布配置。BIRD 2、FRR、Junos 与 RouterOS 预览共用这条凭据流程，各平台的实际发布能力仍按上表区分。

自动化下载使用单独的 `deployments:pull` 令牌，必须指定允许的 `routerIds`，只能取得该路由器当前仍有效的已批准版本。`deployments:read` 只允许读取脱敏信息。下载响应的 `X-Config-SHA256` 是代理所需的真实文件校验和。凭据与配置文件均使用实例 `security.key` 加密；普通 JSON 资源导出不包含它们，完整恢复请使用 `ixp-backup`。

在目标主机可用以下命令拉取指定版本。令牌文件应为 `0600`，也可用 `--token-stdin` 从标准输入读取；不要把令牌写入命令参数。命令要求 HTTPS、拒绝重定向，核对平台、路由器、版本及文件摘要后，以 `0600` 写入新文件，拒绝覆盖已有路径；它不应用配置。

```sh
ixp-agent fetch --server https://ixp.example.net --deployment <版本ID> \
  --router <路由器ID> --platform bird2 --output reviewed.conf \
  --token-file /etc/moedove/deploy.token
# 命令输出文件 SHA-256，用于后续 check/apply 或 ixp-netconf stage。
```

## 核心互联

核心互联采用独立工作台管理 ECMP、L2-LAG 和 L3-LAG：双端端口整体保存、版本核对、端口占用和子网校验，旧平面链路需显式归组。SNMP 物理状态与协议状态分开，两侧流量分别计算，缺数据保持未知。配置工具中的 `switch-core` 提供交换机核心接口/BGP 的类型化 JSON 计划，沿用审批和绑定下载；目标厂商适配、原生校验及安装/撤回仍需完成。模型、操作与迁移边界见[核心互联说明](docs/CORE.md)。

## DNS 与监测配置

「配置工具」提供固定类型的 DNS 反向区、Nagios 对象和 Smokeping 目标规则。规则修改使用修订冲突检查，生成结果独立加密保存；校验、审批、下载及历史清理沿用不可变版本流程。自动化生成/下载使用绑定 `profileIds` 的 `provisioning:write` / `provisioning:pull` 令牌，令牌不能修改规则或批准版本。

DNS 必须选择 VLAN 中一个完整已登记前缀，已有权威区需明确填写当前 SOA serial；只有确实未发布的新区才能选择「全新」。服务端保留已分配与已批准的序列号，阻止乱序批准、旧序列下载及删除重建后的回退；回滚内容应重新生成新序列。IPv4 支持 /8、/16、/24、/32，IPv6 支持 nibble 边界；RFC 2317 父子委派、区内 NS glue 和序列号回绕迁移仍需补齐，不会伪造完整区。

DNS 由真实 `named-checkzone` 校验，使用 `-named-checkzone /usr/bin/named-checkzone` 配置检查器。Nagios/Smokeping 是需要目标模板、命令或 probe 的 include 片段，控制台只检查受限结构；目标完整配置仍需原生验证。所有类型的批准均不代表已安装或重载服务。

```sh
ixp-provision fetch --server https://ixp.example.net --profile PROFILE_ID \
  --revision REVISION_ID --kind dns-arpa --zone 2.0.192.in-addr.arpa. \
  --output reverse.zone --token-file /etc/moedove/provision.token
ixp-provision check --kind dns-arpa --zone 2.0.192.in-addr.arpa. \
  --file reverse.zone --checksum ACTUAL_FILE_SHA256
```

命令只生成、拉取或检查文件，不安装或重载服务；下载要求 HTTPS、目标信息与真实文件摘要匹配，以 `0600` 原子保存且不覆盖旧文件。`ixp-provision probe` 可为 Nagios 读取真实 BGP/SNMP 观测，过期、缺身份或缺数据返回 UNKNOWN；库存启用状态不会被当成健康。操作边界、固定命令接入和完整示例见[配置工具说明](internal/provision/README.md)。

AS112 的专用本机运维使用独立 [ixp-as112](cmd/ixp-as112/README.md)：明确计划、artifact 与 native ELF pins，批准后原子安装，前台 owner 按持续 DNS 健康控制 BIRD 2/3 或 FRR，未知结果不自动重放。同主机离线更新使用显式 old/new pair 与 current 指针；它不接管共享实例，也不自动安装或启用 systemd 服务。

连接、路由器、交换机及成员预留地址可以记录 DNS 名称。禁用地址族的旧预留保留成员归属及 PTR，不进入监控；同成员连接可继承预留 DNS 名，释放或重新分配会清除前任名称。旧库中归属歧义或缺失的已分配地址会阻止迁移输出，即使未指定 `-strict`，也不会将其当作空闲地址。

## 账号与文档

「账号安全」提供密码修改、TOTP 认证器（二维码本地生成）、一次性恢复码、会话撤销以及有权限范围和有效期的 API 令牌。令牌只显示一次，默认只读；不允许通过令牌修改账号、导入数据、修改系统设置或批准路由配置。修改密码会撤销所有会话和令牌。

认证器密钥用实例独立的 AES-GCM 密钥加密。`var/security.key` 必须与 `state.json` 一起备份；丢失或不匹配时服务会拒绝启动。恢复码和 API 令牌只保存散列。

使用 HTTPS 时，登录页可选择「记住这台设备」。完成密码验证和账号已启用的双重验证后，浏览器获得最长 30 天的恢复许可。到期时间固定，恢复登录时会轮换许可。「账号安全 → 已记住的设备」可逐台或全部撤销。正常服务重启保留有效许可，从备份恢复实例后需要重新登录并选择记住设备。通过 TLS 反向代理运行时启用 `-secure-cookie`。

文档库提供目录、上传、下载、元数据修改、文件替换和 `.txt`/`.md` 编辑。可以选择工作人员、指定成员、所有登录用户或公开访问；目录权限更改会原子地应用到所有后代。公开文档使用独立只读入口，文件始终作为附件下载，校验 SHA-256，拒绝符号链接。单文件上限 25 MiB，文本编辑上限 1 MiB，总容量默认 1 GiB（`-document-quota` 可调整，含保留的 20 个历史版本）。文件替换与文本编辑需要当前校验和，避免覆盖他人的修改。

接入连接支持单端口与 LAG 聚合，容量由实际物理端口速率求和。允许同一成员在不同 VLAN 上复用同一个完整端口组；禁止跨成员占用和不一致的重叠聚合。IX-F 输出每条物理接口的实际速率，不把聚合总速率重复写到每个端口。

设置页可选择 IX-F 1.0、0.7 或 0.6 格式导出已保存资料。公开开关默认关闭，旧 `/api/v4/member-export/ixf/{version}` 和 `/apiv1/member-list/list/version/{version}` 路径也遵守同一开关；私有自动化导出使用 `/api/ixf/{version}` 和 `exports:read` 令牌。未知版本会报错，旧查询参数不会公开联系人或改写注册 ID。格式差异与迁移边界见 [IX-F 说明](internal/ixf/README.md)。

## 配线、地址和成员资料

「配线工作台」支持预留、双工配对、接通、停用、释放和预接线，要求理由和当前版本；冲突时整组回滚。历史记录只追加。LOA 固定签发快照，保存真实中文 PDF 与打印 HTML，嵌入 MoeDove 标识；释放、重分配、信息变化、过期或撤销后不再作为当前授权提供下载。附件按当前成员和文档权限检查，重新分配会清除旧租户关联。详见 [配线与 LOA](internal/loa/README.md)。

IPAM 向导会在 VLAN 网段内原子预留空闲地址，避开路由器和连接已占用的地址；使用中的地址不能释放，释放时校验预期所有者。IPv6 容量以十进制大整数字符串返回，不枚举整个地址空间。IP 和 MAC 采用规范化表示，防止不同写法绕过重复检查。设备支持机柜 U 位，重叠或超过机柜高度会被拒绝。

成员资料覆盖注册、账务、NOC 和采购订单字段；关联/内部组织可不提供 ASN，不会伪造 AS0，也不能配置无 ASN 的网络接入。旧数据转换器已补齐关联公司资料、隐私标签、备注、联系组、厂商、IXP 自有设备与未分配地址池；字段遗漏继续显式报告。

## 邮件与后台任务

默认不投递邮件。通过 `-smtp-config /etc/moedove/smtp.json` 指定主机上的私密配置后，可使用密码找回、成员邀请和 API 令牌到期提醒。生产投递要求证书校验成功的 TLS/STARTTLS；详细配置见 [通知说明](internal/notify/README.md)。

密码重置令牌单次有效，且不会绕过已启用的 MFA；已有账号接受成员邀请需要登录、重新验证密码及 MFA。邮件正文加密保存，管理员只看到投递状态和安全元数据。固定后台任务提供重试、崩溃租约、提醒去重与过期数据清理，不执行任意脚本。

管理员可在设置中定义邮件列表，用户在「账号安全 → 邮件列表」保存自己的订阅或退订偏好。邮箱改变后需重新选择；导出使用独立的管理员 `mailing-lists:read` 权限，并保留旧只读接口。保存偏好不会发信，也不表示外部 Mailman 已应用；当前同步与旧偏好迁移进度见 [邮件列表说明](docs/MAILING-LISTS.md)。

工作人员可在「联系人 → 按联系组导出」选择组名或类型，也可限定成员。JSON 保留字段原值，CSV 适合在表格中查看电话和备注。[联系人导出说明](docs/CONTACT-EXPORT.md)

## 交换网络、设备发现与对接

交换网络可以独立使用相同 VLAN 编号；交换机、接入及整个 LAG 必须属于同一网络。主网络可以原子切换，成组移动已有 VLAN/交换机使用专门的事务入口。一个 VLAN 可声明多个真实网段，IPAM 要求明确选择目标网段。新建网络默认不公开到 IX-F；多前缀无法放入 IX-F 1.0 单前缀字段时省略该可选元数据，仍保留并校验成员实际地址。

`ixp-snmp` 使用本机私密配置执行固定只读 MIB 采集，默认要求 SNMPv3 authPriv；旧 v2c 需明确选择。发现结果进入审阅提案，只能在管理员批准后建立接口映射，不自动改写成员归属、端口意图或地址。设备、接口身份与采集前的归属修订都要匹配；重分配、ifIndex 复用和迟到数据不能继承旧成员的观测。原始硬件/FDB 信息仅工作人员可见。详见 [SNMP 说明](internal/snmp/README.md)。

PeeringDB 对接仅查询固定的公开接口，通过私密 `-provider-config` 配置允许的来源；也可用 `ixp-sync` 获取后导入精确校验和快照。提案列出可采纳字段与被保护的 ASN/IP/带宽差异，管理员按当前数据摘要选择应用；只读导出、清理原始快照和保留审计是独立操作，不自动覆盖网络配置。详情见 [外部数据说明](internal/integrations/README.md)。

双边互联工作台支持同一可见 VLAN 上的申请、接受、拒绝、撤回和撤销。对端成员管理员确认，当前成员与 ASN、网络关系和可见性仍需有效；矩阵区分人工协议、配置关系和实际观测，不把申请获批当作 BGP 已建立。组织切换会轮换 CSRF 并通知其他标签页重新加载，旧表单不能误写到另一个组织。

## 运行监控与路由策略

「Looking Glass」通过本机采集器的短期只读租约提供路由详情、过滤/策略前视图和发往指定邻居的路由。成员仅能查询自己仍有效的 peer；表级查询仅工作人员可用，并要求本地采集器显式开启和实际 router 身份证明。结果保留 AS_SET、多下一跳及标准/大型社区，未知属性、截断、失败和旧样本分别显示。不会把社区标签推断成已证实的过滤原因，也不会把一次命令完成当成健康。接口、队列与保留边界见[路由诊断说明](docs/LOOKING-GLASS.md)。

`ixp-collector --looking-glass` 显式启用租约轮询，或使用 `--looking-glass-only` 只处理诊断；worker 令牌需要绑定路由器的 `looking-glass:run`。普通查询令牌使用 `looking-glass:read`，成员令牌保持组织范围。BIRD 2/3、FRR、OpenBGPD 的可用视图由实际适配能力决定，缺原生支持时返回不支持，不伪造空表。管理服务不持有任意路由 CLI 或远端 shell 权限。

监控页显示采集到的实际端口速率和 BGP 会话。使用 `ixp-collector` 在目标主机读取明确映射的 Linux 接口计数器及 BIRD 2/3、FRR、OpenBGPD、Quagga 会话状态；采集令牌需要 `monitoring:write` 并绑定具体路由器。采集器先取得归属修订再读取计数器；旧修订、端口重复分配和观测邻居与当前配置不一致时不会冒充新数据。没有样本、计数器重置或样本过期时不会显示伪造的零流量。成员历史按采样时所有者过滤，重新分配端口不会暴露前一成员流量。步骤见 [采集器说明](internal/telemetry/README.md)。

策略中心导入 `ixp-policy` 采集的 IRR/ROA 快照，展示有效期、模拟结果与允许列表差异；显式包含 AS-SET 下游时保留每个前缀的起源 ASN 配对。应用审阅结果只改变待下发资料，仍需生成、校验、批准及部署新的路由配置。ROA 文件必须来自可信 RPKI 验证器，本工具不替代证书签名验证。

BIRD 2/3 和 FRR 支持运行时 `require-valid`：只接受 ROA_VALID，缓存失效后拒绝未知路由。未支持的厂商会拒绝启用，避免默默降级。FRR 需要 RPKI 模块和允许用户/网络命名空间的校验环境；原生 dry-run 会启动 RTR 模块，所以带 RPKI 的校验强制在无外网的网络命名空间里执行。容器和系统安全策略若禁止该能力，会明确校验失败。运行 FRR 守护进程也需要显式加载其 RPKI 模块。

成员账号可以切换被分配的组织；成员管理员只能维护当前组织允许修改的资料与联系人。成员 API 令牌绑定创建时选中的组织，浏览器切换不会改变令牌的可见范围。暂停/关闭会原子禁用相关连接和 Peer 的期望配置；开通不会自动重新启用它们，也不会直接触碰路由器。

## 长期流量与 sFlow

长期计数器统计与 sFlow 采样分析已经分开接入。计数器总量保持整数精度，提供 UTC 分层归档、覆盖率、回绕下界和冻结的历史归属；sFlow 管理端仅处理有界元数据和采样估算，不混作完整接口流量。接收器须经过范围审批，采集器显式启用后才能按短期逐包授权转发净化包头。旧 MRTG/RRD/P2P 统计使用独立预览、审核导入和历史权限，不与新计数器总量合计。运行方式、保留上限和剩余范围见[流量说明](docs/TRAFFIC.md)。

“地址分配”还可对照交换机学习到的 MAC 与已有登记。只有当前唯一物理接口/VLAN/成员的观测能进入人工审阅，冲突、过期或归属变化会拒绝旧确认；登记不修改在线设备。手工及迁移登记同样限制非零单播 MAC 在同一实际 VLAN 内唯一。详见[MAC 学习审阅](docs/MAC-LEARNING.md)。

右上角全局搜索或 Ctrl/Cmd+K 可查找当前有权读取的成员、网络资产、备注、配线、文档和接收器，点击结果定位到具体记录。API搜索逐领域核对scope，隐藏字段不参与成员匹配或计数。类型、旧查询语法和分页规则见[搜索说明](docs/SEARCH.md)。

## RIPE Atlas

「RIPE Atlas」保留同 VLAN/地址族成员间的有向互测矩阵，也支持单个 ping/traceroute。来源探针与目标明确冻结，创建前展示总样本和 credits 上界，管理员重新认证并明确批准；公有测量还需单独确认公开。`-atlas-config` 省略时禁用所有外部操作，启用配置必须是 `0600` 私密文件。令牌仅支持 `atlas:read`，不能批准、创建或停止测量。

每个子测量在发送前先保存创建意图；超时、响应不明确或检查点写入失败会进入 uncertain，不能自动重复收费 POST。核对只查询已有对象，停止只能作用于本实例已证明拥有的测量。矩阵的已尝试项不会在 resume 中重发；丢包、错误和多路径回复保留原意，不用计划目标补造未上报的实际地址，也不将缺 RTT 显示为零。

本地额度预留账本独立于可清理的历史，清理记录不能绕过限额；估算不代表实际消费。原始响应和回执采用有界加密快照并纳入备份，API key 配置另行保管。当前验证全部使用本地 fake provider，没有创建真实测量。参数、公开范围、审批/执行与恢复合同见 [Atlas 说明](internal/atlas/README.md)。

## 备份、恢复与账号救援

创建备份前先停止源管理服务。数据目录锁会拒绝与运行实例并发备份。校验和恢复读取已有归档，恢复发布到新的数据目录。备份包含账户和密钥，请保存在受保护的位置。

```sh
mkdir -m 700 backups
./dist/ixp-backup create --data ./var --output backups/snapshot.ixp-backup.tar.gz
./dist/ixp-backup verify --input backups/snapshot.ixp-backup.tar.gz --work-dir backups
./dist/ixp-backup restore --input backups/snapshot.ixp-backup.tar.gz --data ./var-restored
# 校验新目录后，用 -data ./var-restored 启动服务。
```

备份包含状态、加密密钥、BGP 凭据、路由与配置工具的加密版本、DNS 序列计数、所有被引用的文档/LOA/策略快照、加密发件箱、独立遥测日志和长期分区、sFlow 聚合与序列检查点、外部/设备发现快照，以及 Looking Glass 和 Atlas 的加密快照、回执与额度账本。备份前会将旧的内嵌路由配置迁移为密文文件，保留原有内容和审批身份；已显式清理的版本保留元数据。恢复验证文件清单、散列、权限和数据关系，拒绝路径穿越、链接、超限文件、损坏归档及已有目标目录。当前工具支持总计不超过 8 GiB、65536 个文件及 128 MiB 状态文件，单文件仍有独立限制。会话不会跨重启保留，本机外部服务凭据配置需另行保管。

遗失账号密码时，使用 `ixp-admin reset-password --data-dir ./var --email ACCOUNT --password-stdin` 从标准输入读取新密码。不要把密码放在命令行或环境变量里。默认保留 MFA；确实需要移除认证器时显式添加 `--disable-mfa`。恢复操作会撤销令牌并留下审计记录。

FRR 的看护服务需要监督器保持运行；进程被强制结束后的回退依赖重新启动。操作步骤和 systemd 示例见 [FRR 下发说明](internal/routing/FRR-WATCHDOG.md)，这不是 FRR 自带的原子事务，也不代替实网连通性检查。

## 数据与权限

通用资源编辑、删除现在要求打开时的强 ETag，缺失返回 428、过期返回 412。浏览器保留冲突草稿，核对字段后再次提交；密码冲突后清空。API 调用方需显式发送 `If-Match`，不能无条件覆盖记录或自动拿新标识重试旧数据。规则、文档等专用修订协议保持独立，详见[并发编辑接口](docs/RESOURCE-EDITING.md)。

- admin：用户管理、导入导出、设置及配置批准；operator：资源编辑、配置预览和校验；viewer：只读；member：只能读取自己成员的授权资料。
- 会话使用 HttpOnly/SameSite Cookie；变更请求要求 CSRF token，并检查 Origin；登录失败限速。密码使用随机盐 PBKDF2-SHA256，600000 次迭代。
- 所有写入先校验整个关系图，再原子持久化；引用中的记录不能删除。审计记录与数据变更一起提交。
- 页面数据导出不包括账户、密码、设置和配置版本，不能用作完整恢复备份。请使用上述离线备份工具，或停止服务后复制整个私有数据目录，包括 `security.key` 和 `blobs/`。
- 目前持久层是单进程文件快照，适合开发和迁移验证；大规模历史遥测、多实例服务和在线备份尚未实现。

旧数据转换命令、字段限制、遗漏警告与导入事务说明见 [迁移清单](docs/MIGRATION.md)。在设置页选择资源 JSON 后，服务器会先在当前库存副本上校验。确认提交时会用最新库存再次检查同一文件。旧路由器库存的支持范围见 [路由器迁移说明](docs/LEGACY-ROUTERS.md)。

已转换的旧路由器可按明确来源规则生成停用的 BGP 邻居草稿。账户管理员也可以把旧操作日志的摘要导入独立历史视图。[邻居草稿](docs/LEGACY-PEER-DRAFTS.md) · [旧审计摘要](docs/LEGACY-AUDIT.md)

## 验证

```sh
make test
# 在具备 BIRD 2 的环境运行真实解析测试：
BIRD_BIN=/usr/sbin/bird go test ./internal/routing -count=1
```

Go 测试覆盖认证、CSRF、权限边界、引用完整性、原子导入、持久化、配置策略与失败恢复。前端通过 TypeScript 和生产构建检查。运行中的网络切换需要另行进行隔离环境验证。

---

<details>
<summary>原项目说明与版权</summary>

![IXP Manager Logo](https://github.com/inex/IXP-Manager/raw/main/public/images/ixp-manager.png)

[INEX](https://www.inex.ie/) are pleased to release [IXP Manager](http://www.ixpmanager.org/) under an open source license (the
GNU Public License V2) which we hope will benefit the wider IXP community,
and especially new and small IXPs looking to expand.

[![CI Tests (ex Dusk)](https://github.com/inex/IXP-Manager/actions/workflows/ci-ex-dusk.yml/badge.svg)](https://github.com/inex/IXP-Manager/actions/workflows/ci-ex-dusk.yml)  - courtesy of [GitHub Actions](https://github.com/inex/IXP-Manager/actions/workflows/ci-ex-dusk.yml).

## Main Links

* [IXP Manager Website](https://www.ixpmanager.org)
* [Documentation](https://docs.ixpmanager.org) including:
  * Installation [overview](https://docs.ixpmanager.org/latest/install/), 
    [by automated script](https://docs.ixpmanager.org/latest/install/automated-script/),
    and [manually](https://docs.ixpmanager.org/latest/install/manually/)
  * [Upgrade Instructions](https://docs.ixpmanager.org/latest/install/upgrading/)
* [Releases](https://github.com/inex/IXP-Manager/releases)
* [List of Users](https://www.ixpmanager.org/community/world-map) ([register here](https://www.ixpmanager.org/community/users/submit))
* [How to Get Help](https://www.ixpmanager.org/support)
* [Presentations & Talks](https://www.ixpmanager.org/support/talks)
* [Sponsors and Sponsorship](https://www.ixpmanager.org/sponsors)

## Documentation

Our documentation is hosted on GitHub pages: https://docs.ixpmanager.org/.

## About IXP Manager

For detailed information, see: https://www.ixpmanager.org/

IXP Manager is a full stack management platform for Internet eXchange Points (IXPs) which includes an administration and customer portal; provides end to end provisioning; and both teaches and implements best practice.

IXP Manager began as an internal project at INEX circa 2007. Over that time it has changed a lot, but always for the better and always to help us run INEX efficiently and with lower overhead. Three of our key requirements as an IXP are security, consistency and reliability. IXP Manager has been designed to help us achieve these.

> Our vision for IXP Manager as a technology is that it will enable the creation of IXPs wherever they are required which in turn will create a stronger, open, more robust and better connected internet. And not only is it hoped that IXP Manager will help start these new IXPs but that it will also enable these IXPs to launch from the best place possible: secure by default, proven configurations and designs, implementing best practice.

Our hope is also to gather and foster an *IXP Manager community* - a place where the many IXPs that use IXP Manager can come and help each other, learn from each other and share their stories. Our website will highlight some of the internet heroes in the IXP space and feature case studies.

Today, IXP Manager is in use by many IXPs around the world - with more being added every month. This is something that gives all of the team at INEX, our community sponsors, the developers and contributors immense pride. It is also quite sobering - it reminds us all to keep all the community in mind when developing and prioritising new features. And it focuses our attention on maintaining the excellent reliability and security standards that have been set over the past two decades. 

See also: https://www.inex.ie/

Authors:

* [Barry O'Donovan](https://www.barryodonovan.com/) 
* Nick Hilliard

Copyright (C) 2009 - 2025 Internet Neutral Exchange Association Company Limited By Guarantee.
All Rights Reserved.


 
## License

This file is part of IXP Manager.
 
IXP Manager is free software: you can redistribute it and/or modify it
under the terms of the GNU General Public License as published by the Free
Software Foundation, version v2.0 of the License.

IXP Manager is distributed in the hope that it will be useful, but WITHOUT
ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or
FITNESS FOR A PARTICULAR PURPOSE.  See the GNU General Public License for
more details.
 
You should have received a copy of the GNU General Public License v2.0
along with IXP Manager.  If not, see:
 
http://www.gnu.org/licenses/gpl-2.0.html


</details>
