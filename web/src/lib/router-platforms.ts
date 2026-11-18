export type RouterFamily = 4 | 6;

export function singleRouterFamily(router?: Readonly<Record<string, unknown>>): RouterFamily | null {
  const ipv4 = typeof router?.localIpv4 === "string" && router.localIpv4.trim() !== "";
  const ipv6 = typeof router?.localIpv6 === "string" && router.localIpv6.trim() !== "";
  if (ipv4 === ipv6) return null;
  return ipv4 ? 4 : 6;
}
