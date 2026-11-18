/* Offline fixture linked to the unmodified Quagga 1.2.4 libbgp/libzebra.
 * It calls the real config reader, route-map engine and attribute serializer.
 * No event loop, listener, route installation, peer session or publication.
 */
#include <zebra.h>
#include "command.h"
#include "filter.h"
#include "memory.h"
#include "privs.h"
#include "routemap.h"
#include "stream.h"
#include "thread.h"
#include "vrf.h"
#include "vty.h"
#include "bgpd/bgpd.h"
#include "bgpd/bgp_aspath.h"
#include "bgpd/bgp_attr.h"
#include "bgpd/bgp_route.h"

struct zebra_privs_t bgpd_privs;
struct thread_master *master;
static unsigned checked;

static void require(int ok, const char *reason) {
  if (!ok) { fprintf(stderr, "native Quagga fixture failed: %s\n", reason); exit(1); }
}

static struct peer *source_peer(struct bgp *bgp, const char *address) {
  union sockunion su;
  require(str2sockunion(address, &su) == 0, "source address parse");
  struct peer *peer = peer_lookup(bgp, &su);
  require(peer != NULL, "configured source peer missing");
  return peer;
}

static void attributes(struct attr *attr, struct attr_extra *extra,
                       const char *path, const char *next_hop, int ipv6) {
  memset(attr, 0, sizeof(*attr)); memset(extra, 0, sizeof(*extra));
  attr->extra = extra;
  attr->aspath = aspath_str2aspath(path);
  require(attr->aspath != NULL, "stock AS-path parser");
  attr->origin = BGP_ORIGIN_IGP;
  attr->flag = ATTR_FLAG_BIT(BGP_ATTR_ORIGIN) | ATTR_FLAG_BIT(BGP_ATTR_AS_PATH);
  if (ipv6) {
    require(inet_pton(AF_INET6, next_hop, &extra->mp_nexthop_global) == 1, "IPv6 next hop");
    extra->mp_nexthop_len = 16;
  } else {
    require(inet_pton(AF_INET, next_hop, &attr->nexthop) == 1, "IPv4 next hop");
    attr->flag |= ATTR_FLAG_BIT(BGP_ATTR_NEXT_HOP);
  }
}

static void policy_case(struct bgp *bgp, const char *label, const char *source,
                        const char *prefix, const char *path, const char *nh,
                        int expected, int import_context) {
  struct prefix p;
  require(str2prefix(prefix, &p) != 0, "prefix parse");
  struct peer *peer = source_peer(bgp, source);
  struct attr attr; struct attr_extra extra; struct bgp_info info;
  attributes(&attr, &extra, path, nh, p.family == AF_INET6);
  memset(&info, 0, sizeof(info)); info.peer = peer; info.attr = &attr;
  peer->rmap_type = import_context ? PEER_RMAP_TYPE_IMPORT : 0;
  struct route_map *map = route_map_lookup_by_name("mq_import");
  require(map != NULL, "import map missing");
  int allowed = route_map_apply(map, &p, RMAP_BGP, &info) != RMAP_DENYMATCH;
  peer->rmap_type = 0;
  require(allowed == expected, label);
  aspath_free(attr.aspath); checked++;
}

static void serialized_attributes(struct bgp *bgp) {
  struct peer *source = source_peer(bgp, "192.0.2.2");
  struct peer *receiver = source_peer(bgp, "192.0.2.3");
  struct attr attr; struct attr_extra extra;
  attributes(&attr, &extra, "64501 64511", "192.0.2.2", 0);
  struct stream *s = stream_new(4096);
  bgp_packet_attribute(bgp, receiver, s, &attr, NULL, AFI_IP, SAFI_UNICAST, source, NULL, NULL);
  const unsigned char *data = STREAM_DATA(s); size_t end = stream_get_endp(s), at = 0;
  int path_seen = 0, hop_seen = 0;
  while (at < end) {
    require(at + 3 <= end, "attribute header length");
    unsigned flags = data[at++], type = data[at++], len = data[at++];
    if (flags & BGP_ATTR_FLAG_EXTLEN) { require(at < end, "extended length"); len = (len << 8) | data[at++]; }
    require(at + len <= end, "attribute value length");
    if (type == BGP_ATTR_AS_PATH) {
      require(len == 6 && data[at] == 2 && data[at+1] == 2, "AS_PATH sequence preserved");
      require(((unsigned)data[at+2] << 8 | data[at+3]) == 64501 &&
              ((unsigned)data[at+4] << 8 | data[at+5]) == 64511, "route server ASN was prepended");
      path_seen = 1;
    }
    if (type == BGP_ATTR_NEXT_HOP) {
      require(len == 4 && memcmp(data + at, &attr.nexthop, 4) == 0, "NEXT_HOP was rewritten");
      hop_seen = 1;
    }
    at += len;
  }
  require(path_seen && hop_seen, "transparent attributes missing");
  stream_free(s); aspath_free(attr.aspath); checked++;
}

