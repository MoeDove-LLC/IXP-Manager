#!/bin/sh
set -eu
# Trusted topology setup runs only inside bwrap's fresh network namespace.
# The Go test and original Quagga executable run after every capability drops.
/usr/sbin/ip link set lo up
for address in 192.0.2.1/32 192.0.2.2/32 192.0.2.3/32 192.0.2.112/32; do
 /usr/sbin/ip address add "$address" dev lo
done
for address in 2001:db8::1/128 2001:db8::2/128 2001:db8::3/128 2001:db8::112/128 fe80::1/128; do
 /usr/sbin/ip -6 address add "$address" dev lo nodad
done
exec /usr/bin/setpriv --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs /bin/sh /inside.sh
