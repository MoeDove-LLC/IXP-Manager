# 核心互联

核心互联以 bundle 为保存单位，包含固定的两侧交换机、两侧逻辑接口，以及一组成对物理端口。支持 ECMP、L2-LAG、L3-LAG；修改两端、增加或移除链路均为一个事务。至少保留一条链路，移除最后一条需要删除整个 bundle。删除逻辑拓扑不会删除物理端口或已审核的 SNMP 映射。

ECMP 使用每条链路的子网和 BFD；L3-LAG 使用 bundle 的子网和 BFD；L2-LAG 保留 STP 和聚合参数，不生成三层邻居。IPv4 `/31` 和 IPv6 `/127` 分别使用网络地址与下一地址，其它受支持子网使用首两个可用地址。服务端验证规范 CIDR、可用端点、重叠、接口身份、同侧交换机和端口占用。L2 不跨交换基础设施。旧模型没有独立 standby/backup 字段，cost 与 preference 按原数值保留，不据此猜测主备关系。

交换机 ASN、IPv4/IPv6 loopback 与 loopback 接口名用于派生核心 BGP 配置，不能套用成员 route-server peers。IPv4 loopback 才可作为 BGP router-id。资料不完整可以保存在库存中；生成有路由的交换机计划时，缺 ASN/router-id/接口名会拒绝生成，IPv6 还需要明确且不小于 1280 的 MTU。

## 整体审阅与端口用途

`GET /api/core/inventory` 提供核心用途的端口 `_etag`，涵盖物理端口、所属交换机及占用关系；不能用普通资源接口的 ETag 替代。保存时提交全部所选端口的 `portEtags`。修改和删除还需聚合对象的 `expectedRevision`，它包含当前依赖库存。冲突不会自动重读版本后覆盖用户审阅结果。

现有平面 `core-links` 保留为未归组链路并继续占用端口。归组必须明确提供 `adoptLinks:[{id,etag}]`，原有端点配对必须保留，采用与新 bundle 保存原子完成；不能自动猜测它属于哪一种 LAG/ECMP。

核心端口标记 `coreReserved:true`。解除 bundle 引用后该标记仍保留，需另行审阅并显式清除才能用于成员接入。更新端口省略标记会保留原值，null 或空串不能清除它。旧平面核心链路在首次加载时补齐预留用途、推进采样归属并原子持久化一次。旧库中尚未连接的 type=3 端口同样保留核心预留用途，不自动当作成员空闲口。

工作人员可读取，管理员和操作员浏览器可修改。`core:read` API token 必须显式绑定交换机；只有两端都在绑定范围内才能读取 bundle、观测及曲线。成员管理归属字段只是内部库存关系，不授予成员核心网络访问权限。

## 实际观测

物理状态来自已审核 SNMP 映射的 IF-MIB 采样，核对设备/接口身份、ifIndex、当前归属版本和时间。任何拓扑变化都会推进 bundle 与端口采样归属；删除再创建以及 A→B→A 都不会让旧采样重新成为当前数据。设备文件读取后还复核当前聚合版本及访问权限。

期望启用、物理状态、LACP、BGP 和 BFD 分开显示。两端 `ifOperStatus=up` 只证明端口报告 Up；当前没有独立的核心 LACP/BGP/BFD 观测适配，所请求协议明确显示 unsupported。缺数据、采集失败和过期各自保留，零条启用链路不能判为正常。

两侧容量与流量分别计算，不能将 A/B 相加作为总吞吐量。历史曲线只包含当前拓扑归属的样本，每分钟使用每个端口最后一次实际速率；缺少任何所选端口的速率时聚合保持 null，并返回覆盖数量和来源时间区间。不同端口的采样并不承诺同时发生，计数器重置、回绕和缺口也不会补成零。

`ixp-provision probe --kind core --id <bundle-id>` 读取控制面的上述观测，只检查物理端口：全部启用链路的两端均新鲜且身份匹配、报告 Up 才返回 OK；端口 Down 返回 CRITICAL，过期/缺失/未启用返回 UNKNOWN。输出明确说明没有评估 LACP、BFD、BGP 或转发。令牌需 `core:read` 及两侧交换机绑定。Nagios 配置只从真实 typed bundle 生成该检查，未归组线缆不会被推测成 bundle。

## 交换机核心配置计划

配置工具新增 `switch-core` 规则，选择一个明确的 `switchId`，使用完整双栈计划，不支持截取单 VLAN 或单地址族。沿用生成版本、预览/差异、校验、批准和 profile 绑定下载。端点、交换机 ASN/loopback/active 或接口属性改变后，旧审批文件不能继续作为当前配置下载。

生成的 JSON 含 loopback、ECMP 物理三层接口或 LAG 逻辑接口、成员物理端口、BFD/STP、双栈 BGP 邻居及同基础设施的受管 active loopback floodlist。L3-LAG 地址不会复制到每个成员端口；inactive 交换机会产生 shutdown 意图，且不会进入 floodlist。`active` 是管理意图，与 `monitor` 或采样健康独立；旧库存省略时兼容 true，省略更新不会激活原本 false 的记录。

`BuildSwitchPlan` 和 `ValidateSwitchPlan` 校验关联、地址、邻居完整性及声明边界。它是供目标适配器使用的类型化计划，**不是厂商原生配置，也不包含自动安装或回滚**。计划固定 `inventoryOnly:true`、`nativeValidated:false`、`requiresTargetAdapter:true`；当前校验等级是 structural。目标厂商翻译、原生检查、受控安装与撤回仍需继续实现及验收。

```sh
ixp-provision fetch --server https://ixp.example.net \
  --profile <profile-id> --revision <approved-version-id> \
  --kind switch-core --switch <switch-id> \
  --token-file /etc/moedove/provisioning.token --output /private/core-plan.json
```

下载核对 profile、版本、kind、实际 SHA-256，以及响应头和 JSON 中的物理 switch ID。文件以 `0600` 原子创建，拒绝覆盖已有路径。配置不会因下载而应用。

## 数据与接口

`State.CoreBundles` 保存有界类型化拓扑；完整备份保留稳定 link/endpoint ID、采样归属、审阅身份和已批准的加密配置版本。资源 JSON 导出包含顶层 `coreBundles`，但不携带采样授权；导入会生成新的归属版本。旧库转换先构建完整 core/CI/PI/VI 占用图，从成员连接转换中排除这些接口；关系不唯一、占用丢失或混入成员接入时，即使非 strict 模式也不能输出可导入数据。

| API | 用途 |
| --- | --- |
| `GET /api/core` | bundle、未归组链路及能力 |
| `GET /api/core/inventory?bundleId=` | 带核心审阅标识及占用原因的候选库存 |
| `POST /api/core/preview` | `{bundle,adoptLinks?}`；规范化 ID、派生地址及警告，不改变库存 |
| `POST /api/core/bundles` | `{bundle,portEtags,adoptLinks?}` 原子创建 |
| `GET/PUT /api/core/bundles/{id}` | 详情；更新需 `expectedRevision` 与 `portEtags` |
| `DELETE /api/core/bundles/{id}` | `{expectedRevision,reason}` 删除逻辑拓扑 |
| `GET .../{id}/observations` | 分层状态、端点、两侧容量和流量覆盖 |
| `GET .../{id}/series?side=a\|b&from=&to=` | 当前归属的有界聚合历史 |

本地验证使用合成库存、SNMP/API fixture 与隔离浏览器；没有操作真实交换机。
