# IP2Location GeoBlock - development helpers
#
# Usage:
#   make check
#   make report
#   make update-db
#   DB_DIR=/opt/traefik/geo make update-db

GOLANGCI_LINT ?= golangci-lint

# Output directory for all generated reports.
REPORT_DIR ?= reports

# Tools installed by "make install-tools". Override on the command line, e.g.
#   make install-tools SYFT_VERSION=v1.55.0
# golangci-lint and syft are installed from their official release binaries.
# The Go-based tools default to "latest"; pin them for reproducible builds.
GOLANGCI_LINT_VERSION   ?= v2.12.2
SYFT_VERSION            ?= v1.54.1
GO_JUNIT_REPORT_VERSION ?= latest
SCC_VERSION             ?= latest
GOCYCLO_VERSION         ?= latest
GOVULNCHECK_VERSION     ?= latest

# Directory the tools are installed into (lazily evaluated, needs Go).
GO_BIN = $(shell go env GOPATH)/bin

# Database download settings (IP2Location LITE DB1, IPv4 + IPv6, CSV).
DB_DIR  ?= geo
DB_ASSET ?= IP2LOCATION-LITE-DB1.IPV6.CSV.ZIP

.PHONY: help fmt vet lint test test-race check install-tools \
	report report-lint report-test report-sbom report-metrics report-vuln \
	clean-reports update-db

help:
	@echo "Targets:"
	@echo "  fmt             format the code (golangci-lint fmt)"
	@echo "  vet             run go vet"
	@echo "  lint            run golangci-lint"
	@echo "  test            run unit tests"
	@echo "  test-race       run unit tests with the race detector (needs cgo)"
	@echo "  check           vet + lint + test"
	@echo "  report          generate all reports into $(REPORT_DIR)/"
	@echo "                  (lint/checkstyle/sarif, tests/coverage/junit, sbom, metrics, vulnerabilities)"
	@echo "  report-*        run a single report: lint, test, sbom, metrics, vuln"
	@echo "  install-tools   install all lint/report tools (golangci-lint, syft, scc, gocyclo, ...)"
	@echo "  clean-reports   remove $(REPORT_DIR)/"
	@echo "  demo-up         generate a test database and start the local Docker test setup"
	@echo "  demo-test       send test requests to the local setup"
	@echo "  demo-logs       follow the Traefik log of the local setup"
	@echo "  demo-down       stop and remove the local setup"
	@echo "  update-db       download the latest IP2Location LITE database"
	@echo "                  (optional: DB_DIR, DB_ASSET)"

fmt:
	$(GOLANGCI_LINT) fmt ./...

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	go test -count=1 ./...

test-race:
	CGO_ENABLED=1 go test -race -count=1 ./...

check: vet lint test

# ---------------------------------------------------------------------------
# Reports
#
# "make report" never stops at findings (lint issues, vulnerabilities), so that
# all reports are always generated. Failing tests are the exception: the target
# still produces every report and then exits non-zero.
# Optional tools (syft, scc, gocyclo, govulncheck, go-junit-report) are skipped
# with a message when they are not installed; "make install-tools" installs them all.
# ---------------------------------------------------------------------------

report:
	@$(MAKE) --no-print-directory -k report-lint report-test report-sbom report-metrics report-vuln; \
	rc=$$?; \
	echo; \
	echo "Reports written to $(REPORT_DIR)/:"; \
	ls -1 "$(REPORT_DIR)"; \
	exit $$rc

# Static analysis: Checkstyle XML and SARIF from golangci-lint, plus go vet.
report-lint:
	@mkdir -p "$(REPORT_DIR)"
	@go vet ./... > "$(REPORT_DIR)/vet.txt" 2>&1 || echo "go vet reported findings, see $(REPORT_DIR)/vet.txt"
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run --issues-exit-code=0 \
			--output.checkstyle.path="$(REPORT_DIR)/checkstyle.xml" \
			--output.sarif.path="$(REPORT_DIR)/golangci-lint.sarif" \
			./... && echo "wrote $(REPORT_DIR)/checkstyle.xml and golangci-lint.sarif"; \
	else \
		echo "skipped lint reports: $(GOLANGCI_LINT) not installed"; \
	fi

