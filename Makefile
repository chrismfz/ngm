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


BIN_DIR := bin
MAIN_DIR := cmd/ngm/
BINARY := $(BIN_DIR)/ngm
PKGROOT      ?= build/pkgroot
RPMTOP       ?= packaging/rpm
SPECFILE     ?= $(RPMTOP)/SPECS/ngm.spec
ARCH         ?= x86_64


override ARCH    := amd64
override VERSION := $(shell date +%Y.%m.%d-%H%M%S)
override PKGROOT := build/pkgroot
override OUTDIR  := build/deb
BIN := bin/ngm
CONFIG_DIR := configs
DEB_SRC := packaging/debian/DEBIAN


# --- Remote Sync ---
REMOTE_USER ?= chris
REMOTE_HOST ?= repo.nixpal.com
REMOTE_PORT ?= 65535
REMOTE_DIR  ?= ~/packages/
SYNC_ON_RELEASE ?= 1

RSYNC_FLAGS ?= -av --partial --inplace
SSH_CMD     ?= ssh -p $(REMOTE_PORT)


# -------------------------------
# Go build target config (CPU/OS)
# -------------------------------
GOOS    ?= linux
GOARCH  ?= amd64
GOAMD64 ?= v1
GOAMD64 := $(strip $(GOAMD64))
CGO_ENABLED ?= 0
# v1=vintage, v2, v3, v4

# -------------------------------
# Phony targets
# -------------------------------
.PHONY: help setup update build run clean git clean-deb clean-rpm distclean

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
# Setup
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
		-ldflags "-X 'main.Version=$(shell date +%Y.%m.%d)' -X 'main.BuildTime=$(shell date +%Y-%m-%dT%H:%M:%S)'" \
		-o $(BINARY) ./$(MAIN_DIR)
	@echo "✅ Built: $(BINARY)"

run: build ## Run the application
	@./$(BINARY)

# -------------------------------
# Clean
# -------------------------------
# Καθαρίζει το binary και ό,τι προσωρινό υπάρχει στο pkgroot
clean:
	@rm -f bin/*
	@rm -rf build/pkgroot/DEBIAN
	@rm -rf build/pkgroot/etc
	@rm -rf build/pkgroot/usr
	@rm -rf build/pkgroot/lib
	@rm -f  build/pkgroot/LICENSE
	@echo "🧹 Cleaned: bin, build/pkgroot"

# Καθαρίζει DEB artifacts (deb πακέτα + staging)
clean-deb:
	@rm -rf build/deb
	@rm -f  build/*.deb build/deb/*.deb build/deb/*/*.deb
	@# προαιρετικά: καθάρισε και ό,τι deb έμεινε κάπου αλλού
	@find build -maxdepth 2 -type f -name '*.deb' -delete 2>/dev/null || true
	@echo "🧹 Cleaned: deb artifacts"

# Καθαρίζει RPM artifacts αλλά ΔΕΝ αγγίζει SPECS/
clean-rpm:
	@rm -rf packaging/rpm/BUILD packaging/rpm/BUILDROOT
	@rm -rf packaging/rpm/RPMS packaging/rpm/SRPMS packaging/rpm/SOURCES
	@# αν έχεις αλλάξει το ARCH folder name, σβήσ’ τα όλα:
	@find packaging/rpm -type f -name '*.rpm' -delete 2>/dev/null || true
	@echo "🧹 Cleaned: rpm artifacts (kept SPECS/)"

# Πλήρες cleanup (ό,τι κάνει το clean + deb + rpm)
distclean: clean clean-deb clean-rpm
	@echo "🧨 Distclean done"



# -------------------------------
# Git helper
# -------------------------------
git: ## Commit + push με προσαρμοσμένο μήνυμα
	@read -p "Enter commit message: " MSG && \
	git add . && \
	git commit -m "$$MSG" && \
	git push


