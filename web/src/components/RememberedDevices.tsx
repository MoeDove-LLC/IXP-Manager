import { useEffect, useRef, useState } from "react";
import { Laptop, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";

type RememberedDevice = {
  id: string;
  createdAt: string;
  lastUsedAt: string;
  expiresAt: string;
  ip: string;
  userAgent: string;
  current: boolean;
};
type DeviceList = { items: RememberedDevice[] };
const endpoint = "/account/remembered-devices";

export default function RememberedDevices({ deviceName, onSignedOut }: {
  deviceName: (agent: string) => string;
  onSignedOut: () => void;
}) {
  const [data, setData] = useState<DeviceList | null>(null);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [selected, setSelected] = useState<RememberedDevice | "all" | null>(null);
  const generation = useRef(0);

  const load = async () => {
    const request = ++generation.current;
    setBusy(true);
    setReady(false);
    setError("");
    try {
      const current = await api<DeviceList>(endpoint);
      if (request !== generation.current) return;
      setData(current);
      setReady(true);
    } catch (error) {
      if (request === generation.current) setError(message(error));
    } finally {
      if (request === generation.current) setBusy(false);
    }
  };
  useEffect(() => {
    void load();
    return () => { generation.current++; };
  }, []);

  const revoke = async () => {
    if (!selected || busy || !ready) return;
    const request = ++generation.current;
    setBusy(true);
    setReady(false);
    setDone("");
    setError("");
    const path = selected === "all" ? endpoint : `${endpoint}/${encodeURIComponent(selected.id)}`;
    try {
      const result = await api<{ ok: boolean; signedOut: boolean }>(path, { method: "DELETE" });
      if (request !== generation.current) return;
      if (result.ok !== true || typeof result.signedOut !== "boolean") throw new Error("撤销结果无法确认。");
      if (result.signedOut) {
        onSignedOut();
        return;
      }
      setSelected(null);
      setDone(selected === "all" ? "已撤销所有记住的设备。" : "已撤销所选设备。");
      await load();
    } catch (error) {
      if (request === generation.current) setError(`${message(error)} 请重新读取设备列表，核对撤销结果。`);
    } finally {
      if (request === generation.current) setBusy(false);
    }
  };
  const open = (device: RememberedDevice | "all") => {
    setDone("");
    setError("");
    setSelected(device);
  };

  return <Card className="min-w-0" role="region" aria-label="已记住的设备">
    <CardHeader className="flex-row flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <CardTitle className="flex items-center gap-2"><Laptop className="size-5 text-primary" />已记住的设备</CardTitle>
        <CardDescription className="mt-2">这些浏览器可在期限内恢复登录。撤销设备也会结束通过它建立的会话。</CardDescription>
      </div>
      <Button variant="outline" disabled={busy || !ready || !data?.items.length} onClick={() => open("all")}>撤销全部</Button>
    </CardHeader>
    <CardContent className="space-y-4 min-w-0">
      {done && <p role="status" className="notice text-sm">{done}</p>}
      {!selected && <ErrorNotice message={error} />}
      {!ready && !busy && !selected && <Button variant="outline" onClick={() => void load()}><RefreshCw />重新读取设备</Button>}
      {!data && busy && <Loading />}
      {ready && !data?.items.length && <Empty title="没有已记住的设备" description="登录时选择记住这台设备，即可在这里管理。" />}
      {!!data?.items.length && <ul className="space-y-3">
        {data.items.map(device => <li key={device.id} className="rounded-lg border p-4 flex flex-wrap items-start justify-between gap-3 min-w-0">
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2"><span className="text-sm font-medium">{deviceName(device.userAgent)}</span>{device.current && <Badge>当前设备</Badge>}</div>
            <div className="grid gap-1 text-xs text-muted-foreground sm:grid-cols-2 sm:gap-x-6">
              <div>登记于 {time(device.createdAt)}</div>
              <div>最近使用 {time(device.lastUsedAt)}</div>
              <div>到期于 {time(device.expiresAt)}</div>
              {device.ip && <div className="break-all">IP {device.ip}</div>}
            </div>
          </div>
          <Button variant="ghost" size="sm" disabled={busy || !ready} onClick={() => open(device)}>撤销设备</Button>
        </li>)}
      </ul>}
    </CardContent>
    <Dialog open={selected !== null} onOpenChange={open => { if (!open && !busy) setSelected(null); }}>
      <DialogContent>
        <DialogTitle>{selected === "all" ? "撤销所有记住的设备" : "撤销已记住的设备"}</DialogTitle>
        <DialogDescription>
          {selected === "all" ? "这些浏览器下次需要重新登录。" : selected && `${deviceName(selected.userAgent)} 下次需要重新登录。`}
          {(selected === "all" && data?.items.some(device => device.current) || selected !== "all" && selected?.current) && " 当前会话也将退出。"}
        </DialogDescription>
        <ErrorNotice message={error} />
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" disabled={busy} onClick={() => setSelected(null)}>返回</Button>
          <Button variant="destructive" disabled={busy || !ready} onClick={() => void revoke()}>确认撤销</Button>
        </div>
      </DialogContent>
    </Dialog>
  </Card>;
}