# Tests: raw JSON, optional JUnit XML, coverage profile, text and HTML coverage.
report-test:
	@mkdir -p "$(REPORT_DIR)"
	@go test -count=1 -covermode=atomic -coverprofile="$(REPORT_DIR)/coverage.out" -json ./... > "$(REPORT_DIR)/test-report.json"; \
	rc=$$?; \
	if [ -s "$(REPORT_DIR)/coverage.out" ]; then \
		go tool cover -func="$(REPORT_DIR)/coverage.out" > "$(REPORT_DIR)/coverage.txt"; \
		go tool cover -html="$(REPORT_DIR)/coverage.out" -o "$(REPORT_DIR)/coverage.html"; \
		tail -n 1 "$(REPORT_DIR)/coverage.txt"; \
	fi; \
	if command -v go-junit-report >/dev/null 2>&1; then \
		go-junit-report -parser gojson -in "$(REPORT_DIR)/test-report.json" -out "$(REPORT_DIR)/junit.xml"; \
	else \
		echo "skipped junit.xml: go-junit-report not installed"; \
	fi; \
	if [ $$rc -ne 0 ]; then echo "tests FAILED, see $(REPORT_DIR)/test-report.json"; fi; \
	exit $$rc

# SBOM in CycloneDX and SPDX JSON via syft.
# The IP2Location database and generated reports are excluded from the scan.
# Note: the plugin has no third-party Go dependencies, so the SBOM is small.
report-sbom:
	@mkdir -p "$(REPORT_DIR)"
	@if command -v syft >/dev/null 2>&1; then \
		syft scan dir:. \
			--exclude "./$(REPORT_DIR)/**" \
			--exclude "./**/*.CSV" \
			-o cyclonedx-json="$(REPORT_DIR)/sbom.cdx.json" \
			-o spdx-json="$(REPORT_DIR)/sbom.spdx.json" \
		&& echo "wrote $(REPORT_DIR)/sbom.cdx.json and sbom.spdx.json"; \
	else \
		echo "skipped SBOM: syft not installed (https://github.com/anchore/syft)"; \
	fi

# Code metrics: lines of code (scc), cyclomatic complexity (gocyclo) and module list.
report-metrics:
	@mkdir -p "$(REPORT_DIR)"
	@go list -m -json all > "$(REPORT_DIR)/modules.json" 2>/dev/null || true
	@if command -v scc >/dev/null 2>&1; then \
		scc --exclude-dir "$(REPORT_DIR),.git" -f json -o "$(REPORT_DIR)/metrics-loc.json" . ; \
		scc --exclude-dir "$(REPORT_DIR),.git" -o "$(REPORT_DIR)/metrics-loc.txt" . ; \
		echo "wrote $(REPORT_DIR)/metrics-loc.json and metrics-loc.txt"; \
	else \
		echo "skipped LOC metrics: scc not installed"; \
	fi
	@if command -v gocyclo >/dev/null 2>&1; then \
		gocyclo -avg -over 0 . > "$(REPORT_DIR)/metrics-cyclomatic.txt" || true; \
		echo "wrote $(REPORT_DIR)/metrics-cyclomatic.txt"; \
	else \
		echo "skipped cyclomatic complexity: gocyclo not installed"; \
	fi

# Known vulnerabilities in the Go standard library and dependencies (needs network).
report-vuln:
	@mkdir -p "$(REPORT_DIR)"
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./... > "$(REPORT_DIR)/govulncheck.txt" 2>&1 \
			&& echo "govulncheck: no known vulnerabilities" \
			|| echo "govulncheck reported findings or failed, see $(REPORT_DIR)/govulncheck.txt"; \
	else \
		echo "skipped vulnerability scan: govulncheck not installed"; \
	fi

