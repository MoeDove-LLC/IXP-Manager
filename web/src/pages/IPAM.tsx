import { lazy, Suspense, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Loading } from "@/components/shared";
const AddressPools = lazy(() => import("@/pages/AddressPools"));
const MACLearning = lazy(() => import("@/pages/MACLearning"));
const macRoute = () => {
  const [page, query = ""] = location.hash.slice(1).split("?");
  const params = new URLSearchParams(query);
  return {
    section:
      page === "ipam" && params.get("tab") === "mac" ? "mac" : "addresses",
    switchId: params.get("switchId") ?? "",
    candidateId: params.get("candidateId") ?? "",
  };
};
export default function IPAM({ role }: { role: string }) {
  const [route, setRoute] = useState(() => ({ ...macRoute(), navigation: 0 })),
    [section, setSection] = useState(() => macRoute().section);
  useEffect(() => {
    const changed = () => {
      const next = macRoute();
      setRoute((previous) => ({
        ...next,
        navigation: previous.navigation + 1,
      }));
      setSection(next.section);
    };
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, []);
  return (
    <>
      <div className="mb-5 flex flex-wrap gap-2" aria-label="地址管理功能">
        <Button
          variant={section === "addresses" ? "secondary" : "ghost"}
          aria-pressed={section === "addresses"}
          onClick={() => {
            setSection("addresses");
            location.hash = "ipam";
          }}
        >
          IP 地址分配
        </Button>
        {role !== "member" && (
          <Button
            variant={section === "mac" ? "secondary" : "ghost"}
            aria-pressed={section === "mac"}
            onClick={() => {
              setSection("mac");
              location.hash = "ipam?tab=mac";
            }}
          >
            MAC 登记与学习对照
          </Button>
        )}
      </div>
      <Suspense fallback={<Loading />}>
        {section === "mac" && role !== "member" ? (
          <MACLearning
            key={`${route.switchId}/${route.candidateId}/${route.navigation}`}
            role={role}
            requestedSwitchId={route.switchId}
            requestedCandidateId={route.candidateId}
          />
        ) : (
          <AddressPools role={role} />
        )}
      </Suspense>
    </>
  );
}
