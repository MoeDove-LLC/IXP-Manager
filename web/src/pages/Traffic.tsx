import { lazy, Suspense, useState } from "react";
import { Button } from "@/components/ui/button";
import { Loading } from "@/components/shared";
const CounterTraffic = lazy(() => import("@/pages/CounterTraffic"));
const LegacyTraffic = lazy(() => import("@/pages/LegacyTraffic"));
export default function Traffic({ role }: { role: string }) {
  const [section, setSection] = useState("counter");
  return (
    <>
      <div className="mb-5 flex flex-wrap gap-2" aria-label="流量来源">
        <Button
          variant={section === "counter" ? "secondary" : "ghost"}
          aria-pressed={section === "counter"}
          onClick={() => setSection("counter")}
        >
          计数器归档
        </Button>
        <Button
          variant={section === "legacy" ? "secondary" : "ghost"}
          aria-pressed={section === "legacy"}
          onClick={() => setSection("legacy")}
        >
          旧统计（独立来源）
        </Button>
      </div>
      <Suspense fallback={<Loading />}>
        {section === "counter" ? (
          <CounterTraffic role={role} />
        ) : (
          <LegacyTraffic role={role} />
        )}
      </Suspense>
    </>
  );
}
