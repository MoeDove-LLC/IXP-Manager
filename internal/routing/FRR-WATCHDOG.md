# FRR 本机确认与回退

FRR 使用独立的本机看护进程，流程为：**预览与审批 → stage → 检查实际邻居/路由 → confirm**。`stage` 会改变运行配置；没有确认时会恢复先前配置。`cancel` 可以主动回退。Web 服务只生成审批快照，不持有路由主机权限。

这套机制是进程监督下的补偿回退，不是 FRR 原生原子事务。`frr-reload.py` 按差异执行命令，中间可能发生短暂变化；部分命令失败也会触发恢复。看护进程被 SIGKILL 或机器重启时不能继续执行计时器，必须由 systemd 等监督器重新启动，启动后会先恢复所有未确认事务，再接受请求。设备停机或 FRR 不可用期间无法保证回退完成，恢复日志会保留并重试。不能在没有重启监督器的环境声称具有进程死亡后的自动回退。

## 使用范围

- 自动变更只接受当前生成器的完整 managed 配置和独立 `MOEDOVE` BGP view，不能导入任意 FRR CLI 命令。
- 同一独立 view 可明确切换 `route-server` / `route-collector`；收集器文件还必须保留每个激活地址族的出站拒绝、首 ASN 检查，以及观察模式的预策略留存和包含过滤路由的数量上限。角色切换仍需完整审批并按 stage/confirm 流程执行。
- 目标文件必须是独立 bgpd 实例的 `bgpd.conf`，不能传入整机 `frr.conf`。
- 每次 stage/confirm 前，使用官方 reload helper 的只读 `--test --daemon bgpd` 比较运行状态与已审阅磁盘基线；任何差异都拒绝覆盖。成功 reload 后再比较，要求差异为空。
- 带外修改会造成漂移，需操作员明确处理；不能把运行配置直接视为可信的新基线。待确认事务期间不要直接改写 managed 配置：超时回退会恢复原先审阅的基线。
- 目标文件独占锁与持久 state-dir 绑定阻止并发变更或换目录丢失恢复日志。
- 运行目录 0700，控制 Unix socket、恢复快照和日志 0600。服务不监听 TCP。

## 首次接管

先在实验环境准备独立 bgpd 实例、工作中的配置文件及 VTY socket。下面命令由路由主机管理员执行；项目开发过程不会代为操作实际设备。

对于非 managed 配置，必须使用显式离线 bootstrap。参数中的两个 SHA-256 分别对应授权下载响应 `X-Config-SHA256` 返回的文件散列和当前磁盘文件；控制台的脱敏审查标识不是文件 SHA。不要用未经审核的新文件替代旧基线值。

TCP-MD5 密钥随授权文件传递，不能从脱敏预览复制安装。新候选或回退内容含密钥时，目标不能具有执行权限、组写权限或其他用户访问权限；先设置正确 owner/group 和 `0600` 或 `0640`。watchdog 的候选及回退快照始终为 `0600`。解析与 reload 错误不会返回原始输出，避免设备或工具回显完整、截断或旧密钥；若权限在事务期间被放宽导致回退失败，应收紧权限后让 watchdog 重试。

```sh
ixp-agent bootstrap --platform frr --target /etc/frr/bgpd.conf \
  --validator /usr/lib/frr/bgpd --file approved.conf \
  --checksum APPROVED_SHA256 --base-checksum CURRENT_FILE_SHA256
```

bootstrap 只在磁盘安装原生校验通过的 managed 文件，并保存 `.moedove-bootstrap-original`；**不重载运行守护进程**。备份已存在或目标已 managed 时不会覆盖。管理员需要在维护窗口让专用 bgpd 使用该文件启动；看护服务会拒绝与该文件存在差异的运行实例。