deb: build
	@echo "PKGROOT=[$(PKGROOT)] OUTDIR=[$(OUTDIR)]"
	@test -n "$(PKGROOT)" && test -n "$(OUTDIR)"
	@rm -rf "$(PKGROOT)" && mkdir -p "$(PKGROOT)/DEBIAN" \
		"$(PKGROOT)/usr/bin" \
		"$(PKGROOT)/lib/systemd/system" \
		"$(PKGROOT)/usr/share/ngm/configs" \
		"$(PKGROOT)/etc/ngm" \
		"$(OUTDIR)"

	# copy DEBIAN metadata/scripts
	@cp -a "$(DEB_SRC)/." "$(PKGROOT)/DEBIAN/"
	@sed -i "s/^Version:.*/Version: $(VERSION)-1/" "$(PKGROOT)/DEBIAN/control"

	# payload
	@install -m0755 "$(BIN)" "$(PKGROOT)/usr/bin/ngm"
	@install -m0640 "$(CONFIG_DIR)/ngm.service"   "$(PKGROOT)/lib/systemd/system/ngm.service"
	@install -m0640 "$(CONFIG_DIR)/ngm.conf"      "$(PKGROOT)/etc/ngm/ngm.conf"

	@rsync -a --delete "$(CONFIG_DIR)/" "$(PKGROOT)/usr/share/ngm/configs/"
	# executables
	@chmod 0755 "$(PKGROOT)/DEBIAN/postinst" "$(PKGROOT)/DEBIAN/prerm" "$(PKGROOT)/DEBIAN/postrm" 2>/dev/null || true

	# build artifact -> build/deb/
	@fakeroot dpkg-deb --build "$(PKGROOT)" "$(OUTDIR)/ngm_$(VERSION)-1_$(ARCH).deb"
	@echo "📦 Built: $(OUTDIR)/ngm_$(VERSION)-1_$(ARCH).deb"





stage-pkgroot: build
	@echo "→ Staging into $(PKGROOT)"
	# binary
	@mkdir -p $(PKGROOT)/usr/bin
	@cp -f $(BINARY) $(PKGROOT)/usr/bin/ngm
	# configs
	@mkdir -p $(PKGROOT)/etc/ngm
	@[ -f $(PKGROOT)/etc/ngm/ngm.conf ]       || cp -f $(CONFIG_DIR)/ngm.conf       $(PKGROOT)/etc/ngm/
	# === ship ALL example configs ===
	@mkdir -p $(PKGROOT)/usr/share/ngm/configs
	@rsync -a --delete "$(CONFIG_DIR)/" "$(PKGROOT)/usr/share/ngm/configs/"

	# systemd unit (RPM-friendly path)
	@mkdir -p $(PKGROOT)/usr/lib/systemd/system
	@cp -f $(CONFIG_DIR)/ngm.service $(PKGROOT)/usr/lib/systemd/system/ngm.service


rpm_prep_dirs:
	@mkdir -p $(RPMTOP)/{BUILD,BUILDROOT,RPMS,SRPMS,SPECS,SOURCES}

rpm_spec_version:
	@sed -i 's/^Version:.*/Version:        $(RPM_VERSION)/' $(SPECFILE)
	@sed -i 's/^Release:.*/Release:        $(RPM_RELEASE)%{?dist}/' $(SPECFILE)


.PHONY: stage-rpm
stage-rpm: stage-pkgroot
	@echo "→ Staging RPM systemd unit"
	@mkdir -p $(PKGROOT)/usr/lib/systemd/system
	@cp -f $(CONFIG_DIR)/ngm.service $(PKGROOT)/usr/lib/systemd/system/ngm.service



# --- RPM (.rpm) --- (μόνο η τελευταία γραμμή αλλάζει)
rpm: rpm_prep_dirs rpm_spec_version stage-rpm ## Δημιουργεί .rpm
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
	@echo "✅ RPMs under: $(RPMTOP)/RPMS/$(RPM_ARCH)"




# --- Sync both DEB & RPM to remote repo ---
.PHONY: sync
sync:
	@set -euo pipefail; \
	DEB_FILE="$$(ls -1t build/deb/ngm_*_amd64.deb | head -n1)"; \
	RPM_FILE="$$(ls -1t packaging/rpm/RPMS/*/ngm-*.rpm | head -n1)"; \
	[ -n "$$DEB_FILE" ] || { echo "❌ No .deb package found in build/deb"; exit 1; }; \
	[ -n "$$RPM_FILE" ] || { echo "❌ No .rpm package found in packaging/rpm/RPMS"; exit 1; }; \
	echo "🌐 Syncing to $(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)"; \
	$(SSH_CMD) $(REMOTE_USER)@$(REMOTE_HOST) "mkdir -p $(REMOTE_DIR)/deb $(REMOTE_DIR)/rpm"; \
	echo "→ Upload: $$DEB_FILE -> $(REMOTE_DIR)/deb/"; \
	rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" "$$DEB_FILE" "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/deb/"; \
	echo "→ Upload: $$RPM_FILE -> $(REMOTE_DIR)/rpm/"; \
	rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" "$$RPM_FILE" "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/rpm/"; \
	echo "→ Upload: checksums.txt -> $(REMOTE_DIR)/"; \
	if [ -f checksums.txt ]; then \
	  rsync $(RSYNC_FLAGS) -e "$(SSH_CMD)" checksums.txt "$(REMOTE_USER)@$(REMOTE_HOST):$(REMOTE_DIR)/"; \
	fi; \
	echo "✅ Remote sync complete."


