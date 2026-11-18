GO ?= go
COMMANDS := ixp-manager ixp-agent ixp-netconf ixp-routeros ixp-migrate ixp-admin ixp-backup ixp-collector ixp-policy ixp-snmp ixp-sflow ixp-traffic-import ixp-sync ixp-provision ixp-openbgpd ixp-as112

.PHONY: build bundle test dev clean
build:
	cd web && npm ci && npm run build
	mkdir -p dist
	CGO_ENABLED=0 $(GO) build -trimpath -o dist/ $(addprefix ./cmd/,$(COMMANDS))

# Package an already built Linux workspace, including the matching web assets.
bundle:
	@test -s web/dist/index.html || { printf '%s\n' 'Run make build before make bundle.' >&2; exit 1; }
	@set -eu; \
		stage=$$(mktemp -d dist/.bundle.XXXXXXXX); \
		trap 'rm -rf "$$stage"' EXIT; \
		release="$$stage/moedove-ixp-manager"; \
		mkdir -p "$$release/web"; \
		for command in $(COMMANDS); do install -m 0755 "dist/$$command" "$$release/$$command"; done; \
		install -m 0644 LICENSE README.md "$$release/"; \
		cp -R web/dist/. "$$release/web/"; \
		tar -C "$$stage" -czf "$$stage/release.tar.gz" moedove-ixp-manager; \
		mv -f "$$stage/release.tar.gz" dist/moedove-ixp-manager.tar.gz
	@printf '%s\n' dist/moedove-ixp-manager.tar.gz

# Tests never activate routing daemons or touch production configuration.
test:
	$(GO) test -race ./...
	$(GO) vet ./...
	cd web && npm run build

dev:
	$(GO) run ./cmd/ixp-manager

clean:
	rm -rf dist web/dist
