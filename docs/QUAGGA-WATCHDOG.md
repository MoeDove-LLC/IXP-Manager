# Quagga 本机配置事务

`ixp-agent` 可以在目标 Linux 主机上监督一个专用 Quagga 1.2.4 `bgpd`。暂存会停止旧进程并启动候选配置，因此已有 BGP 会话会断开并重新建立。取消也会替换进程。请将两项操作安排在独占维护窗口内。

## 启动固定实例

使用为该实例私密 `<state-dir>/bgpd.vty` 路径编译的实际 Quagga 1.2.4 可执行文件。将其绝对路径和实际 SHA-256 固定在服务启动参数里。实例以单一 UID、零 capabilities 和 `NoNewPrivs` 运行。控制 socket、VTY 和持久状态路径由同一 UID 管理，目录权限为 `0700`。选择该实例专用的 BGP 端口，下面使用 `1179`。

首次启动提供经审阅的私密引导配置及其实际 SHA-256。服务重启复用同一实例绑定和原有持久状态目录。

```sh
ixp-agent serve --platform quagga \
  --validator /opt/quagga-1.2.4/sbin/bgpd --binary-checksum BGPD_SHA256 \
  --state-dir /var/lib/moedove-quagga --socket /var/lib/moedove-quagga/agent.sock \
  --bgp-port 1179 --exclusive-instance-acknowledged \
  --bootstrap-file /srv/private/reviewed-initial.conf --bootstrap-checksum INITIAL_SHA256
```

由本机服务管理器监督 `serve`。启动后可用 `status` 读回已提交配置的 `activeChecksum`。重新打开有待确认事务的日志时，守护程序先恢复原已提交配置，再提供控制接口。

## 暂存、核查和决策

通过既有 `ixp-agent fetch --platform quagga` 获取已审批的配置文件及真实文件 SHA-256。暂存文件要求属于当前 UID、为单硬链接普通文件且权限为 `0600`。

```sh
ixp-agent status --platform quagga --socket /var/lib/moedove-quagga/agent.sock

ixp-agent stage --platform quagga --socket /var/lib/moedove-quagga/agent.sock \
  --file /srv/private/approved.conf --checksum APPROVED_SHA256 \
  --base-checksum ACTIVE_SHA256 --timeout 120

ixp-agent confirm --platform quagga --socket /var/lib/moedove-quagga/agent.sock \
  --transaction EXACT_TRANSACTION_ID --checksum APPROVED_SHA256
```

`stage` 要求 `--base-checksum` 与已提交版本一致。收到 `pending` 后，先核对预期对等会话和实际路由行为，再使用返回的精确事务 ID 与候选 SHA-256 确认。确认期限为 60 至 3600 秒。若决定回退，以相同 ID 与候选 SHA-256 执行 `cancel`。期限到达后守护程序恢复原已提交版本。请求连接中断时先读取 `status`，由持久事务状态决定下一步。

控制接口只对同 UID 的私密 Unix socket 客户端开放。请求仅包含固定状态操作、候选配置与两个摘要，或精确事务决策。进程、监听端口及状态路径由 `serve` 启动参数设置。管理控制台负责校验、批准和交付文件，目标机事务由操作员明确执行。隔离双栈测试已覆盖进程替换、确认及回退。GR、TCP-MD5 握手和生产目标仍待验收。

独立原生验收在候选配置已实际通告双栈路由时，仅对生产构建的 `ixp-agent` 监督进程发送 SIGKILL。候选进程和监听退出，旧会话清除 RIB。外来监听器存在时服务拒绝重开且原事务日志保持原字节。释放该监听器后，新监督进程重新加载最后确认的配置，并以文件字节、VTY 运行指纹和双栈 BGP 确认恢复。试验用时 29.27 秒，证据见 `.cache/quagga-supervisor-sigkill/results.json`。
