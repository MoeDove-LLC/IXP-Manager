import { useEffect, useRef, useState } from "react";
export type TrafficPoint = {
  observedAt: string;
  inOctets?: string;
  outOctets?: string;
  inBps: number | null;
  outBps: number | null;
  operStatus: string;
  counterReset?: boolean;
  counterWrapped?: boolean;
};
export const rate = (value: number | null | undefined) =>
  value == null
    ? "—"
    : value >= 1e9
      ? `${(value / 1e9).toFixed(2)} Gbps`
      : value >= 1e6
        ? `${(value / 1e6).toFixed(2)} Mbps`
        : value >= 1e3
          ? `${(value / 1e3).toFixed(2)} Kbps`
          : `${value.toFixed(0)} bps`;
export default function TrafficChart({
  points,
  from,
  to,
  timeZone,
  description,
  formatRate = rate,
  hint = "指向图表查看采样；缺失速率保留为空。",
}: {
  points: TrafficPoint[];
  from: number;
  to: number;
  timeZone?: string;
  description?: string;
  formatRate?: (value: number | null | undefined) => string;
  hint?: string;
}) {
  const [selected, setSelected] = useState<number | null>(null);
  const container = useRef<HTMLDivElement>(null),
    [width, setWidth] = useState(960);
  useEffect(() => {
    const node = container.current;
    if (!node) return;
    const observer = new ResizeObserver(([entry]) =>
      setWidth(Math.max(200, Math.round(entry.contentRect.width))),
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  const height = width < 500 ? 240 : 280,
    left = 82,
    right = 20,
    top = 20,
    bottom = 42,
    plotWidth = width - left - right,
    plotHeight = height - top - bottom;
  const max = Math.max(
    1,
    ...points.flatMap((p) => [p.inBps ?? 0, p.outBps ?? 0]),
  );
  const x = (at: string) =>
    left + ((Date.parse(at) - from) / Math.max(1, to - from)) * plotWidth;
  const y = (value: number) => top + plotHeight - (value / max) * plotHeight;
  const path = (key: "inBps" | "outBps") => {
    let previous = false;
    return points
      .map((p) => {
        const value = p[key];
        if (value == null) {
          previous = false;
          return "";
        }
        const part = `${previous ? "L" : "M"}${x(p.observedAt).toFixed(2)},${y(value).toFixed(2)}`;
        previous = true;
        return part;
      })
      .join(" ");
  };
  const chosen = selected == null ? null : points[selected];
  return (
    <div>
      <div className="relative" ref={container}>
        <svg
          className="w-full"
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={
            description ??
            `真实端口流量采样，${points.length} 个时间点；紫色为入口、绿色为出口，缺失速率不补零。`
          }
          onPointerMove={(e) => {
            if (!points.length) return;
            const rect = e.currentTarget.getBoundingClientRect(),
              at =
                from +
                ((((e.clientX - rect.left) / rect.width) * width - left) /
                  plotWidth) *
                  (to - from);
            let index = 0;
            for (let i = 1; i < points.length; i++)
              if (
                Math.abs(Date.parse(points[i].observedAt) - at) <
                Math.abs(Date.parse(points[index].observedAt) - at)
              )
                index = i;
            setSelected(index);
          }}
          onPointerLeave={() => setSelected(null)}
        >
          {[0, 0.25, 0.5, 0.75, 1].map((v) => (
            <g key={v}>
              <line
                x1={left}
                x2={width - right}
                y1={y(max * v)}
                y2={y(max * v)}
                stroke="#e9eaf0"
                strokeDasharray="4 4"
              />
              <text
                x={left - 12}
                y={y(max * v) + 4}
                textAnchor="end"
                fill="#858b9c"
                fontSize="11"
              >
                {formatRate(max * v)}
              </text>
            </g>
          ))}
          {(width < 500 ? [0, 0.5, 1] : [0, 0.25, 0.5, 0.75, 1]).map((v) => (
            <text
              key={v}
              x={left + v * plotWidth}
              y={height - 14}
              textAnchor={v === 0 ? "start" : v === 1 ? "end" : "middle"}
              fill="#858b9c"
              fontSize="11"
            >
              {new Date(from + v * (to - from)).toLocaleString("zh-CN", {
                ...(to - from > 36 * 3600000
                  ? {
                      ...(to - from > 330 * 24 * 3600000
                        ? { year: "numeric" }
                        : {}),
                      month: "2-digit",
                      day: "2-digit",
                    }
                  : { hour: "2-digit", minute: "2-digit" }),
                hour12: false,
                timeZone,
              })}
            </text>
          ))}
          <path
            d={path("inBps")}
            fill="none"
            stroke="#8066bd"
            strokeWidth="2"
          />
          <path
            d={path("outBps")}
            fill="none"
            stroke="#59a594"
            strokeWidth="2"
          />
          {points.filter((p) => p.inBps != null || p.outBps != null).length <
            30 &&
            points
              .filter((p) => p.inBps != null || p.outBps != null)
              .map((p, i) => (
                <g key={i}>
                  {p.inBps != null && (
                    <circle
                      cx={x(p.observedAt)}
                      cy={y(p.inBps)}
                      r="2.5"
                      fill="#8066bd"
                    />
                  )}
                  {p.outBps != null && (
                    <circle
                      cx={x(p.observedAt)}
                      cy={y(p.outBps)}
                      r="2.5"
                      fill="#59a594"
                    />
                  )}
                </g>
              ))}
          {chosen && (
            <line
              x1={x(chosen.observedAt)}
              x2={x(chosen.observedAt)}
              y1={top}
              y2={height - bottom}
              stroke="#a6a9b7"
              strokeDasharray="4 3"
            />
          )}
        </svg>
      </div>
      <div className="flex min-h-9 flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <div className="flex gap-5">
          <span className="flex items-center gap-2">
            <span className="h-0.5 w-4 bg-[#8066bd]" />
            入口
          </span>
          <span className="flex items-center gap-2">
            <span className="h-0.5 w-4 bg-[#59a594]" />
            出口
          </span>
        </div>
        {chosen ? (
          <span>
            {new Date(chosen.observedAt).toLocaleString("zh-CN", {
              hour12: false,
              timeZone,
            })}{" "}
            · 入口 {formatRate(chosen.inBps)} · 出口 {formatRate(chosen.outBps)}
            {chosen.counterReset ? " · 计数器重置" : ""}
            {chosen.counterWrapped ? " · 计数器回绕" : ""}
          </span>
        ) : (
          <span>{hint}</span>
        )}
      </div>
    </div>
  );
}