Debian 类环境需要 `frr` 与 `frr-pythontools`；后者提供 `frr-reload.py`。当前实际检验的 reload helper 来自 FRR 10.3。服务账号需要读写目标所在目录以原子重命名文件，以及连接专用 bgpd VTY socket 的权限，不需要网络管理 capability。`/var/lib/frr` 和 `/run/frr` 必须由 FRR 安装/服务预先创建；实际 FRR 10.3 dry-run 已在这两个目录只读、`/var/tmp` 私有可写的隔离环境验证，不需要给看护服务写入整个 FRR 运行目录。示例 systemd 单元见 `cmd/ixp-agent/deploy/moedove-frr-agent.service`。安装前核对目标、目录、发行版用户组和 helper 路径。该单元仅作为部署文件提供，未在开发环境启用。

```sh
ixp-agent serve --platform frr --target /etc/frr/bgpd.conf \
  --state-dir /var/lib/moedove-frr --socket /run/moedove-frr/agent.sock \
  --validator /usr/lib/frr/bgpd --reload /usr/lib/frr/frr-reload.py \
  --vtysh /usr/bin/vtysh --vty-socket /run/frr
```

上面的前台命令便于诊断；正式运行必须由具有 `Restart=always` 和整个控制组子进程清理行为的监督器管理。停止服务会回退未确认事务。已确认配置不会在正常退出时回退。

## 日常变更

```sh
ixp-agent stage --platform frr --socket /run/moedove-frr/agent.sock \
  --file approved.conf --checksum APPROVED_SHA256 \
  --base-checksum CURRENT_FILE_SHA256 --timeout 120

ixp-agent status --platform frr --socket /run/moedove-frr/agent.sock

ixp-agent confirm --platform frr --socket /run/moedove-frr/agent.sock \
  --transaction TRANSACTION_ID --checksum APPROVED_SHA256

# 或主动回退
ixp-agent cancel --platform frr --socket /run/moedove-frr/agent.sock \
  --transaction TRANSACTION_ID --checksum APPROVED_SHA256
```

stage 返回 JSON，含事务 ID、checksum、deadline 和状态。确认窗口为 60–3600 秒，在运行配置修改前开始计时。确认前应使用独立监控检查 BGP 会话和预期路由；服务验证语法、命令执行、运行配置收敛，不宣称端到端连通性已经成立。CLI 请求超时或连接断开后先查 status，避免把未知结果当作失败后重复提交。

## 恢复与故障

事务日志在任何磁盘/运行变更前写入并同步，恢复快照校验和保存在日志内。回退时重新原生校验旧配置，恢复磁盘，再用同一个 helper 恢复 live 状态并检查差异。恢复失败状态为 `rollback-failed`；服务会重试，重启也会先恢复。不得删除该日志来绕过失败；先修复工具路径、VTY 连接、文件权限或快照完整性。

一旦 target 创建 `.moedove-frr-state` 绑定，就应持续使用同一 state-dir。迁移必须在没有待确认事务时由管理员连同状态目录一起进行，不能用新的空目录跳过恢复。

验证包括：受限配置拒绝、基线/运行漂移、并发 stage、部分重载失败、确认超时、取消、进程死亡后的启动恢复、失败回退重试、快照篡改、Unix socket API 与优雅退出。生命周期测试使用模拟 helper，不接触实际 FRR daemon，包含真实子进程 SIGKILL 后重新启动的恢复测试。另有 FRR 10.3 官方 reload helper 与 vtysh 的离线 `--test --input` 验证：相同配置没有差异，最大前缀变更产生可读差异；该测试不会连接 daemon。原生 `bgpd --dryrun` 的独立测试仍适用。

```sh
FRR_RELOAD_BIN=/usr/lib/frr/frr-reload.py VTYSH_BIN=/usr/bin/vtysh \
  go test ./internal/routing -run TestNativeFRRReloadPlanIntegration -v
```

官方依据：[FRR reload helper 的差异与 per-daemon 选项](https://docs.frrouting.org/en/latest/frr-reload.html)。
