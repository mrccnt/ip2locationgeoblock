#!/usr/bin/env bash
# Generates a tiny IP2Location-style CSV for local testing, so that no real
# database (and no download token) is needed. It uses the reserved
# documentation ranges (RFC 5737) as fake "countries":
#
#   192.0.2.0/24     -> CN
#   198.51.100.0/24  -> RU
#   203.0.113.0/24   -> DE
#
# The format matches IP2LOCATION-LITE-DB1.IPV6.CSV: IPv4 addresses are stored
# as IPv4-mapped IPv6 numbers (::ffff:a.b.c.d) in decimal notation.
set -euo pipefail

out_dir="$(cd "$(dirname "$0")" && pwd)/testdb"
out_file="${out_dir}/IP2LOCATION-LITE-DB1.IPV6.CSV"
mkdir -p "${out_dir}"

ip2num() {
	local a b c d
	IFS=. read -r a b c d <<<"$1"
	echo $((0xffff00000000 + (a << 24) + (b << 16) + (c << 8) + d))
}

add_range() { # from to country-code country-name
	printf '"%s","%s","%s","%s"\n' "$(ip2num "$1")" "$(ip2num "$2")" "$3" "$4"
}

{
	add_range 192.0.2.0 192.0.2.255 CN "China (test data)"
	add_range 198.51.100.0 198.51.100.255 RU "Russian Federation (test data)"
	add_range 203.0.113.0 203.0.113.255 DE "Germany (test data)"
} >"${out_file}"

echo "Wrote ${out_file}"
