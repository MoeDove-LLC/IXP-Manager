import { useEffect, useRef, useState, type FormEvent } from "react";
import { useHashParameters } from "@/lib/hash-route";
import LegacyDocstore from "@/components/LegacyDocstore";
import {
  ChevronRight,
  Download,
  FileText,
  Folder,
  FolderPlus,
  Home,
  Pencil,
  RefreshCw,
  Search,
  Trash2,
  Upload,
} from "lucide-react";
import { api, save, downloadFile, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
type Scope = "staff" | "member" | "authenticated" | "public" | "member-admin" | "authenticated-admin";
type LegacyReadOnly = { legacyPatch?: boolean; legacyDocstore?: boolean; readOnly?: boolean };
const immutable = (item?: LegacyReadOnly | null) =>
  Boolean(item?.legacyPatch || item?.readOnly);
type Directory = LegacyReadOnly & {
  id: string;
  name: string;
  parentId?: string;
  memberId?: string;
  visibility: Scope;
  description?: string;
  createdAt: string;
};
type Document = LegacyReadOnly & {
  legacyKind?: "current" | "history" | "shared" | "customer";
  originalName?: string;
  sourceUploadedAt?: string;
  allocationAccessActive?: boolean;
  id: string;
  name: string;
  directoryId?: string;
  memberId?: string;
  visibility: Scope;
  description?: string;
  size: number;
  mediaType: string;
  checksum: string;
  version?: number;
  createdAt: string;
  updatedAt: string;
};
type Listing = {
  directories: Directory[];
  items: Document[];
  usedBytes?: number;
  quotaBytes?: number;
};
type Meta = {
  name: string;
  directoryId: string;
  memberId: string;
  visibility: Scope;
  description: string;
};
const scopes: Record<Scope, string> = {
  staff: "仅工作人员",
  member: "指定成员",
  authenticated: "所有登录用户",
  public: "公开访问",
  "member-admin": "指定成员的管理员",
  "authenticated-admin": "所有成员管理员",
};
const bytes = (n: number) =>
  n < 1024
    ? `${n} B`
    : n < 1024 * 1024
      ? `${(n / 1024).toFixed(1)} KB`
      : `${(n / 1024 / 1024).toFixed(1)} MB`;
export default function Documents({ role }: { role: string }) {
  const route = useHashParameters("documents");
  const targetId = route.get("id") ?? "";
  const targetDirectory = route.get("directoryId") ?? "";
  const publicView = role === "public",
    canWrite = role === "admin" || role === "operator",
    prefix = publicView ? "/public/documents" : "/documents";
  const [listing, setListing] = useState<Listing>({
      directories: [],
      items: [],
    }),
    [members, setMembers] = useState<Row[]>([]),
    [directory, setDirectory] = useState(""),
    [search, setSearch] = useState(""),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [mode, setMode] = useState<"directory" | "document" | null>(null),
    [editing, setEditing] = useState<Directory | Document | null>(null),
    [meta, setMeta] = useState<Meta>({
      name: "",
      directoryId: "",
      memberId: "",
      visibility: "staff",
      description: "",
    }),
    [file, setFile] = useState<File | null>(null),
    [formError, setFormError] = useState(""),
    [cascade, setCascade] = useState(false),
    [deleting, setDeleting] = useState<{
      item: Directory | Document;
      kind: string;
    } | null>(null);
  const [textEdit, setTextEdit] = useState<{
      document: Document;
      content: string;
      checksum: string;
    } | null>(null),
    [replacing, setReplacing] = useState<Document | null>(null),
    replaceInput = useRef<HTMLInputElement>(null);
  const load = async () => {
    setLoading(true);
    try {
      const data = await api<Listing>(prefix);
      setListing({
        ...data,
        directories: data.directories ?? [],
        items: data.items ?? [],
      });
      if (canWrite)
        setMembers((await api<{ items: Row[] }>("/resources/members")).items);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [role]);
  useEffect(() => {
    if (!targetId && !targetDirectory) return;
    const document = listing.items.find((item) => item.id === targetId);
    if (targetId && !document) return;
    setDirectory(document?.directoryId ?? targetDirectory);
    setSearch("");
  }, [targetId, targetDirectory, listing.items]);
  const parent = listing.directories.find((d) => d.id === directory);
  const focusedDocument = listing.items.find((d) => d.id === targetId);
  const breadcrumbs: Directory[] = [];
  let cursor = parent;
  const seen = new Set<string>();
  while (cursor && !seen.has(cursor.id)) {
    seen.add(cursor.id);
    breadcrumbs.unshift(cursor);
    cursor = listing.directories.find((d) => d.id === cursor?.parentId);
  }
  const dirs = listing.directories
    .filter(() => !targetId)
    .filter((d) =>
      search
        ? `${d.name} ${d.description ?? ""}`
            .toLowerCase()
            .includes(search.toLowerCase())
        : (d.parentId ?? "") === directory,
    );
  const docs = listing.items
    .filter((d) => !targetId || d.id === targetId)
    .filter((d) =>
      search
        ? `${d.name} ${d.description ?? ""}`
            .toLowerCase()
            .includes(search.toLowerCase())
        : (d.directoryId ?? "") === directory,
    );
  const navigate = (id: string) => {
    if (targetId || targetDirectory)
      location.hash = `documents${id ? `?directoryId=${encodeURIComponent(id)}` : ""}`;
    setDirectory(id);
    setSearch("");
    setError("");
  };
  const inherited = (id: string) =>
    listing.directories.find((d) => d.id === id);
  const setParent = (id: string) => {
    const p = inherited(id);
    setMeta({
      ...meta,
      directoryId: id,
      ...(p ? { memberId: p.memberId ?? "", visibility: p.visibility } : {}),
    });
  };
  const open = (
    kind: "directory" | "document",
    item?: Directory | Document,
  ) => {
    if (!canWrite || immutable(item) || (!item && immutable(parent))) {
      setError("遗留归档为只读，不能编辑或往其目录上传内容。");
      return;
    }
    setMode(kind);
    setEditing(item ?? null);
    const container = item
      ? kind === "directory"
        ? (item as Directory).parentId
        : (item as Document).directoryId
      : directory;
    const p = inherited(container ?? "");
    setMeta({
      name: item?.name ?? "",
      directoryId: container ?? "",
      memberId: item?.memberId ?? p?.memberId ?? "",
      visibility: item?.visibility ?? p?.visibility ?? "staff",
      description: item?.description ?? "",
    });
    setFile(null);
    setFormError("");
    setCascade(false);
  };
  const scopeChanged =
    mode === "directory" &&
    editing &&
    (editing.visibility !== meta.visibility ||
      (editing.memberId ?? "") !== meta.memberId);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    try {
      if (
        !canWrite ||
        immutable(editing) ||
        immutable(inherited(meta.directoryId))
      )
        throw new Error("遗留归档为只读，请选择其他可写目录。");
      if (meta.visibility === "member" && !meta.memberId)
        throw new Error("请指定可以访问的成员网络。");
      if (mode === "directory") {
        await save(
          `/directories${editing ? `/${editing.id}` : ""}`,
          {
            name: meta.name,
            parentId: meta.directoryId,
            memberId: meta.memberId,
            visibility: meta.visibility,
            description: meta.description,
          },
          editing ? "PUT" : "POST",
        );
      } else if (editing) {
        await save(`/documents/${editing.id}`, meta, "PUT");
      } else {
        if (!file) throw new Error("请选择要上传的文件。");
        if (file.size > 25 * 1024 * 1024)
          throw new Error("单个文件不能超过 25 MiB。");
        const data = new FormData();
        data.set("file", file);
        for (const [k, v] of Object.entries(meta)) data.set(k, v);
        await api("/documents", { method: "POST", body: data });
      }
      setMode(null);
      setError("");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!deleting || !canWrite || immutable(deleting.item)) return;
    setBusy(true);
    setFormError("");
    try {
      await api(`/${deleting.kind}/${deleting.item.id}`, { method: "DELETE" });
      setDeleting(null);
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const download = async (doc: Document) => {
    setError("");
    try {
      await downloadFile(`${prefix}/${doc.id}/download`, doc.name);
    } catch (e) {
      setError(message(e));
    }
  };
  const editText = async (doc: Document) => {
    setBusy(true);
    setError("");
    try {
      const data = await api<{ content: string; checksum: string }>(
        `/documents/${doc.id}/text`,
      );
      setTextEdit({ document: doc, ...data });
      setFormError("");
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const saveText = async () => {
    if (!textEdit || !canWrite || immutable(textEdit.document)) return;
    setBusy(true);
    setFormError("");
    try {
      await save(
        `/documents/${textEdit.document.id}/text`,
        { content: textEdit.content, checksum: textEdit.checksum },
        "PUT",
      );
      setTextEdit(null);
      await load();
    } catch (e) {
      if (e instanceof APIError && e.status === 409) {
        setFormError(
          "文件已被其他人修改。你的编辑仍保留，请复制内容后重新打开最新版本。",
        );
        await load();
      } else setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const replace = async (newFile?: File) => {
    if (!newFile || !replacing || !canWrite || immutable(replacing)) return;
    setBusy(true);
    setError("");
    try {
      if (newFile.size > 25 * 1024 * 1024)
        throw new Error("单个文件不能超过 25 MiB。");
      const data = new FormData();
      data.set("file", newFile);
      data.set("checksum", replacing.checksum);
      await api(`/documents/${replacing.id}/content`, {
        method: "POST",
        body: data,
      });
      await load();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
      setReplacing(null);
    }
  };
  const eligibleParents = listing.directories.filter((d) => {
    if (immutable(d)) return false;
    if (mode !== "directory" || !editing) return true;
    let p: Directory | undefined = d;
    const visited = new Set<string>();
    while (p && !visited.has(p.id)) {
      if (p.id === editing.id) return false;
      visited.add(p.id);
      p = listing.directories.find((v) => v.id === p?.parentId);
    }
    return true;
  });
  if (route.get("view") === "legacy" && ["admin", "operator", "viewer"].includes(role)) return <LegacyDocstore />;
  return (
    <>
      <Heading
        className="document-heading"
        title={publicView ? "公开文档" : "文档库"}
        description={
          publicView
            ? "查阅交换中心公开分享的资料。"
            : "集中管理运维资料与成员文件，明确每份文档的访问范围。"
        }
      >
        {["admin", "operator", "viewer"].includes(role) && <Button variant="outline" onClick={() => { location.hash = "documents?view=legacy"; }}>旧文档归档</Button>}
        {canWrite && !immutable(parent) && (
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => open("directory")}>
              <FolderPlus />
              新建目录
            </Button>
            <Button onClick={() => open("document")}>
              <Upload />
              上传文件
            </Button>
          </div>
        )}
      </Heading>
      {targetId && (
        <div
          className="notice mb-4 flex flex-wrap items-center justify-between gap-2"
          role="status"
        >
          <span>
            {loading
              ? "正在定位文档…"
              : listing.items.some((item) => item.id === targetId)
                ? "已定位搜索结果"
                : "此文档已删除，或你已失去访问权限。"}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate(directory)}
          >
            查看所在目录
          </Button>
        </div>
      )}
      {listing.quotaBytes != null && (
        <div className="mb-4 flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
          <span>
            文档空间 {bytes(listing.usedBytes ?? 0)} /{" "}
            {bytes(listing.quotaBytes)}
          </span>
          <div
            className="h-1.5 w-36 overflow-hidden rounded-full bg-muted"
            role="meter"
            aria-label="文档存储用量"
            aria-valuenow={listing.usedBytes ?? 0}
            aria-valuemin={0}
            aria-valuemax={listing.quotaBytes}
          >
            <div
              className="h-full bg-primary"
              style={{
                width: `${Math.min(100, ((listing.usedBytes ?? 0) / listing.quotaBytes) * 100)}%`,
              }}
            />
          </div>
        </div>
      )}
      <ErrorNotice message={error} />
      {immutable(parent) && (
        <div className="notice mt-4 text-sm leading-6">
          此目录属于遗留配线归档，只允许读取和下载。来源内容与访问资格由当前配线分配核对，不能在这里上传、移动或修改权限。
        </div>
      )}
      {focusedDocument && immutable(focusedDocument) && (
        <section
          aria-label="遗留文档说明"
          className="mt-4 min-w-0 rounded-lg border bg-card p-4"
        >
          <p className="break-words text-sm font-semibold">
            {focusedDocument.name}
          </p>
          <div className="mt-2 flex flex-wrap gap-2">
            <Badge variant="outline">遗留只读</Badge>
            <Badge variant="outline">
              {scopes[focusedDocument.visibility]}
            </Badge>
          </div>
          <p className="mt-2 text-xs leading-6 text-muted-foreground">
            {focusedDocument.legacyDocstore ? "旧文档只读快照。" : focusedDocument.legacyKind === "history"
              ? "旧分配快照附件"
              : "原当前端口附件"}
            {!focusedDocument.legacyDocstore && "，不代表新 LOA。"}
            {focusedDocument.sourceUploadedAt
              ? `原上传日期 / 时间：${focusedDocument.sourceUploadedAt}`
              : ""}
          </p>
        </section>
      )}
      <Card className="mt-4 overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b p-4">
          <nav
            aria-label="文档目录"
            className="flex flex-wrap items-center gap-1 text-sm"
          >
            <Button variant="ghost" size="sm" onClick={() => navigate("")}>
              <Home />
              文档库
            </Button>
            {breadcrumbs.map((d) => (
              <span key={d.id} className="flex items-center">
                <ChevronRight className="size-3 text-muted-foreground" />
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => navigate(d.id)}
                >
                  {d.name}
                </Button>
              </span>
            ))}
          </nav>
          <div className="flex items-center gap-2">
            <div className="relative">
              <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
              <Input
                className="pl-9"
                aria-label="搜索文档"
                placeholder="搜索所有可访问文档…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <Button
              variant="ghost"
              size="icon"
              aria-label="刷新文档"
              onClick={() => void load()}
              disabled={loading}
            >
              <RefreshCw />
            </Button>
          </div>
        </div>
        {parent?.description && !search && (
          <p className="border-b px-5 py-3 text-xs leading-6 text-muted-foreground">
            {parent.description}
          </p>
        )}
        {loading ? (
          <Loading />
        ) : !dirs.length && !docs.length ? (
          <Empty
            title={search ? "没有匹配的文档" : "目录中还没有文件"}
            description={
              search
                ? "请尝试其他名称或关键词。"
                : canWrite
                  ? "上传文件或创建目录，按访问范围与成员组织资料。"
                  : "此处暂无你可以访问的文档。"
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>名称</th>
                  <th>访问范围</th>
                  <th>大小</th>
                  <th>更新时间</th>
                  <th className="text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                {dirs.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <button
                        className="flex items-center gap-3 font-medium text-primary"
                        onClick={() => navigate(d.id)}
                      >
                        <Folder className="size-5" />
                        {d.name}
                      </button>
                      {immutable(d) && (
                        <Badge className="mt-2" variant="outline">
                          遗留只读目录
                        </Badge>
                      )}
                      {search && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {listing.directories.find((p) => p.id === d.parentId)
                            ?.name ?? "根目录"}
                        </p>
                      )}
                    </td>
                    <td>
                      <Badge
                        variant={
                          d.visibility === "public" ? "secondary" : "outline"
                        }
                      >
                        {scopes[d.visibility]}
                      </Badge>
                    </td>
                    <td className="text-muted-foreground">目录</td>
                    <td className="text-xs text-muted-foreground">
                      {time(d.createdAt)}
                    </td>
                    <td>
                      {canWrite && !immutable(d) && (
                        <div className="flex justify-end">
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label={`编辑目录 ${d.name}`}
                            onClick={() => open("directory", d)}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label={`删除目录 ${d.name}`}
                            onClick={() => {
                              setDeleting({ item: d, kind: "directories" });
                              setFormError("");
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
                {docs.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <div className="flex items-center gap-3">
                        <FileText className="size-5 text-muted-foreground" />
                        <div>
                          <p className="font-medium">
                            {d.name}
                            {d.version != null && (
                              <span className="ml-2 text-[10px] font-normal text-muted-foreground">
                                v{d.version}
                              </span>
                            )}
                          </p>
                          {immutable(d) && (
                            <div className="mt-2 space-y-1">
                              <Badge variant="outline">遗留只读</Badge>
                              <p className="text-xs leading-5 text-muted-foreground">
                                {d.legacyDocstore ? "旧文档只读快照" : d.legacyKind === "history"
                                  ? "旧分配快照附件"
                                  : "原当前端口附件"}{" "}
                                {!d.legacyDocstore && "· 不代表新 LOA"}
                              </p>
                              {d.sourceUploadedAt && (
                                <p className="text-xs text-muted-foreground">
                                  原上传日期 / 时间：{d.sourceUploadedAt}
                                </p>
                              )}
                            </div>
                          )}
                          {d.description && (
                            <p className="mt-1 max-w-xs truncate text-xs text-muted-foreground">
                              {d.description}
                            </p>
                          )}
                          {search && (
                            <p className="mt-1 text-xs text-muted-foreground">
                              {listing.directories.find(
                                (p) => p.id === d.directoryId,
                              )?.name ?? "根目录"}
                            </p>
                          )}
                        </div>
                      </div>
                    </td>
                    <td>
                      <Badge
                        variant={
                          d.visibility === "public" ? "secondary" : "outline"
                        }
                      >
                        {scopes[d.visibility]}
                      </Badge>
                    </td>
                    <td className="text-muted-foreground">{bytes(d.size)}</td>
                    <td className="text-xs text-muted-foreground">
                      {time(d.updatedAt)}
                    </td>
                    <td>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="icon"
                          variant="ghost"
                          aria-label={`下载 ${d.name}`}
                          onClick={() => void download(d)}
                        >
                          <Download />
                        </Button>
                        {!publicView &&
                          immutable(d) &&
                          /\.(txt|md)$/i.test(d.name) &&
                          d.size <= 1024 * 1024 && (
                            <Button
                              size="icon"
                              variant="ghost"
                              aria-label={`查看文本 ${d.name}`}
                              disabled={busy}
                              onClick={() => void editText(d)}
                            >
                              <FileText />
                            </Button>
                          )}
                        {canWrite && !immutable(d) && (
                          <>
                            <Button
                              size="icon"
                              variant="ghost"
                              aria-label={`编辑文件信息 ${d.name}`}
                              onClick={() => open("document", d)}
                            >
                              <Pencil />
                            </Button>
                            {/\.(txt|md)$/i.test(d.name) &&
                              d.size <= 1024 * 1024 && (
                                <Button
                                  size="icon"
                                  variant="ghost"
                                  aria-label={`编辑文本 ${d.name}`}
                                  disabled={busy}
                                  onClick={() => void editText(d)}
                                >
                                  <FileText />
                                </Button>
                              )}
                            <Button
                              size="icon"
                              variant="ghost"
                              aria-label={`替换内容 ${d.name}`}
                              disabled={busy}
                              onClick={() => {
                                setReplacing(d);
                                replaceInput.current?.click();
                              }}
                            >
                              <Upload />
                            </Button>
                            <Button
                              size="icon"
                              variant="ghost"
                              aria-label={`删除文件 ${d.name}`}
                              onClick={() => {
                                setDeleting({ item: d, kind: "documents" });
                                setFormError("");
                              }}
                            >
                              <Trash2 />
                            </Button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <input
        ref={replaceInput}
        type="file"
        className="hidden"
        aria-label="替换文件内容"
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = "";
          void replace(f);
        }}
      />
      <Dialog open={!!mode} onOpenChange={(v) => !busy && !v && setMode(null)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {mode === "directory"
              ? editing
                ? "编辑目录"
                : "新建目录"
              : editing
                ? "编辑文件信息"
                : "上传文件"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {mode === "directory"
              ? "目录的访问范围会同步到其中的文件与子目录。"
              : "文件放入目录后，继承该目录的访问范围。单个文件最大 25 MiB。"}
          </DialogDescription>
          <form onSubmit={submit} className="space-y-4">
            <ErrorNotice message={formError} />
            {mode === "document" && !editing && (
              <div className="field">
                <label htmlFor="doc-file">文件</label>
                <Input
                  id="doc-file"
                  type="file"
                  required
                  onChange={(e) => {
                    const f = e.target.files?.[0] ?? null;
                    setFile(f);
                    if (f && !meta.name) setMeta({ ...meta, name: f.name });
                  }}
                />
              </div>
            )}
            <div className="field">
              <label htmlFor="doc-name">名称</label>
              <Input
                id="doc-name"
                required
                value={meta.name}
                onChange={(e) => setMeta({ ...meta, name: e.target.value })}
              />
            </div>
            <div className="field">
              <label htmlFor="doc-parent">
                {mode === "directory" ? "上级目录" : "所在目录"}
              </label>
              <select
                id="doc-parent"
                value={meta.directoryId}
                onChange={(e) => setParent(e.target.value)}
              >
                <option value="">根目录</option>
                {eligibleParents.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-grid">
              <div className="field">
                <label htmlFor="doc-visibility">访问范围</label>
                <select
                  id="doc-visibility"
                  value={meta.visibility}
                  disabled={!!meta.directoryId}
                  onChange={(e) => {
                    const visibility = e.target.value as Scope;
                    setMeta({
                      ...meta,
                      visibility,
                      memberId:
                        visibility === "public" ||
                        visibility === "authenticated"
                          ? ""
                          : meta.memberId,
                    });
                  }}
                >
                  {Object.entries(scopes).filter(([v]) => !v.endsWith("-admin")).map(([v, label]) => (
                    <option key={v} value={v}>
                      {label}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="doc-member">
                  关联成员{meta.visibility === "member" ? " *" : ""}
                </label>
                <select
                  id="doc-member"
                  required={meta.visibility === "member"}
                  disabled={
                    !!meta.directoryId ||
                    meta.visibility === "public" ||
                    meta.visibility === "authenticated"
                  }
                  value={meta.memberId}
                  onChange={(e) =>
                    setMeta({ ...meta, memberId: e.target.value })
                  }
                >
                  <option value="">未指定</option>
                  {members.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </select>
              </div>
            </div>
            {meta.visibility === "public" && (
              <p className="notice">
                此资料无需登录即可访问，请确认内容适合公开分享。
              </p>
            )}
            {meta.directoryId && (
              <p className="text-xs text-muted-foreground">
                访问范围由所选目录决定。如需改变，请编辑上级目录或移至根目录。
              </p>
            )}
            <div className="field">
              <label htmlFor="doc-description">说明</label>
              <textarea
                id="doc-description"
                value={meta.description}
                onChange={(e) =>
                  setMeta({ ...meta, description: e.target.value })
                }
              />
            </div>
            {scopeChanged && (
              <label className="notice flex items-start gap-2">
                <input
                  className="mt-1"
                  type="checkbox"
                  required
                  checked={cascade}
                  onChange={(e) => setCascade(e.target.checked)}
                />
                我理解此修改会同步到目录下全部文件和子目录，并改变它们的访问范围。
              </label>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setMode(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>{busy ? "正在保存…" : "保存"}</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleting}
        onOpenChange={(v) => !busy && !v && setDeleting(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            确认删除{deleting?.kind === "directories" ? "目录" : "文件"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将删除「{deleting?.item.name}」。
            {deleting?.kind === "directories"
              ? "目录必须为空才可删除。"
              : "文件内容将被移除，此操作无法撤销。"}
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDeleting(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认删除
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!textEdit}
        onOpenChange={(v) => !busy && !v && setTextEdit(null)}
      >
        <DialogContent className="max-w-3xl">
          <DialogTitle className="text-lg font-semibold">
            {canWrite && !immutable(textEdit?.document)
              ? "编辑文本"
              : "查看文本"}
            ：{textEdit?.document.name}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            {canWrite && !immutable(textEdit?.document)
              ? "保存前会检查文件版本。如文件已被其他人修改，将保留你的编辑并提示冲突。"
              : "这是授权范围内的只读内容；遗留原文不会被改写，也不会恢复旧授权。"}
          </DialogDescription>
          <ErrorNotice message={formError} />
          <textarea
            className="min-h-80 font-mono text-sm"
            aria-label="文档文本内容"
            readOnly={!canWrite || immutable(textEdit?.document)}
            value={textEdit?.content ?? ""}
            onChange={(e) =>
              textEdit && setTextEdit({ ...textEdit, content: e.target.value })
            }
          />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setTextEdit(null)}
            >
              取消
            </Button>
            {canWrite && !immutable(textEdit?.document) && (
              <Button disabled={busy} onClick={() => void saveText()}>
                {busy ? "正在保存…" : "保存文本"}
              </Button>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
