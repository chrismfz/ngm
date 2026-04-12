# -------------------------------
# Project directories & binary
# -------------------------------
VERSION      ?= $(shell date +%Y.%m.%d)
BUILD_TIME   ?= $(shell date -u +"%Y-%m-%dT%H:%M:%S")
TAG          ?= v$(VERSION)

RPM_VERSION  := $(shell echo "$(VERSION)" | sed 's/-.*//; s/[^A-Za-z0-9._+~]/./g')
RPM_TS       := $(shell echo "$(BUILD_TIME)" | sed 's/.*T//; s/://g')
RPM_RELEASE  := 1.$(RPM_TS)
RPM_ARCH     := $(shell rpm --eval '%{_arch}')

BIN_DIR      := bin
MAIN_DIR     := cmd/ngm/
BINARY       := $(BIN_DIR)/ngm
PKGROOT      ?= build/pkgroot
RPMTOP       ?= packaging/rpm
SPECFILE     ?= $(RPMTOP)/SPECS/ngm.spec
CONFIG_DIR   := configs

# -------------------------------
# Remote Sync
# -------------------------------
REMOTE_USER ?= chris
REMOTE_HOST ?= repo.nixpal.com
REMOTE_PORT ?= 65535
REMOTE_DIR  ?= ~/packages/

RSYNC_FLAGS ?= -av --partial --inplace
SSH_CMD     ?= ssh -p $(REMOTE_PORT)

# -------------------------------
# Go build config
# -------------------------------
GOOS        ?= linux
GOARCH      ?= amd64
GOAMD64     ?= v1
GOAMD64     := $(strip $(GOAMD64))
CGO_ENABLED ?= 0

# -------------------------------
# Phony targets
# -------------------------------
.PHONY: help setup update build run clean clean-rpm distclean \
        stage-pkgroot stage-rpm rpm_prep_dirs rpm_spec_version rpm tar sync git

# -------------------------------
# Help
# -------------------------------
help: ## Show this help message
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' Makefile | sort | \
	awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	@echo ""

# -------------------------------
# Setup / Update
# -------------------------------
setup: ## First-time setup after git clone
	go mod tidy
	@echo "✅ Setup complete."

update: ## Update all dependencies
	@echo "🔍 Checking for module updates..."
	go list -m -u all | grep -E '\[|\.'
	go get -u ./...
	go mod tidy
	@echo "✅ Dependencies updated."

# -------------------------------
# Build
# -------------------------------
build: ## Build the binary into ./bin/
	@mkdir -p $(BIN_DIR)
	@echo "→ Building for $(GOOS)/$(GOARCH) (GOAMD64=$(GOAMD64), CGO_ENABLED=$(CGO_ENABLED))"
	env -u GOAMD64 \
	GOOS=$(GOOS) GOARCH=$(GOARCH) GOAMD64=$(GOAMD64) CGO_ENABLED=$(CGO_ENABLED) \
	go build -a \
		-tags netgo,osusergo \
		-ldflags "-X 'main.Version=$(VERSION)' -X 'main.BuildTime=$(BUILD_TIME)'" \
		-o $(BINARY) ./$(MAIN_DIR)
	@echo "✅ Built: $(BINARY)"

run: build ## Run the application
	@./$(BINARY)

# -------------------------------
# Stage (shared by rpm + tar)
# -------------------------------
stage-pkgroot: build
	@echo "→ Staging into $(PKGROOT)"
	@mkdir -p $(PKGROOT)/usr/bin
	@cp -f $(BINARY) $(PKGROOT)/usr/bin/ngm
	@mkdir -p $(PKGROOT)/etc/ngm
	@[ -f $(PKGROOT)/etc/ngm/ngm.conf ] || cp -f $(CONFIG_DIR)/ngm.conf $(PKGROOT)/etc/ngm/
	@mkdir -p $(PKGROOT)/usr/share/ngm/configs
	@rsync -a --delete "$(CONFIG_DIR)/" "$(PKGROOT)/usr/share/ngm/configs/"
	@mkdir -p $(PKGROOT)/usr/lib/systemd/system
	@cp -f $(CONFIG_DIR)/ngm.service $(PKGROOT)/usr/lib/systemd/system/ngm.service

stage-rpm: stage-pkgroot
	@echo "→ Staging RPM systemd unit"
	@mkdir -p $(PKGROOT)/usr/lib/systemd/system
	@cp -f $(CONFIG_DIR)/ngm.service $(PKGROOT)/usr/lib/systemd/system/ngm.service

