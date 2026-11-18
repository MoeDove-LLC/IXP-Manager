export const RFC2317_MERGE_KIND = "dns-rfc2317-parent-merge";
export const RFC2317_MERGE_MAX_BYTES = 16 * 1024 * 1024;

export type RFC2317Metadata = {
  parentZone: string;
  childZone: string;
  previousParentSerial: number;
  parentSerial: number;
  childSerial: number;
  newChildZone: boolean;
  previousChildSerial?: number;
  addedRecords: number;
};

const digest = /^[a-f0-9]{64}$/;
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const isSerial = (value: unknown): value is number =>
  typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 0xffffffff;
const serialAdvances = (next: number, previous: number) => {
  const step = (next - previous) >>> 0;
  return next > 0 && step > 0 && step < 0x80000000;
};

export function isRFC2317Metadata(value: unknown): value is RFC2317Metadata {
  if (!record(value) || typeof value.parentZone !== "string" || typeof value.childZone !== "string") return false;
  const parent = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.in-addr\.arpa\.$/.exec(value.parentZone);
  const child = /^(\d{1,3})-(\d{1,3})\.(.+)$/.exec(value.childZone);
  return !!parent && parent.slice(1).every(part => String(Number(part)) === part && Number(part) <= 255) &&
    !!child && child[3] === value.parentZone && Number(child[1]) <= Number(child[2]) && Number(child[2]) <= 255 &&
    isSerial(value.previousParentSerial) && isSerial(value.parentSerial) && serialAdvances(value.parentSerial, value.previousParentSerial) &&
    isSerial(value.childSerial) && value.childSerial > 0 && typeof value.newChildZone === "boolean" &&
    (value.newChildZone ? value.previousChildSerial === undefined : isSerial(value.previousChildSerial) && serialAdvances(value.childSerial, value.previousChildSerial)) &&
    typeof value.addedRecords === "number" && Number.isSafeInteger(value.addedRecords) && value.addedRecords >= 0;
}

export function sameRFC2317Metadata(left: unknown, right: unknown): boolean {
  return isRFC2317Metadata(left) && isRFC2317Metadata(right) &&
    left.parentZone === right.parentZone && left.childZone === right.childZone &&
    left.previousParentSerial === right.previousParentSerial && left.parentSerial === right.parentSerial &&
    left.childSerial === right.childSerial && left.newChildZone === right.newChildZone &&
    left.previousChildSerial === right.previousChildSerial && left.addedRecords === right.addedRecords;
}

function exactFields(value: Record<string, unknown>, fields: string[]) {
  return Object.keys(value).length === fields.length && fields.every(field => Object.hasOwn(value, field));
}

export async function verifyRFC2317Merge(metadata: unknown, bytes: ArrayBuffer): Promise<void> {
  const invalid = new Error("RFC 2317 下载包的区名、序列号或完整文件校验不匹配，未保存文件。");
  if (!isRFC2317Metadata(metadata) || bytes.byteLength === 0 || bytes.byteLength > RFC2317_MERGE_MAX_BYTES) throw invalid;
  let value: unknown;
  try { value = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
  catch { throw invalid; }
  if (!record(value) || !exactFields(value, [
    "version", "kind", "review", "reviewSHA256", "parentSnapshot", "parentSnapshotSHA256",
    "previousParentSerial", "parentSerial", "childSerial", "addedRecords", "files", "parentSnapshotVerified",
    "approved", "activated", "liveParentVerified",
  ]) || value.kind !== RFC2317_MERGE_KIND || value.version !== 1 || value.parentSnapshotVerified !== true ||
    value.approved !== false || value.activated !== false || value.liveParentVerified !== false ||
    typeof value.parentSnapshot !== "string" || !value.parentSnapshot ||
    typeof value.parentSnapshotSHA256 !== "string" || !digest.test(value.parentSnapshotSHA256) ||
    typeof value.reviewSHA256 !== "string" || !digest.test(value.reviewSHA256) ||
    !Array.isArray(value.files) || value.files.length !== 2) throw invalid;

  const review = value.review;
  if (!record(review) || !exactFields(review, ["version", "kind", "input", "inputSHA256", "files", "approved", "activated", "parentIntegrated", "parentSnapshotVerified"]) ||
    review.version !== 1 || review.kind !== "dns-rfc2317-review" || review.approved !== false || review.activated !== false ||
    review.parentIntegrated !== false || review.parentSnapshotVerified !== false || !record(review.input) ||
    typeof review.inputSHA256 !== "string" || !digest.test(review.inputSHA256)) throw invalid;
  const input = review.input;
  if (!record(input.parent) || !record(input.dns) || input.parent.observedSerial !== value.previousParentSerial ||
    (input.newChildZone === true && input.previousChildSerial !== null) ||
    input.dns.serial !== value.childSerial || !sameRFC2317Metadata(metadata, {
      parentZone: input.parent.zone,
      childZone: input.childZone,
      previousParentSerial: value.previousParentSerial,
      parentSerial: value.parentSerial,
      childSerial: value.childSerial,
      newChildZone: input.newChildZone,
      previousChildSerial: input.previousChildSerial ?? undefined,
      addedRecords: value.addedRecords,
    })) throw invalid;

  const seen = new Set<string>();
  await Promise.all(value.files.map(async file => {
    if (!record(file) || !exactFields(file, ["path", "zone", "serial", "complete", "sha256", "content"]) ||
      (file.path !== "child.zone" && file.path !== "parent.zone") || seen.has(file.path) || file.complete !== true ||
      typeof file.sha256 !== "string" || !digest.test(file.sha256) || typeof file.content !== "string" || file.content.length === 0) throw invalid;
    seen.add(file.path);
    const child = file.path === "child.zone";
    if (file.zone !== (child ? metadata.childZone : metadata.parentZone) || file.serial !== (child ? metadata.childSerial : metadata.parentSerial)) throw invalid;
    const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(file.content));
    const actual = Array.from(new Uint8Array(hash), value => value.toString(16).padStart(2, "0")).join("");
    if (actual !== file.sha256) throw invalid;
  }));
}
