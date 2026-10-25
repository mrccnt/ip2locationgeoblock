# IP2Location GeoBlock for Traefik

A Traefik middleware plugin that blocks or allows requests by country, using the IP2Location LITE CSV database.

## Description

`ip2locationgeoblock` looks up the client IP in the [IP2Location LITE](https://lite.ip2location.com) **DB1** database (IP address to country) and either denies requests from a list of countries (`block` mode) or only permits requests from a list of countries (`allow` mode).

Features:

- Configurable per middleware, and therefore per router/service, entirely via **Docker labels** (all options are plain strings).
- IPv4 and IPv6 support.
- Block-list (`mode=block`) and allow-list (`mode=allow`) modes.
- Always-allowed IPs/CIDRs (`allowedIPs`) and private networks (`allowPrivate`).
- Optional handling of unknown IPs (`blockUnknown`) and of `X-Forwarded-For` (`trustForwardedHeader`).
- Configurable status code for blocked requests (default `403`).
- In-memory binary search lookups; the database is loaded once per file and shared by all middleware instances.

The plugin reads the **CSV** version of the database (not `.BIN`), because Traefik plugins run in the Yaegi interpreter, which reliably supports only the Go standard library. The recommended file is `IP2LOCATION-LITE-DB1.IPV6.CSV`, which contains IPv4 (as `::ffff:x.x.x.x`) and IPv6 ranges. The plugin handles both automatically.

## Attribution and database license

This product includes IP2Location LITE data available from [https://lite.ip2location.com](https://lite.ip2location.com).

The IP2Location LITE database is **not part of this repository** and is provided by IP2Location under its own license terms (attribution required; please review the current terms on [lite.ip2location.com](https://lite.ip2location.com) before using or redistributing the data, especially for commercial use). If you display or document your service publicly, keep the attribution above in place.

The plugin source code is licensed under the Apache License 2.0 (see [LICENSE](LICENSE)). IP2Location is a trademark of its respective owner; this project is not affiliated with or endorsed by IP2Location.

## Configuration options

All options are strings, so they work with labels without list syntax or type conversion.

| Option | Default | Description |
|---|---|---|
| `databaseFilePath` | – (required) | Path to the IP2Location LITE DB1 CSV file inside the Traefik container. |
| `countries` | – | Comma-separated ISO 3166-1 alpha-2 codes, e.g. `RU,CN,KP`. |
| `mode` | `block` | `block`: deny listed countries. `allow`: permit only listed countries. |
| `allowedIPs` | – | Comma-separated IPs/CIDRs that always pass, e.g. `203.0.113.10,198.51.100.0/24`. |
| `allowPrivate` | `true` | Private, loopback and link-local addresses always pass. |
| `blockUnknown` | `false` | Deny IPs that have no country mapping. Note: in `allow` mode, unknown IPs pass unless this is `true`. |
| `trustForwardedHeader` | `false` | Take the client IP from `X-Forwarded-For`. Only enable behind a trusted proxy. |
| `statusCode` | `403` | HTTP status code returned for blocked requests. |

## Installation

### Static configuration (local plugin)

```yaml
# traefik.yml
experimental:
  localPlugins:
    ip2locationgeoblock:
      moduleName: github.com/mrccnt/ip2locationgeoblock
```

### docker-compose.yml (Traefik)

```yaml
services:
  traefik:
    image: traefik:v3
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./traefik.yml:/etc/traefik/traefik.yml:ro
      # The plugin source must be mounted at exactly this path:
      - ./ip2locationgeoblock:/plugins-local/src/github.com/mrccnt/ip2locationgeoblock:ro
      # Database
      - ./geo/IP2LOCATION-LITE-DB1.IPV6.CSV:/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV:ro
```

### Configuration via labels (per service)

```yaml
services:
  # Service A: block Russia, China and North Korea
  app-a:
    image: nginx
    labels:
      - traefik.enable=true
      - traefik.http.routers.app-a.rule=Host(`a.example.com`)
      - traefik.http.routers.app-a.middlewares=geoblock-a
      - traefik.http.middlewares.geoblock-a.plugin.ip2locationgeoblock.databaseFilePath=/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV
      - traefik.http.middlewares.geoblock-a.plugin.ip2locationgeoblock.countries=RU,CN,KP
      - traefik.http.middlewares.geoblock-a.plugin.ip2locationgeoblock.mode=block

  # Service B: only allow DE, AT and CH
  app-b:
    image: nginx
    labels:
      - traefik.enable=true
      - traefik.http.routers.app-b.rule=Host(`b.example.com`)
      - traefik.http.routers.app-b.middlewares=geoblock-b
      - traefik.http.middlewares.geoblock-b.plugin.ip2locationgeoblock.databaseFilePath=/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV
      - traefik.http.middlewares.geoblock-b.plugin.ip2locationgeoblock.countries=DE,AT,CH
      - traefik.http.middlewares.geoblock-b.plugin.ip2locationgeoblock.mode=allow
      - traefik.http.middlewares.geoblock-b.plugin.ip2locationgeoblock.allowedIPs=203.0.113.10,198.51.100.0/24
```

### Shared middleware

Define a middleware **once** and reference it from any number of routers with the `@docker` suffix. The definition can live on any container that Traefik reads. Note that Traefik only reads the labels of **enabled** containers: with `exposedByDefault=false` (as in the examples) the container needs `traefik.enable=true`, otherwise its labels, including middleware definitions, are silently ignored and routers that reference the middleware answer with 404. The middleware exists only as long as the container that defines it is running.

Definition (here on `app-a`, which uses it itself):

```yaml
services:
  app-a:
    image: nginx
    labels:
      - traefik.enable=true
      - traefik.http.routers.app-a.rule=Host(`a.example.com`)
      - traefik.http.routers.app-a.middlewares=geo-dach

      # Variant 1: only allow DE/AT/CH
      - traefik.http.middlewares.geo-dach.plugin.ip2locationgeoblock.databaseFilePath=/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV
      - traefik.http.middlewares.geo-dach.plugin.ip2locationgeoblock.mode=allow
      - traefik.http.middlewares.geo-dach.plugin.ip2locationgeoblock.countries=DE,AT,CH

      # Variant 2: block specific countries
      - traefik.http.middlewares.geo-block-ru-cn.plugin.ip2locationgeoblock.databaseFilePath=/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV
      - traefik.http.middlewares.geo-block-ru-cn.plugin.ip2locationgeoblock.mode=block
      - traefik.http.middlewares.geo-block-ru-cn.plugin.ip2locationgeoblock.countries=RU,CN,KP
```

Usage in other services:

```yaml
  app-b:
    image: nginx
    labels:
      - traefik.enable=true
      - traefik.http.routers.app-b.rule=Host(`b.example.com`)
      - traefik.http.routers.app-b.middlewares=geo-block-ru-cn@docker

  app-c:
    image: nginx
    labels:
      - traefik.enable=true
      - traefik.http.routers.app-c.rule=Host(`c.example.com`)
      # Multiple middlewares can be combined
      - traefik.http.routers.app-c.middlewares=geo-dach@docker,security-headers@docker
```

Middleware names must be unique. A name such as `geo-dach` must not be defined on two containers, otherwise they conflict.

## Notes

- **All options are strings** (`countries=RU,CN`, `allowPrivate=true`). This avoids problems with list syntax and type conversion in labels.
- **Memory and loading:** The database (about 20-40 MB in memory for DB1) is **not** loaded per request; each request is only a binary search in memory. Traefik instantiates a middleware once per router that references it, and rebuilds it on every dynamic configuration change (for example when a container starts or stops). To avoid loading the CSV each time, the plugin keeps **one shared copy per database file** (keyed by path, modification time and size). All middleware instances, including those with different country lists, share it. The log line `loaded ...` should therefore appear only once per file version.
- **Shared middleware:** A shared middleware does not save memory on its own (that is what the cache does), but it keeps the configuration in a single place.
- **Startup time:** Parsing the CSV in the Yaegi interpreter takes a few seconds the first time a given file version is loaded.
- **Behind a proxy/CDN (for example Cloudflare):** Only set `trustForwardedHeader=true` if Traefik is reachable exclusively through that proxy. Additionally configure `entryPoints.<name>.forwardedHeaders.trustedIPs`, otherwise a client can forge the header and bypass the blocking.
- **Database updates:** IP2Location updates the LITE database monthly. After replacing the file, the new version is picked up the next time the middleware is rebuilt (restart or any dynamic configuration change). No restart is needed beyond that. `make update-db` downloads the latest file (see below).
- **Plugin catalog:** To publish in the Traefik plugin catalog, the repository needs the GitHub topic `traefik-plugin` and a Git tag (for example `v0.1.0`). You then use `experimental.plugins` instead of `localPlugins`.
- **License:** The LITE database requires attribution to IP2Location, see [Attribution and database license](#attribution-and-database-license).

## Development

The [Makefile](Makefile) provides the following targets:

| Target | Description |
|---|---|
| `make lint` | Run [golangci-lint](https://golangci-lint.run) (v2) with the included `.golangci.yml`. |
| `make fmt` | Format the code. |
| `make vet` | Run `go vet`. |
| `make test` | Run the unit tests. |
| `make test-race` | Run the unit tests with the race detector (requires cgo). |
| `make check` | Run vet, lint and tests. |
| `make report` | Generate all reports into `reports/` (see below). |
| `make install-tools` | Install the optional Go-based report tools. |
| `make clean-reports` | Remove the `reports/` directory. |
| `make update-db` | Download the latest IP2Location LITE DB1 CSV. |

### Tests

The unit tests (`ip2locationgeoblock_test.go`) generate small IP2Location-style CSV files on the fly, so no real database is needed. They cover CSV parsing (IPv4, IPv4-mapped IPv6, IPv6, headers, invalid and unsorted rows), lookups, the shared database cache (including reloading a changed file and concurrent access), configuration validation, and request handling (block/allow mode, `allowedIPs`, private networks, unknown IPs, `X-Forwarded-For`, custom status codes).

```sh
make test
make test-race
```

### Reports

`make report` generates the following files in `reports/` (override with `REPORT_DIR=...`). It always runs every report, even if lint or vulnerability findings exist. If tests fail, all reports are still written and the target then exits with an error.

| File | Created by | Content |
|---|---|---|
| `checkstyle.xml` | golangci-lint | Lint findings in Checkstyle format (for Jenkins, SonarQube, etc.). |
| `golangci-lint.sarif` | golangci-lint | Lint findings as SARIF (for GitHub code scanning). |
| `vet.txt` | go vet | `go vet` output. |
| `test-report.json` | go test | Raw `go test -json` event stream. |
| `junit.xml` | go-junit-report | Test results in JUnit format. |
| `coverage.out`, `coverage.txt`, `coverage.html` | go tool cover | Coverage profile, per-function summary and HTML view. |
| `sbom.cdx.json`, `sbom.spdx.json` | [syft](https://github.com/anchore/syft) | Software Bill of Materials (CycloneDX and SPDX). The plugin has no third-party dependencies, so the SBOM is small. |
| `metrics-loc.json`, `metrics-loc.txt` | [scc](https://github.com/boyter/scc) | Lines of code, comments and complexity estimate. |
| `metrics-cyclomatic.txt` | [gocyclo](https://github.com/fzipp/gocyclo) | Cyclomatic complexity per function, with the average. |
| `modules.json` | go list | Go module information. |
| `govulncheck.txt` | [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | Known vulnerabilities (needs network access). |

Tools that are not installed are skipped with a message. `make install-tools` installs go-junit-report, scc, gocyclo and govulncheck via `go install`. golangci-lint and syft are installed separately (see their documentation). You can also run a single report, for example `make report-sbom` or `make report-metrics`.

### Local test setup

The `examples/` directory contains a Docker Compose setup that runs Traefik with the plugin loaded **from source** (only `ip2locationgeoblock.go`, `go.mod` and `.traefik.yml` are mounted), together with four `traefik/whoami` test services. It uses a tiny generated test database instead of the real one, so no download token is needed.

```sh
make demo-up     # generate the test database and start the containers
make demo-test   # send requests and verify the status codes
make demo-logs   # follow the Traefik log (shows the plugin output)
make demo-down   # stop and remove everything
```

The generated database (`examples/gen-testdb.sh`) maps the reserved documentation ranges to fake countries: `192.0.2.0/24` is `CN`, `198.51.100.0/24` is `RU` and `203.0.113.0/24` is `DE`. The test requests spoof these origins with the `X-Forwarded-For` header, which is why the setup enables `trustForwardedHeader` and, **for testing only**, `forwardedHeaders.insecure` on the entrypoint.

| Host | Middleware | Behavior |
|---|---|---|
| `block.localhost` | inline labels, `mode=block`, `countries=RU,CN` | RU and CN are denied, everything else passes. |
| `dach.localhost` | defines the shared middleware `geo-dach`: `mode=allow`, `countries=DE`, `blockUnknown=true` | Only DE passes. |
| `dach2.localhost` | reuses the shared middleware as `geo-dach@docker` | Same behavior as `dach.localhost`. |
| `bypass.localhost` | `mode=block`, `countries=CN`, `allowedIPs=192.0.2.10` | CN is denied, except the allowed IP. |

You can also try it manually, for example:

```sh
curl -i -H 'Host: block.localhost' -H 'X-Forwarded-For: 198.51.100.7' http://localhost:8080/   # 403
curl -i -H 'Host: block.localhost' -H 'X-Forwarded-For: 203.0.113.7'  http://localhost:8080/   # 200
```

The setup uses `allowPrivate=false`. Without it, requests from your own machine arrive from a private Docker gateway address and would always pass. To test with the real database, mount it at `/plugins/IP2LOCATION-LITE-DB1.IPV6.CSV` instead of the generated file. The module path in `examples/traefik.yml` and the mount paths in `examples/docker-compose.yml` must match the module name in `go.mod`.

### Updating the database

```sh
make update-db
# or into a specific directory:
DB_DIR=/opt/traefik/geo make update-db
```

The target downloads the LITE ZIP and its `.md5` checksum file from `download.ip2location.com`, verifies the checksum and extracts the contents (the CSV plus the license and readme files from IP2Location) into `geo/` (override with `DB_DIR`). It requires `curl`, `unzip` and `md5sum`. The database is updated monthly, so there is no need to run it more often. Use `DB_ASSET=IP2LOCATION-LITE-DB1.CSV.ZIP` for an IPv4-only database (check the exact file name on the IP2Location download page).

## License

Apache License 2.0, see [LICENSE](LICENSE). The IP2Location LITE data is subject to IP2Location's own terms, see [Attribution and database license](#attribution-and-database-license).
