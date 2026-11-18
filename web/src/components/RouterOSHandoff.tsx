import { useLayoutEffect, useRef, useState } from "react";
import { Download, FileCode2, LoaderCircle } from "lucide-react";
import { captureSessionContext } from "@/lib/api";
import { fetchRouterOSHandoff, type RouterOSHandoffBinding } from "@/lib/routeros-handoff";
import { Button } from "@/components/ui/button";
import { message } from "@/components/shared";

type Props = {
  binding: RouterOSHandoffBinding;
  contextKey: string;
  enabled: boolean;
  stillBound: () => boolean;
  onFailure: (message: string) => void;
};

export default function RouterOSHandoff({ binding, contextKey, enabled, stillBound, onFailure }: Props) {
  const [pending, setPending] = useState(false);
  const [saved, setSaved] = useState(false);
  const sequence = useRef(0);
  const active = useRef<AbortController | null>(null);
  // Layout cleanup runs before a new selection is displayed. A late response
  // cannot save a file or restore permissions from an old review/session.
  useLayoutEffect(() => {
    sequence.current++;
    active.current?.abort();
    active.current = null;
    setPending(false);
    setSaved(false);
    return () => { sequence.current++; active.current?.abort(); active.current = null; };
  }, [contextKey, enabled]);

  const download = async () => {
    if (!enabled || active.current || !stillBound()) return;
    const controller = new AbortController();
    active.current = controller;
    const request = sequence.current;
    const sessionCurrent = captureSessionContext();
    const current = () => !controller.signal.aborted && request === sequence.current && sessionCurrent() && stillBound();
    setPending(true);
    setSaved(false);
    try {
      const blob = await fetchRouterOSHandoff(binding, controller.signal, current);
      if (!blob || !current()) return;
      const url = URL.createObjectURL(blob);
      try {
        if (!current()) return;
        const link = document.createElement("a");
        link.href = url;
        link.download = `routeros-handoff-${binding.deploymentId}.json`;
        link.click();
        setSaved(true);
      } finally { URL.revokeObjectURL(url); }
    } catch (error) {
      if (current()) onFailure(message(error));
    } finally {
      if (request === sequence.current && active.current === controller) {
        active.current = null;
        setPending(false);
        if (!sessionCurrent()) {
          setSaved(false);
          onFailure("会话上下文已变化，请重新检查后下载规划文件。");
        }
      }
    }
  };

  return <section aria-label="RouterOS 规划交接" className="mb-4 min-w-0 space-y-3 rounded-lg border p-4">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h3 className="flex items-center gap-2 text-sm font-semibold"><FileCode2 className="size-4" />RouterOS 本机规划</h3>
        <p className="mt-2 text-xs leading-6 text-muted-foreground">下载当前草稿的 JSON 交接文件，与本机 review 一起生成新的计划文件。交接文件不含认证密钥，也不授予发布权限。</p>
      </div>
      <Button variant="outline" size="sm" aria-busy={pending} disabled={!enabled || pending} onClick={() => void download()}>
        {pending ? <LoaderCircle className="animate-spin" /> : <Download />}下载 RouterOS 规划文件
      </Button>
    </div>
    {!enabled && <p className="text-xs leading-6 text-muted-foreground">仅在当前草稿、输入和配置文件通过上方就绪检查后提供交接；状态未知时请重新检查。</p>}
    {saved && <p role="status" className="text-sm text-primary">已保存当前版本的规划交接文件。请在本机继续 review / plan，配置版本仍未获得发布授权。</p>}
    <div aria-label="本机 RouterOS 规划命令" className="mono max-w-full whitespace-pre-wrap break-all rounded-md bg-muted/40 p-3 text-xs leading-6">{`ixp-routeros plan --review /private/review.json --handoff '${(`/private/routeros-handoff-${binding.deploymentId}.json`).replace(/'/g, "'\"'\"'")}' --output /private/next-plan.json`}</div>
    <p className="text-xs leading-6 text-muted-foreground">本机工具会核对交接内容与设备 review，再由运维审阅计划及目标侧操作。</p>
  </section>;
}
