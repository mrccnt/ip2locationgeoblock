#!/usr/bin/env bash
# Sends requests with spoofed X-Forwarded-For headers to the local test setup
# and checks the HTTP status codes. Start the setup first: make demo-up
#
# Environment:
#   BASE_URL  Traefik HTTP endpoint (default: http://localhost:8080)
set -uo pipefail

base="${BASE_URL:-http://localhost:8080}"
compose_file="$(cd "$(dirname "$0")" && pwd)/docker-compose.yml"
failed=0
last_code=000

# status <host> [x-forwarded-for]: prints the HTTP status code (000 = no response).
status() {
	local args=(-s -o /dev/null --max-time 5 -w '%{http_code}' -H "Host: $1")
	[ -n "${2:-}" ] && args+=(-H "X-Forwarded-For: $2")
	curl "${args[@]}" "${base}/" 2>/dev/null || true
}

# Waits until Traefik answers and the first router is loaded.
#   000 = Traefik not reachable yet, 404 = reachable, but router not loaded yet.
wait_ready() {
	local i
	for i in $(seq 1 30); do
		last_code="$(status block.localhost)"
		if [ "${last_code}" != "000" ] && [ "${last_code}" != "404" ]; then
			return 0
		fi
		sleep 1
	done
	return 1
}

diagnose() {
	echo
	if [ "${last_code}" = "000" ]; then
		echo "Traefik is not reachable at ${base} (no HTTP response)."
		echo "Possible causes: the stack is not running, Traefik exited at startup,"
		echo "port 8080 is in use or not forwarded (remote DOCKER_HOST?)."
		echo "Try BASE_URL=http://127.0.0.1:8080 if localhost resolves to IPv6."
	else
		echo "Traefik answers (HTTP ${last_code}), but the test router is not available."
		echo "Likely cause: the plugin or a label failed to load. Look for"
		echo "'Plugins are disabled because an error has occurred' in the log."
	fi
	if command -v docker >/dev/null 2>&1; then
		echo
		echo "--- docker compose ps -a ---"
		docker compose -f "${compose_file}" ps -a 2>&1 || true
		echo
		echo "--- traefik log (filtered: plugin/error/ip2location) ---"
		docker compose -f "${compose_file}" logs --tail 200 traefik 2>&1 |
			grep -iE 'plugin|error|ip2location|level=(err|fat)' | tail -n 30 || true
		echo
		if docker compose -f "${compose_file}" logs traefik 2>&1 | grep -q 'no valid configuration found'; then
			echo
			echo "Traefik found no configuration in /etc/traefik/traefik.yml. On the host,"
			echo "examples/traefik.yml is probably empty or a directory (Docker creates a"
			echo "directory when a bind-mount source is missing). Check with:"
			echo "  ls -ld examples/traefik.yml && wc -c examples/traefik.yml"
		fi
		echo
		echo "Full log: docker compose -f examples/docker-compose.yml logs traefik"
	fi
}

# check <description> <host> <x-forwarded-for or ""> <expected status>
check() {
	local desc="$1" host="$2" xff="$3" want="$4" got
	got="$(status "${host}" "${xff}")"
	if [ "${got}" = "${want}" ]; then
		printf 'PASS  %-58s %s\n' "${desc}" "${got}"
	else
		printf 'FAIL  %-58s got %s, want %s\n' "${desc}" "${got}" "${want}"
		failed=1
	fi
}

echo "Waiting for Traefik at ${base} (up to 30s) ..."
if ! wait_ready; then
	diagnose
	exit 1
fi

echo "block.localhost (block RU, CN)"
check "RU client is blocked"                 block.localhost 198.51.100.7 403
check "CN client is blocked"                 block.localhost 192.0.2.7    403
check "DE client is allowed"                 block.localhost 203.0.113.7  200
check "unknown client is allowed"            block.localhost 8.8.8.8      200
check "no header: docker gateway is allowed" block.localhost ""           200

echo "dach.localhost (allow DE only; middleware defined here)"
check "DE client is allowed"                 dach.localhost  203.0.113.7  200
check "RU client is blocked"                 dach.localhost  198.51.100.7 403
check "unknown client is blocked"            dach.localhost  8.8.8.8      403
check "no header: docker gateway is blocked" dach.localhost  ""           403

echo "dach2.localhost (shared middleware geo-dach@docker)"
check "DE client is allowed"                 dach2.localhost 203.0.113.7  200
check "RU client is blocked"                 dach2.localhost 198.51.100.7 403
check "unknown client is blocked"            dach2.localhost 8.8.8.8      403

echo "bypass.localhost (block CN, allowedIPs=192.0.2.10)"
check "allowed IP passes despite CN"         bypass.localhost 192.0.2.10  200
check "other CN client is blocked"           bypass.localhost 192.0.2.11  403
check "RU client is allowed (not listed)"    bypass.localhost 198.51.100.7 200

if [ "${failed}" -ne 0 ]; then
	echo "Some checks failed. See: make demo-logs"
	exit 1
fi
echo "All checks passed."
