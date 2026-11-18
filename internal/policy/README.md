# IRR / RPKI 策略快照

本模块提供有边界的 IRR 收集、ROA 缓存导入、前缀–来源 ASN 授权及只读策略模拟。采集成功只产生待审核快照，不修改对等体白名单，不审批配置，也不连接路由器执行变更。

## 采集与审阅

```sh
ixp-policy irr --server whois.radb.net:43 --sources RADB \
  --as-set AS-EXAMPLE --ttl 1h --output irr.json

ixp-policy roa-fetch --url https://validator.example/vrps.json \
  --ttl 1h --output rpki.json

ixp-policy roa-import --input validated-vrps.json --source internal-validator \
  --ttl 1h --output rpki.json

ixp-policy candidates --input irr.json --asn 64501
ixp-policy candidates --input irr.json --asn 64501 --include-as-set-origins
ixp-policy simulate --input request.json
```

`irr` 要求操作员显式指定可信 WHOIS server，最多递归8层、1024次查询、512个 ASN、25万条路由、单响应8MiB/总响应64MiB。超限、循环、查询失败或错误 origin 不产生部分可用快照。解析 RPSL route/route6、origin、续行以及 AS-SET members/mp-members。`mbrs-by-ref` 只接受匹配维护者（或该对象显式 ANY）的 member-of 成员，若对象带 registry source，反向成员必须属于同一来源。同名 AS-SET 返回多个对象时拒绝合并，要求选择单一权威 registry，避免混合不同维护者的信任规则。

ROA 输入必须来自已经验证 RPKI 证书链的可信验证器。当前支持 Routinator 风格 JSON 的 `metadata.generated/generatedTime` 和 `roas`。本工具验证 VRP 数据结构并计算路由来源状态，**不自行获取 TAL、验证证书链或 ROA 签名**。源生成时间是必须字段：过期文档不能因刚刚被下载而重新变成新数据。默认只允许 HTTPS、禁止重定向和 URL 内嵌凭据；可信本地 HTTP 缓存需要显式 `--allow-http`。

快照写入采用0600临时文件、fsync、原子重命名。TTL 不能超过24小时。读取、模拟和候选生成都会重新检查有效期，过期、未来时间、非法 maxLength、缺失/null 集合均拒绝使用。明确空 `[]` 表示完整但无记录的缓存，与字段缺失不同。

## 模拟与前缀–来源关联

快照格式：

- IRR：`{version:1,fetchedAt,expiresAt,source,asSet,asns:[],routes:[{prefix,originAsn}]}`。
- RPKI：`{version:1,fetchedAt,expiresAt,source,roas:[{prefix,maxLength,asn}]}`。
- 模拟请求：`{peerAsn,routes:[{prefix,originAsn,asPath?:[]}],allowedPrefixes:[],allowedRoutes?:[],irr?,rpki?,requireIrr,rejectRpkiNotFound}`。

模拟输出逐条 `accepted`、`rpki`、`reasons`、`pathValidated`。没有提供 AS path 时仅能模拟 peer 自身起源的路由，且 `pathValidated=false`；下游来源必须显式提供路径。RPKI 状态独立于路径长度等本地策略计算：路径过长可以被策略拒绝，但仍显示真实的 valid/invalid/not-found。

VRP 匹配检查全部覆盖前缀；任何满足 origin 和 maxLength 的授权都能使该路由成为 valid，而不是错误地只选最长覆盖项。存在覆盖但没有任何匹配授权为 invalid，没有覆盖为 not-found。AS0 VRP 不能授权正常 BGP 来源。验证状态与是否拒绝是不同概念：模拟中选择 RPKI 快照默认拒绝 invalid，`rejectRpkiNotFound=true` 再拒绝 not-found。

默认候选仅限指定 ASN 自身来源。全 AS-SET 模式必须显式选择，且 peer ASN 必须出现在展开集合内；输出保留每个 prefix/origin 对，不能只存前缀并丢弃来源关系。对等体 `allowedRoutes` 一旦启用，必须完整覆盖 `allowedPrefixes`；所有渲染器额外约束最后一个 AS。BIRD 使用 `bgp_path.last`，FRR 使用来源邻居限定的入口 route-map 和首尾 AS-path ACL，Junos 使用每来源策略项，RouterOS 使用数值 AS-path 条件。RouterOS 仍仅预览；Junos 仍需设备原生校验。

## 运行时 RTR

本项目目前为 BIRD 2 和 FRR 生成严格 `require-valid` 模式；默认 off。开启后 unknown 和 invalid 均不导入，缓存缺失/失效不会退回 prefix-only 接受。原有精确前缀、第一跳 ASN 和最大路径限制同时生效。其他厂商的本项目 RTR 适配尚未实现，要求开启时会明确拒绝，不能静默降级。

BIRD 配置启用 import table、rpki reload、固定 expire keep。FRR 配置要求 `frr-rpki-rtrlib`、运行 bgpd 时 `-M rpki`、`match rpki valid` 和 inbound soft-reconfiguration；expire最小600秒。ROA更新/失效需要守护进程重评估并传播BGP撤回，并非零延迟切换。RTR TCP 需可信、受控的管理网络或本机验证器。

FRR 10.3 即使 `--dryrun` 也会在解析时启动 RTR 线程。为避免预览访问缓存，本项目对含 RPKI 的原生解析强制创建独立用户/网络命名空间。内核或容器禁止该操作时校验失败；不会退回未隔离执行。原生模块路径可由主机管理员通过 `MOEDOVE_FRR_MODULE_DIR` 指定，配置内容不能指定可执行路径。运行宿主看护服务时保留命名空间能力；systemd 模板允许 AF_INET/AF_INET6 用于私有命名空间内的解析，控制接口仍为 Unix socket。

测试使用本地假 WHOIS/HTTP 服务、RFC 状态反例、真实 BIRD2.17.5 和 FRR10.3 模块解析。没有使用测试来声称真实网络中的会话或转发已验证。

参考：[RFC6811](https://www.rfc-editor.org/rfc/rfc6811.html)、[RFC8481](https://www.rfc-editor.org/rfc/rfc8481.html)、[Routinator JSON](https://routinator.docs.nlnetlabs.nl/en/stable/output-formats.html)、[IRRd 来源查询](https://irrd.readthedocs.io/en/stable/users/queries/whois/)、[BIRD RPKI](https://bird.nic.cz/doc/bird-2.17.1.html)、[FRR RPKI](https://docs.frrouting.org/en/latest/bgp.html)。
