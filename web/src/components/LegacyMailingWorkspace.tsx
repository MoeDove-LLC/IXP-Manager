import { lazy, Suspense } from "react";
import { Loading } from "@/components/shared";
import LegacyMailingSelf from "@/components/LegacyMailingSelf";

const LegacyMailingAdmin = lazy(() => import("@/components/LegacyMailingAdmin"));
export default function LegacyMailingWorkspace({ role }: { role: string }) {
  return <div className="space-y-8 min-w-0"><LegacyMailingSelf />{role === "admin" && <Suspense fallback={<Loading />}><LegacyMailingAdmin /></Suspense>}</div>;
}
