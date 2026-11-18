# 旧版 IXP 按月总流量 API

Go 服务支持旧版 `GET /api/v4/statistics/overall-by-month` 与无 Cookie 的旧
`GET /api/v4/public/statistics/overall-by-month`。两条路径来自旧
`RouteServiceProvider` 加载 `routes/apiv4.php` 和 `routes/publicapi.php` 的实际前缀。
旧控制器读取总体 Grapher `ixp001` 的 `year` 数据，取最近六个 UTC 自然月内各
样点两方向最大值中的较大者。`bits` 是旧 graph 的默认类别。原系统的 MRTG 和
RRD 驱动在返回 `data()` 时已把 bits 类别的速率换成 bits/s。[旧 Grapher API 说明](https://docs.ixpmanager.org/latest/grapher/api/)

## 从旧实例取得审阅快照

使用有权限的旧实例，将总体 graph 的年度 bits 日志保存为本地数据。旧 URL 选项
为 `/grapher/ixp?id=1&period=year&category=bits&protocol=all&type=log`。
它等价于取得 `Grapher::ixp()->setPeriod(Graph::PERIOD_YEAR)->data()` 的五列值。
旧版月最大值 API 自己会为没有证据的月预填零，不适合作为新快照的来源。
只使用旧实例明确表示整个 IXP 的 `ixp001`，不选同名成员、交换机、机房、
基础设施或 VLAN graph。[旧总体 graph API](https://docs.ixpmanager.org/grapher/api/)

快照为一个 JSON 对象。下面是合成示例。

```json
{
  "version": 1,
  "source": "ixp001",
  "period": "year",
  "unit": "bits/s",
  "timezone": "UTC",
  "capturedAt": "2024-02-29T12:00:00Z",
  "samples": [
    [1706745600, 1250000000, 2000000000, 2500000000, 3000000000],
    [1709193600, null, null, null, null]
  ]
}
```

每一行按固定顺序填写 `[Unix秒，入向平均速率，出向平均速率，入向最大速率，
出向最大速率]`。时间戳为互不重复的正整数，且不晚于 `capturedAt`。四个速率
使用原来的非负整数 bits/s，未知值用 `null`；请从快照中移除超过采样时刻的未
完成桶。导入要求 UTC 月边界，与旧实例 `APP_TIMEZONE` 默认配置和旧 API 的 `Z`
响应一致。文件上限为 16 MiB，每份快照最多 20,000 条样点。

旧 RRD 驱动曾将 `NaN` 转为 `0`，旧 MRTG 日志读取器也可用 `0` 填充缺少的最大
值列。依原输出不能判断的零值应标为 `null`。只有能核对为已观测的零值才保留
数字 `0`。原始数据已是 bits/s 时直接保留，不再乘八。双方向都未知的样点不会
决定任何月份的最大值，平均值也不代替未知的峰值。

审阅快照后，按文件实际字节生成 SHA-256，保存在服务器本地路径。

```sh
sha256sum /srv/ixp/private/overall-traffic.json

ixp-manager \
  --legacy-overall-traffic /srv/ixp/private/overall-traffic.json \
  --legacy-overall-traffic-sha256 REVIEWED_FILE_SHA256
```

快照在启动时校验加载。刷新时保存新捕获的文件，替换配置中的路径或摘要并重启
服务。路径、摘要或内容有误时，显式配置的快照加载失败。请求参数不能选择
新的来源、时间范围或成员。

## 权限与返回值

默认只允许工作人员访问 `/api/v4/statistics/overall-by-month`。令牌调用还需
`resources:read`。启用 `--publish-legacy-overall-traffic` 后，两条旧路径都可匿名读取这份
快照的月汇总。公开别名默认返回 `404`；工作人员请求没有配置的快照时收到
`503`。响应设为 `no-store`，仅包含 `start`、`startTs`、`end`、`endTs`、`max`
五个旧字段。前后时间是真正的 UTC 月边界，`end` 包含该月最后一秒，`max` 是
JSON 整数。已观测为零的月份返回 `max:0`，没有已知最大值的月份省略；窗口内
无可用月份时返回 `[]`。

每一个月份的 `max` 来自该已审阅总体快照中的入向或出向样点。服务不拼接端口
峰值、其它报表或 sFlow 汇总，不对缺少的样点外推，也不公开原始样点、路径、
库存或租户标识。
