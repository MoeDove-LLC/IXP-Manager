export type AS112Bundle = {
  version: 1; kind: "as112-dns"; routerId: string; routerCreatedAt: string; asn: 112;
  service: "direct-delegation" | "dname" | "both"; families: number[];
  listenAddresses: string[]; advertisePrefixes: string[]; zones: string[];
  nodeAddresses: string[]; requiredLocalAddresses: string[];
  identity: { serverName: string; responsibleMailbox: string; facility: string; location: string; uniqueIP: string; loc: string };
  files: { path: string; content: string }[];
  healthPlan: { executed: false; requireLocalAddresses: true; transports: string[]; checks: { name: string; type: string; rcode: string; authoritative: true; recursionAvailable: false }[]; requirements: string[] };
};

// This guards the preview/download target and its claims. It is not DNS syntax
// validation and does not execute the bundled health plan.
export function readAS112Bundle(text: string, routerId: string): AS112Bundle {
  const value = JSON.parse(text) as AS112Bundle;
  const strings = (values: unknown): values is string[] => Array.isArray(values) && values.every(item => typeof item === "string");
  if (!value || !routerId || value.version !== 1 || value.kind !== "as112-dns" || value.routerId !== routerId || value.asn !== 112 || !["direct-delegation", "dname", "both"].includes(value.service) || !Array.isArray(value.families) || !value.families.length || value.families.some(family => family !== 4 && family !== 6) || new Set(value.families).size !== value.families.length || !strings(value.listenAddresses) || !value.listenAddresses.length || !strings(value.advertisePrefixes) || !value.advertisePrefixes.length || !strings(value.zones) || !value.zones.length || !value.identity || ["serverName", "responsibleMailbox", "facility", "location", "uniqueIP", "loc"].some(key => typeof value.identity[key as keyof AS112Bundle["identity"]] !== "string") || !Array.isArray(value.files) || !value.files.length || value.files.some(file => !file || typeof file.path !== "string" || typeof file.content !== "string" || !file.path || file.path.startsWith("/") || file.path.includes("\\") || file.path.includes("\0") || file.path.split("/").some(part => !part || part === "." || part === "..")) || value.healthPlan?.executed !== false || value.healthPlan.requireLocalAddresses !== true || !strings(value.healthPlan.transports) || value.healthPlan.transports.length !== 2 || !value.healthPlan.transports.includes("udp") || !value.healthPlan.transports.includes("tcp") || !strings(value.healthPlan.requirements) || !Array.isArray(value.healthPlan.checks) || !value.healthPlan.checks.length || value.healthPlan.checks.some(check => !check || typeof check.name !== "string" || typeof check.type !== "string" || typeof check.rcode !== "string" || check.authoritative !== true || check.recursionAvailable !== false))
    throw new Error("AS112 包的目标、服务边界或未执行的健康计划不匹配，已禁用后续操作。");
  const family = (address: string) => {
    if (/^(?:0|[1-9][0-9]{0,2})(?:\.(?:0|[1-9][0-9]{0,2})){3}$/.test(address) && address.split(".").every(part => Number(part) <= 255)) return 4;
    if (address.includes(":") && !address.includes("%") && !address.includes(".")) {
      try { if (new URL(`http://[${address}]/`).hostname === `[${address}]`) return 6; } catch { /* Not a canonical IPv6 literal. */ }
    }
    return 0;
  };
  if (typeof value.routerCreatedAt !== "string" || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value.routerCreatedAt) || !Number.isFinite(Date.parse(value.routerCreatedAt)) || !strings(value.nodeAddresses) || !strings(value.requiredLocalAddresses) || value.nodeAddresses.length !== value.families.length || value.families.some(wanted => value.nodeAddresses.filter(address => family(address) === wanted).length !== 1) || !value.nodeAddresses.includes(value.identity.uniqueIP) || value.nodeAddresses.some(address => value.listenAddresses.includes(address)))
    throw new Error("AS112 包的节点地址或必需本机地址不完整，已禁用后续操作。");
  const required = new Set([...value.listenAddresses, ...value.nodeAddresses]);
  if (value.requiredLocalAddresses.length !== required.size || new Set(value.requiredLocalAddresses).size !== required.size || value.requiredLocalAddresses.some(address => !required.has(address)))
    throw new Error("AS112 包的节点地址或必需本机地址不完整，已禁用后续操作。");
  return value;
}