# -------------------------------
# RPM
# -------------------------------
rpm_prep_dirs:
	@mkdir -p $(RPMTOP)/{BUILD,BUILDROOT,RPMS,SRPMS,SPECS,SOURCES}

rpm_spec_version:
	@sed -i 's/^Version:.*/Version:        $(RPM_VERSION)/' $(SPECFILE)
	@sed -i 's/^Release:.*/Release:        $(RPM_RELEASE)%{?dist}/' $(SPECFILE)

rpm: rpm_prep_dirs rpm_spec_version stage-rpm ## Build .rpm package
	@echo "→ Creating RPM package: ngm-$(RPM_VERSION)-$(RPM_RELEASE)"
	@rpmbuild \
	  --define "_topdir $(CURDIR)/$(RPMTOP)" \
	  --define "_binary_payload w9.gzdio" \
	  --define "debug_package %{nil}" \
	  --define "pkgroot $(CURDIR)/$(PKGROOT)" \
	  --define "projectroot $(CURDIR)" \
	  --buildroot "$(CURDIR)/$(RPMTOP)/BUILDROOT" \
	  --target $(RPM_ARCH) \
	  -bb $(SPECFILE)
	@echo "✅ RPM under: $(RPMTOP)/RPMS/$(RPM_ARCH)"

# -------------------------------
# Tar
# -------------------------------
TAR_OUTDIR := build/tar
TAR_NAME   := ngm-$(VERSION)-linux-$(GOARCH)
TAR_FILE   := $(TAR_OUTDIR)/$(TAR_NAME).tar.gz

tar: stage-pkgroot ## Build tar.gz archive (binary + configs + service)
	@mkdir -p $(TAR_OUTDIR)
	@tar -czf $(TAR_FILE) \
	  --transform 's|^|$(TAR_NAME)/|' \
	  -C $(PKGROOT) \
	  usr etc
	@echo "✅ Archive: $(TAR_FILE)"

# -------------------------------
# Sync (RPM + tar → remote repo)
# -------------------------------
sync: ## Sync latest .rpm and .tar.gz to remote repo
	@set -euo pipefail; \
	RPM_FILE="$$(ls -1t $(RPMTOP)/RPMS/*/ngm-*.rpm 2>/dev/null | head -n1)"; \
	TAR_FILE="$$(ls -1t $(TAR_OUTDIR)/ngm-*.tar.gz 2>/dev/null | head -n1)"; \
	[ -n "$$RPM_FILE" ] || { echo "❌ No .rpm found in $(RPMTOP)/RPMS — run: make rpm"; exit 1; }; \
	[ -n "$$TAR_FILE" ] || { echo "❌ No .tar.gz found in $(TAR_OUTDIR) — run: make tar"; exit 1; }; \
	echo "🌐 Syncing to $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)"; \
	$(SSH_CMD) $(REMOTE_USER)@$(REMOTE_HOST) "mkdir -p $(REMOTE_DIR)/rpm $(REMOTE_DIR)/tar"; \
	echo "→ Upload: $$RPM_FILE → $(REMOTE_DIR)/rpm/"; \
	rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" "$$RPM_FILE" "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/rpm/"; \
	echo "→ Upload: $$TAR_FILE → $(REMOTE_DIR)/tar/"; \
	rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" "$$TAR_FILE" "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/tar/"; \
	if [ -f checksums.txt ]; then \
	  rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" checksums.txt "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/"; \
	fi; \
	echo "✅ Remote sync complete."

# -------------------------------
# Clean
# -------------------------------
clean: ## Remove binary and pkgroot staging
	@rm -f bin/*
	@rm -rf build/pkgroot
	@echo "🧹 Cleaned: bin, build/pkgroot"

clean-rpm: ## Remove RPM build artifacts (keeps SPECS/)
	@rm -rf $(RPMTOP)/BUILD $(RPMTOP)/BUILDROOT
	@rm -rf $(RPMTOP)/RPMS $(RPMTOP)/SRPMS $(RPMTOP)/SOURCES
	@find $(RPMTOP) -type f -name '*.rpm' -delete 2>/dev/null || true
	@rm -rf $(TAR_OUTDIR)
	@echo "🧹 Cleaned: rpm artifacts + tar (kept SPECS/)"

distclean: clean clean-rpm ## Full clean
	@echo "🧨 Distclean done"

# -------------------------------
# Git helper
# -------------------------------
git: ## Commit + push with custom message
	@read -p "Enter commit message: " MSG && \
	git add . && \
	git commit -m "$$MSG" && \
	git push