clean-reports:
	rm -rf "$(REPORT_DIR)"

# Installs every tool used by "make lint", "make check" and "make report" into
# $(GO_BIN) (make sure that directory is in your PATH):
#   golangci-lint, syft (release binaries via the official install scripts),
#   go-junit-report, scc, gocyclo, govulncheck (via go install).
# The install scripts are downloaded to a temporary file first, so that a failed
# download stops make instead of being silently ignored by a pipe.
# Note: this overwrites golangci-lint and syft binaries in $(GO_BIN).
install_script = tmp=$$(mktemp) && trap 'rm -f "$$tmp"' EXIT && curl -sSfL "$(1)" -o "$$tmp" && sh "$$tmp" -b "$(GO_BIN)" "$(2)"

install-tools:
	@command -v go >/dev/null 2>&1 || { echo "error: go is required to install the tools"; exit 1; }
	@curl --version >/dev/null 2>&1 || { echo "error: curl is required to install the tools"; exit 1; }
	@echo "Installing tools into $(GO_BIN) ..."
	@$(call install_script,https://golangci-lint.run/install.sh,$(GOLANGCI_LINT_VERSION))
	@$(call install_script,https://get.anchore.io/syft,$(SYFT_VERSION))
	go install github.com/jstemmer/go-junit-report/v2@$(GO_JUNIT_REPORT_VERSION)
	go install github.com/boyter/scc/v3@$(SCC_VERSION)
	go install github.com/fzipp/gocyclo/cmd/gocyclo@$(GOCYCLO_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@PATH="$(GO_BIN):$$PATH"; missing=0; \
	for t in golangci-lint syft go-junit-report scc gocyclo govulncheck; do \
		if command -v $$t >/dev/null 2>&1; then echo "ok: $$t"; else echo "missing: $$t"; missing=1; fi; \
	done; \
	exit $$missing

# ---------------------------------------------------------------------------
# Local Docker test setup (see examples/)
# ---------------------------------------------------------------------------

DEMO_COMPOSE := docker compose -f examples/docker-compose.yml

.PHONY: demo-up demo-test demo-logs demo-down

demo-up:
	@for f in examples/docker-compose.yml examples/traefik.yml ip2locationgeoblock.go go.mod .traefik.yml; do \
		if [ ! -f "$$f" ] || [ ! -s "$$f" ]; then \
			echo "error: $$f is missing, empty or not a regular file"; \
			echo "(Docker creates a directory for a missing bind-mount source; remove it and restore the file)"; \
			exit 1; \
		fi; \
	done
	bash examples/gen-testdb.sh
	$(DEMO_COMPOSE) up -d
	@echo "Traefik: http://localhost:8080  Dashboard: http://localhost:8081"
	@echo "Run 'make demo-test' once Traefik has started (the plugin loads in a few seconds)."

demo-test:
	bash examples/test.sh

demo-logs:
	$(DEMO_COMPOSE) logs -f traefik

demo-down:
	$(DEMO_COMPOSE) down

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

# Downloads the IP2Location LITE database ZIP and its MD5 checksum, verifies the
# checksum and extracts the files into $(DB_DIR).
# Note: IP2Location limits the number of downloads, and the database is updated
# monthly, so do not run this more often than needed.
# Traefik picks up the new file the next time the middleware is rebuilt
# (configuration change or restart).
update-db:
	@curl -s -o $(DB_ASSET) https://download.ip2location.com/lite/$(DB_ASSET)
	@curl -s -o $(DB_ASSET).md5 https://download.ip2location.com/lite/$(DB_ASSET).md5
	@md5sum --quiet -c $(DB_ASSET).md5
	@rm $(DB_ASSET).md5
	@mkdir -p $(DB_DIR)
	@unzip -q -o -d $(DB_DIR) $(DB_ASSET)
	@rm $(DB_ASSET)