int main(int argc, char **argv) {
  require(argc == 2, "one generated config required");
  bgp_master_init(); master = bm->master;
  bgp_option_set(BGP_OPT_NO_LISTEN); bgp_option_set(BGP_OPT_NO_FIB);
  cmd_init(1); vty_init(master); memory_init(); vrf_init(); bgp_init();
  vty_read_config(argv[1], NULL);
  struct bgp *bgp = bgp_lookup_by_name("MOEDOVE_QUAGGA");
  require(bgp && bgp->name && bgp_option_check(BGP_OPT_NO_FIB) && bgp_option_check(BGP_OPT_NO_LISTEN), "dedicated non-FIB non-listening view");
  policy_case(bgp,"authorized v4 pair","192.0.2.2","198.51.100.0/24","64501 64511","192.0.2.2",1,1);
  policy_case(bgp,"wrong origin for exact prefix","192.0.2.2","198.51.100.0/24","64501 64512","192.0.2.2",0,1);
  policy_case(bgp,"wrong source with same neighbor ASN","192.0.2.3","198.51.100.0/24","64501 64511","192.0.2.3",0,1);
  policy_case(bgp,"second source own pair","192.0.2.3","198.51.100.0/24","64501 64512","192.0.2.3",1,1);
  policy_case(bgp,"crossed prefix-origin pair","192.0.2.2","203.0.113.0/24","64501 64511","192.0.2.2",0,1);
  policy_case(bgp,"second exact pair","192.0.2.2","203.0.113.0/24","64501 64512","192.0.2.2",1,1);
  policy_case(bgp,"wrong first ASN","192.0.2.2","198.51.100.0/24","64502 64511","192.0.2.2",0,1);
  policy_case(bgp,"more specific v4","192.0.2.2","198.51.100.0/25","64501 64511","192.0.2.2",0,1);
  policy_case(bgp,"third-party v4 next hop","192.0.2.2","198.51.100.0/24","64501 64511","192.0.2.3",0,1);
  policy_case(bgp,"zero v4 next hop","192.0.2.2","198.51.100.0/24","64501 64511","0.0.0.0",0,1);
  policy_case(bgp,"empty AS path","192.0.2.2","198.51.100.0/24","","192.0.2.2",0,1);
  policy_case(bgp,"AS set cannot satisfy sequence","192.0.2.2","198.51.100.0/24","64501 {64511,64512}","192.0.2.2",0,1);
  policy_case(bgp,"match peer is not an in-map rule","192.0.2.2","198.51.100.0/24","64501 64511","192.0.2.2",0,0);
  policy_case(bgp,"IPv6 four-byte ASN pair","2001:db8::2","2001:db8:100::/48","4200000001 4200000002","2001:db8::2",1,1);
  policy_case(bgp,"IPv6 first ASN mismatch","2001:db8::2","2001:db8:100::/48","4200000003 4200000002","2001:db8::2",0,1);
  policy_case(bgp,"more specific v6","2001:db8::2","2001:db8:100::/64","4200000001 4200000002","2001:db8::2",0,1);
  policy_case(bgp,"third-party v6 next hop","2001:db8::2","2001:db8:100::/48","4200000001 4200000002","2001:db8::3",0,1);
  policy_case(bgp,"zero global IPv6 next hop","2001:db8::2","2001:db8:100::/48","4200000001 4200000002","::",0,1);
  char path[1024] = "64501 ";
  for (int i=0; i<62; i++) strcat(path,"64502 ");
  strcat(path,"64511");
  policy_case(bgp,"64-AS path accepted","192.0.2.2","198.51.100.0/24",path,"192.0.2.2",1,1);
  strcat(path," 64511");
  policy_case(bgp,"65-AS path rejected","192.0.2.2","198.51.100.0/24",path,"192.0.2.2",0,1);
  serialized_attributes(bgp);
  printf("{\"stockQuagga\":\"1.2.4\",\"nativePolicyChecks\":%u,\"ASPathAndNextHopPreserved\":true,\"eventLoopRun\":false,\"listenersEnabled\":false,\"fibEnabled\":false}\n",checked);
  return 0;
}
