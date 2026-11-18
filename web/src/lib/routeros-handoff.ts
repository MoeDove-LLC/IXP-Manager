import { APIError } from "@/lib/api";

export type RouterOSHandoffBinding = {
  deploymentId: string;
  routerId: string;
  reviewIdentifier: string;
};
const limit = 16 * 1024 * 1024;
const identifier = /^[A-Za-z0-9_-]{1,128}$/;
const digest = /^[a-f0-9]{64}$/;
const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

// Preserve the server's exact JSON bytes. The local CLI verifies the intent
// digest and policy; this transport checks the selected draft's identity only.
export async function fetchRouterOSHandoff(
  binding: RouterOSHandoffBinding,
  signal: AbortSignal,
  stillCurrent: () => boolean,
): Promise<Blob | null> {
  if (!identifier.test(binding.deploymentId) || !identifier.test(binding.routerId) || !digest.test(binding.reviewIdentifier))
    throw new Error("当前 RouterOS 版本标识无法核对，请重新检查。");
  if (signal.aborted || !stillCurrent()) return null;
  const response = await fetch(`/api/deployments/${encodeURIComponent(binding.deploymentId)}/routeros-handoff`, {
    credentials: "same-origin",
    redirect: "error",
    signal,
    headers: { Accept: "application/json", "X-IXP-Review-Identifier": binding.reviewIdentifier },
  });
  if (signal.aborted || !stillCurrent()) return null;
  if (!response.ok) {
    if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
    throw new APIError(
      response.status === 409
        ? "RouterOS 草稿或当前输入已变化，请重新检查并审阅当前版本。"
        : response.status === 403
          ? "当前浏览器会话无权下载 RouterOS 规划文件，请重新检查权限。"
          : `RouterOS 规划文件暂不可下载 (${response.status})，请重新检查或刷新版本列表。`,
      response.status,
    );
  }
  if (response.headers.get("X-IXP-Review-Identifier") !== binding.reviewIdentifier ||
      response.headers.get("Content-Type")?.split(";", 1)[0].trim().toLowerCase() !== "application/json" || !response.body)
    throw new Error("RouterOS 交接响应未绑定当前审阅版本，未保存文件。请重新检查。");
  const length = response.headers.get("Content-Length");
  if (length && Number(length) > limit) throw new Error("RouterOS 规划文件超过 16 MiB，未保存文件。");
  const reader = response.body.getReader();
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (signal.aborted || !stillCurrent()) return null;
      if (done) break;
      total += value.byteLength;
      if (total > limit) throw new Error("RouterOS 规划文件超过 16 MiB，未保存文件。");
      chunks.push(new Uint8Array(value));
    }
  } finally { await reader.cancel().catch(() => {}); }
  const blob = new Blob(chunks, { type: "application/json" });
  let value: unknown;
  try { value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(await blob.arrayBuffer())); }
  catch { throw new Error("RouterOS 规划文件格式无法核对，未保存文件。请重新检查。"); }
  if (!record(value) || value.version !== 1 || value.deploymentId !== binding.deploymentId ||
      value.routerId !== binding.routerId || value.reviewIdentifier !== binding.reviewIdentifier ||
      typeof value.intentSha256 !== "string" || !digest.test(value.intentSha256) ||
      !record(value.desired) || typeof value.desired.routerId !== "string" || !value.desired.routerId ||
      typeof value.desired.asn !== "number" || !Number.isSafeInteger(value.desired.asn) || value.desired.asn < 1 ||
      value.desired.asn > 4294967294 || !Array.isArray(value.desired.peers))
    throw new Error("RouterOS 规划文件的目标或版本不匹配，未保存文件。请重新检查。");
  return signal.aborted || !stillCurrent() ? null : blob;
}
