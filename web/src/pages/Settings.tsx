import LegacyLogoImport from "@/components/LegacyLogoImport";
import LegacyPatchImport from "@/components/LegacyPatchImport";
import LegacyDocstoreImport from "@/components/LegacyDocstoreImport";
import LegacyNoteImport from "@/components/LegacyNoteImport";
import DataTransfer from "@/components/DataTransfer";
import { lazy, Suspense, useEffect, useState, type FormEvent } from "react";
import { Check, Download, Save } from "lucide-react";
import { api, save } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@/components/ui/card";
import { ErrorNotice, Heading, Loading, message } from "@/components/shared";
const MailingListAdmin = lazy(() => import("@/components/MailingListAdmin"));
const LegacyMailingImport = lazy(() => import("@/components/LegacyMailingImport"));
const StorageCheck = lazy(() => import("@/components/StorageCheck"));
export default function Settings({
  role,
  onChange,
}: {
  role: string;
  onChange: () => void;
}) {
  const [data, setData] = useState<Record<string, unknown> | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [done, setDone] = useState(false),
    [ixfVersion, setIxfVersion] = useState("1.0");
  const [showMailingLists, setShowMailingLists] = useState(false);
  useEffect(() => {
    api<Record<string, unknown>>("/settings")
      .then(setData)
      .catch((e) => setError(message(e)));
  }, []);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setDone(false);
    try {
      await save(
        "/settings",
        { ...data, asn: Number(data?.asn), ixfId: Number(data?.ixfId || 0) },
        "PUT",
      );
      setDone(true);
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const update = (key: string, value: unknown) => {
    setDone(false);
    setData({ ...data, [key]: value });
  };
  const exportIxf = async () => {
    setBusy(true);
    setError("");
    try {
      const body = await api<unknown>(`/ixf/${ixfVersion}`),
        url = URL.createObjectURL(
          new Blob([JSON.stringify(body, null, 2)], {
            type: "application/json",
          }),
        ),
        a = document.createElement("a");
      a.href = url;
      a.download = `ixf-members-${ixfVersion}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="交换中心设置"
        description="维护交换中心的基础信息与控制台显示名称。"
      />
      <ErrorNotice message={error} />
      {!data ? (
        !error && <Loading />
      ) : (
        <Card className="max-w-3xl">
          <CardHeader>
            <CardTitle>基本信息</CardTitle>
            <CardDescription>这些信息用于识别你的交换网络。</CardDescription>
          </CardHeader>
          <CardContent>
            <form className="space-y-6" onSubmit={submit}>
              <div className="form-grid">
                {[
                  { key: "name", label: "交换中心名称", required: true },
                  {
                    key: "asn",
                    label: "自治系统号（ASN）",
                    type: "number",
                    required: true,
                    min: 1,
                    max: 4294967294,
                  },
                  { key: "website", label: "官方网站", type: "url" },
                  { key: "supportEmail", label: "支持邮箱", type: "email" },
                  { key: "shortName", label: "交换中心简称" },
                  {
                    key: "country",
                    label: "国家 / 地区代码",
                    placeholder: "CN",
                  },
                  {
                    key: "ixfId",
                    label: "IX-F 交换中心 ID",
                    type: "number",
                    min: 0,
                  },
                ].map((f) => (
                  <div key={f.key} className="field">
                    <label htmlFor={`ixp-${f.key}`}>{f.label}</label>
                    <Input
                      id={`ixp-${f.key}`}
                      type={f.type}
                      required={f.required}
                      min={f.min}
                      max={f.max}
                      placeholder={f.placeholder}
                      value={String(data[f.key] ?? "")}
                      disabled={role !== "admin"}
                      onChange={(e) => update(f.key, e.target.value)}
                    />
                  </div>
                ))}
              </div>
              <p className="text-xs leading-6 text-muted-foreground">
                显式交换网络分别使用自己的 IX-F ID
                与简称；兼容未绑定网络才使用此处的全局标识。是否能够公开导出由服务器核对实际网络资料。
              </p>
              <div className="rounded-lg border p-4">
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={Boolean(data.publishIxf)}
                    disabled={role !== "admin"}
                    onChange={(e) => update("publishIxf", e.target.checked)}
                  />
                  公开 IX-F 成员信息
                </label>
                <p className="mt-2 text-xs leading-6 text-muted-foreground">
                  启用后，成员与连接信息可通过 /api/public/ixf
                  公开获取。请确认这些信息适合公开分享。
                </p>
              </div>
              <div className="field">
                <label htmlFor="ixf-export-version">IX-F 导出格式</label>
                <select
                  id="ixf-export-version"
                  value={ixfVersion}
                  disabled={busy}
                  onChange={(e) => setIxfVersion(e.target.value)}
                  className="h-10 w-full rounded-md border bg-background px-3 text-sm"
                >
                  <option value="1.0">1.0（推荐）</option>
                  <option value="0.7">0.7（旧系统兼容）</option>
                  <option value="0.6">0.6（旧系统兼容）</option>
                </select>
                <p className="text-xs leading-6 text-muted-foreground">
                  导出使用已保存的数据。公开开启后，同一格式可从
                  <code className="break-all"> /api/public/ixf/{ixfVersion}</code>
                  获取；切换格式不会开启公开访问。
                </p>
              </div>
              <div className="flex flex-wrap items-center justify-end gap-4 border-t pt-5">
                {done && (
                  <span
                    role="status"
                    className="flex items-center gap-1 text-xs text-primary"
                  >
                    <Check className="size-4" />
                    已保存
                  </span>
                )}
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void exportIxf()}
                >
                  <Download />
                  导出 IX-F
                </Button>
                {role === "admin" && (
                  <Button disabled={busy}>
                    <Save />
                    {busy ? "正在保存…" : "保存设置"}
                  </Button>
                )}
              </div>
            </form>
          </CardContent>
        </Card>
      )}
      {role === "admin" && (
        <>
          <Suspense fallback={<Loading />}><StorageCheck /></Suspense>
          <Card className="mt-6 max-w-3xl min-w-0"><CardHeader><CardTitle>邮件列表</CardTitle><CardDescription>管理列表定义并导出用户明确选择的偏好；外部同步尚未确认。</CardDescription></CardHeader><CardContent>{showMailingLists ? <Suspense fallback={<Loading />}><MailingListAdmin /></Suspense> : <Button variant="outline" onClick={() => setShowMailingLists(true)}>管理邮件列表</Button>}</CardContent></Card>
          <DataTransfer onChange={onChange} />
          <LegacyPatchImport onChange={onChange} />
          <LegacyDocstoreImport onChange={onChange} />
          <LegacyLogoImport onChange={onChange} />
          <LegacyNoteImport onChange={onChange} />
          <Suspense fallback={<Loading />}><LegacyMailingImport /></Suspense>
        </>
      )}
    </>
  );
}
