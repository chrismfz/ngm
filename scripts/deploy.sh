#!/usr/bin/env bash
set -euo pipefail

# Minimal Debian-oriented deploy/update script for packaged OpenResty + ngm.
# Re-runnable:
# - installs missing packages only
# - clones repo if missing, otherwise pulls latest changes
# - rebuilds ngm
# - ensures /opt/ngm/bin/ngm exists and is mode 0755
# - refreshes bootstrap cert + nginx.conf
# - installs ngm systemd unit from the repo, reloads systemd, and enables it
#   without starting it

MASTER_CONF_URL="${MASTER_CONF_URL:-https://raw.githubusercontent.com/chrismfz/ngm/refs/heads/main/nginx.conf.master}"
NGM_REPO_URL="${NGM_REPO_URL:-https://github.com/chrismfz/ngm.git}"
NGM_REPO_BRANCH="${NGM_REPO_BRANCH:-main}"
NGM_REPO_DIR="${NGM_REPO_DIR:-/opt/ngm}"
NGM_INSTALL_BIN="${NGM_INSTALL_BIN:-/opt/ngm/bin/ngm}"
NGM_BUILD_OUTPUT="${NGM_BUILD_OUTPUT:-}"
NGM_SYSTEMD_SRC="${NGM_SYSTEMD_SRC:-${NGM_REPO_DIR}/configs/ngm.service}"
NGM_SYSTEMD_DST="${NGM_SYSTEMD_DST:-/etc/systemd/system/ngm.service}"

OPENRESTY_PREFIX="/usr/local/openresty"
NGINX_PREFIX="${OPENRESTY_PREFIX}/nginx"
HTML_PATH="${NGINX_PREFIX}/html"
SELFSIGNED_DIR="${NGINX_PREFIX}/conf/selfsigned"

WEB_GROUP="root"

log() {
    echo "[+] $*"
}

warn() {
    echo "[!] $*" >&2
}

die() {
    echo "[ERROR] $*" >&2
    exit 1
}

need_root() {
    if [ "${EUID:-$(id -u)}" -ne 0 ]; then
        die "Please run as root"
    fi
}

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

pkg_installed() {
    dpkg -s "$1" >/dev/null 2>&1
}

apt_install_missing() {
    local pkgs=()
    local pkg

    for pkg in "$@"; do
        if ! pkg_installed "$pkg"; then
            pkgs+=("$pkg")
        fi
    done

    if [ "${#pkgs[@]}" -gt 0 ]; then
        log "Installing packages: ${pkgs[*]}"
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y "${pkgs[@]}"
    else
        log "Requested packages already installed"
    fi
}

get_codename() {
    local codename=""

    if command_exists lsb_release; then
        codename="$(lsb_release -sc 2>/dev/null || true)"
    fi

    if [ -z "$codename" ] && [ -f /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        codename="${VERSION_CODENAME:-}"
    fi

    [ -n "$codename" ] || die "Could not determine Debian codename"
    printf '%s\n' "$codename"
}

setup_openresty_repo() {
    local key_file="/etc/apt/trusted.gpg.d/openresty.gpg"
    local repo_file="/etc/apt/sources.list.d/openresty.list"
    local codename

    codename="$(get_codename)"

    if [ ! -f "$key_file" ]; then
        log "Adding OpenResty GPG key"
        wget -qO - https://openresty.org/package/pubkey.gpg | gpg --dearmor -o "$key_file"
    fi

    if [ ! -f "$repo_file" ] || ! grep -q 'openresty.org/package/debian' "$repo_file"; then
        log "Adding OpenResty APT repository for ${codename}"
        echo "deb http://openresty.org/package/debian ${codename} openresty" > "$repo_file"
    fi

    apt-get update
}

setup_sury_php_repo() {
    local repo_file="/etc/apt/sources.list.d/php.list"
    local keyring="/usr/share/keyrings/debsuryorg-archive-keyring.gpg"
    local codename

    codename="$(get_codename)"

    if [ ! -f "$keyring" ]; then
        log "Installing Sury PHP archive keyring"
        curl -fsSL -o /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
        dpkg -i /tmp/debsuryorg-archive-keyring.deb
    fi

    if [ ! -f "$repo_file" ] || ! grep -q 'packages.sury.org/php' "$repo_file"; then
        log "Adding Sury PHP repository for ${codename}"
        echo "deb [signed-by=${keyring}] https://packages.sury.org/php/ ${codename} main" > "$repo_file"
    fi

    apt-get update
}

install_base_packages() {
    apt_install_missing \
        ca-certificates curl wget gnupg lsb-release apt-transport-https openssl \
        git make build-essential golang-go certbot net-tools sudo acl
}

install_openresty_packages() {
    apt_install_missing openresty openresty-opm openresty-openssl3
}

install_php83_packages() {
    setup_sury_php_repo

    apt_install_missing \
        php8.3-bcmath php8.3-bz2 php8.3-cli php8.3-common php8.3-curl php8.3-decimal php8.3-enchant php8.3-fpm php8.3-gd php8.3-grpc \
        php8.3-igbinary php8.3-imagick php8.3-imap php8.3-inotify php8.3-lz4 php8.3-mailparse php8.3-maxminddb php8.3-mbstring \
        php8.3-mcrypt php8.3-memcache php8.3-memcached php8.3-mysql php8.3-opcache php8.3-protobuf php8.3-redis php8.3-rrd \
        php8.3-soap php8.3-sqlite3 php8.3-tidy php8.3-uploadprogress php8.3-uuid php8.3-xml php8.3-xmlrpc php8.3-yaml \
        php8.3-zip php8.3-zstd
}

find_opm_bin() {
    local candidates=(
        /usr/bin/opm
        /usr/local/openresty/bin/opm
        "$(command -v opm 2>/dev/null || true)"
    )
    local c

    for c in "${candidates[@]}"; do
        if [ -n "$c" ] && [ -x "$c" ]; then
            printf '%s\n' "$c"
            return 0
        fi
    done

    return 1
}

opm_package_installed() {
    local opm_bin="$1"
    local pkg="$2"
    "$opm_bin" list 2>/dev/null | awk '{print $1}' | grep -Fxq "$pkg"
}

install_opm_packages() {
    local opm_bin
    local pkgs=(
        ledgetech/lua-resty-http
        openresty/lua-resty-string
        anjia0532/lua-resty-maxminddb
    )
    local pkg

    opm_bin="$(find_opm_bin)" || die "Could not find opm binary after OpenResty installation"

    for pkg in "${pkgs[@]}"; do
        if opm_package_installed "$opm_bin" "$pkg"; then
            log "OPM package already installed: $pkg"
        else
            log "Installing OPM package: $pkg"
            "$opm_bin" get "$pkg"
        fi
    done
}

detect_web_group() {
    if getent group www-data >/dev/null 2>&1; then
        WEB_GROUP="www-data"
    elif getent group nginx >/dev/null 2>&1; then
        WEB_GROUP="nginx"
    else
        WEB_GROUP="root"
    fi

    log "Using web group: ${WEB_GROUP}"
}

prepare_layout() {
    log "Preparing OpenResty filesystem layout under ${NGINX_PREFIX}"
    mkdir -p "${NGINX_PREFIX}/conf"
    mkdir -p "${NGINX_PREFIX}/conf/sites"
    mkdir -p "${NGINX_PREFIX}/logs"
    mkdir -p "${NGINX_PREFIX}/lua"
    mkdir -p "${NGINX_PREFIX}/cache/proxy_micro"
    mkdir -p "${NGINX_PREFIX}/cache/proxy_static"
    mkdir -p "${NGINX_PREFIX}/cache/fastcgi"
    mkdir -p "${HTML_PATH}"

    chown -R root:"${WEB_GROUP}" "${NGINX_PREFIX}/logs" "${NGINX_PREFIX}/cache" || true
}

create_default_certs_if_missing() {
    log "Ensuring self-signed bootstrap cert exists"
    mkdir -p "${SELFSIGNED_DIR}"
    chown -R root:"${WEB_GROUP}" "${SELFSIGNED_DIR}"
    chmod 0750 "${SELFSIGNED_DIR}"

    if [ -s "${SELFSIGNED_DIR}/privkey.pem" ] && [ -s "${SELFSIGNED_DIR}/fullchain.pem" ]; then
        log "Default certs already present"
        return 0
    fi

    umask 027
    openssl req -x509 -newkey ec \
        -pkeyopt ec_paramgen_curve:prime256v1 \
        -nodes \
        -keyout "${SELFSIGNED_DIR}/privkey.pem" \
        -out "${SELFSIGNED_DIR}/fullchain.pem" \
        -days 3650 \
        -subj "/CN=localhost" \
        -addext "subjectAltName=DNS:localhost"

    chown root:"${WEB_GROUP}" "${SELFSIGNED_DIR}/privkey.pem" "${SELFSIGNED_DIR}/fullchain.pem"
    chmod 0640 "${SELFSIGNED_DIR}/privkey.pem"
    chmod 0644 "${SELFSIGNED_DIR}/fullchain.pem"
}

backup_if_exists() {
    local file="$1"
    if [ -f "$file" ]; then
        cp -a "$file" "${file}.bak"
        log "Backed up ${file} -> ${file}.bak"
    fi
}

install_master_nginx_conf() {
    local dst="${NGINX_PREFIX}/conf/nginx.conf"

    log "Refreshing master nginx.conf"
    backup_if_exists "$dst"
    curl -fsSL "$MASTER_CONF_URL" -o "$dst"
}

install_logrotate_config() {
    log "Installing logrotate config"
    cat > /etc/logrotate.d/openresty-custom <<EOF2
${NGINX_PREFIX}/logs/*.log {
    daily
    rotate 14
    compress
    notifempty
    create 0640 root ${WEB_GROUP}
    sharedscripts
    postrotate
        [ -f ${NGINX_PREFIX}/logs/nginx.pid ] && kill -USR1 \$(cat ${NGINX_PREFIX}/logs/nginx.pid)
    endscript
}
EOF2
}

ensure_ngm_repo() {
    mkdir -p /opt

    if [ -d "${NGM_REPO_DIR}/.git" ]; then
        log "Updating existing ngm repo in ${NGM_REPO_DIR}"
        git -C "${NGM_REPO_DIR}" fetch --all --prune
        git -C "${NGM_REPO_DIR}" checkout "${NGM_REPO_BRANCH}"
        git -C "${NGM_REPO_DIR}" pull --ff-only origin "${NGM_REPO_BRANCH}"
        return 0
    fi

    if [ -d "${NGM_REPO_DIR}" ] && [ -n "$(ls -A "${NGM_REPO_DIR}" 2>/dev/null || true)" ]; then
        die "${NGM_REPO_DIR} exists but is not a git checkout"
    fi

    [ -n "${NGM_REPO_URL}" ] || die "NGM_REPO_URL is required for first clone"

    log "Cloning ngm repo into ${NGM_REPO_DIR}"
    git clone --branch "${NGM_REPO_BRANCH}" --single-branch "${NGM_REPO_URL}" "${NGM_REPO_DIR}"
}

build_ngm() {
    [ -f "${NGM_REPO_DIR}/Makefile" ] || die "No Makefile found in ${NGM_REPO_DIR}"

    log "Building ngm with make build"
    make -C "${NGM_REPO_DIR}" build
}

find_ngm_build_output() {
    local candidates=()
    local c

    if [ -n "${NGM_BUILD_OUTPUT}" ]; then
        candidates+=("${NGM_BUILD_OUTPUT}")
    fi

    candidates+=(
        "${NGM_REPO_DIR}/bin/ngm"
        "${NGM_REPO_DIR}/ngm"
        "${NGM_REPO_DIR}/build/ngm"
        "${NGM_REPO_DIR}/dist/ngm"
        "${NGM_REPO_DIR}/cmd/ngm/ngm"
    )

    for c in "${candidates[@]}"; do
        if [ -f "$c" ] && [ -x "$c" ]; then
            printf '%s\n' "$c"
            return 0
        fi
    done

    return 1
}

install_ngm_binary() {
    local src
    local dst_dir

    src="$(find_ngm_build_output)" || die "Built ngm binary not found. Set NGM_BUILD_OUTPUT explicitly."

    dst_dir="$(dirname "${NGM_INSTALL_BIN}")"
    mkdir -p "${dst_dir}"

    if [ "$(readlink -f "$src")" = "$(readlink -f "${NGM_INSTALL_BIN}")" ]; then
        chmod 0755 "${NGM_INSTALL_BIN}"
        log "ngm binary already in place, ensured mode 0755: ${NGM_INSTALL_BIN}"
        return 0
    fi

    install -m 0755 "$src" "${NGM_INSTALL_BIN}"
    log "Installed ngm binary: ${src} -> ${NGM_INSTALL_BIN}"
}

install_ngm_systemd_unit() {
    [ -f "${NGM_SYSTEMD_SRC}" ] || die "ngm systemd unit not found: ${NGM_SYSTEMD_SRC}"

    backup_if_exists "${NGM_SYSTEMD_DST}"
    install -m 0644 "${NGM_SYSTEMD_SRC}" "${NGM_SYSTEMD_DST}"
    log "Installed systemd unit: ${NGM_SYSTEMD_SRC} -> ${NGM_SYSTEMD_DST}"
}

reload_systemd() {
    if command_exists systemctl; then
        log "Reloading systemd daemon"
        systemctl daemon-reload
    else
        warn "systemctl not found; skipping daemon-reload"
    fi
}

test_nginx_config() {
    local bin
    if command_exists openresty; then
        bin="$(command -v openresty)"
    elif [ -x "${NGINX_PREFIX}/sbin/nginx" ]; then
        bin="${NGINX_PREFIX}/sbin/nginx"
    else
        die "Could not find OpenResty/nginx binary for config test"
    fi

    log "Testing nginx config via ${bin}"
    "$bin" -t -p "${NGINX_PREFIX}" -c "${NGINX_PREFIX}/conf/nginx.conf"
}

enable_openresty() {
    if command_exists systemctl; then
        log "Enabling OpenResty service"
        systemctl enable openresty
    else
        warn "systemctl not found; skipping service enable"
    fi
}

enable_ngm_service() {
    if command_exists systemctl; then
        log "Enabling ngm service (not starting it)"
        systemctl enable ngm
    else
        warn "systemctl not found; skipping ngm enable"
    fi
}

show_versions() {
    log "OpenResty build info"
    if command_exists openresty; then
        openresty -V 2>&1 || true
    fi

    log "ngm version/build output"
    if [ -x "${NGM_INSTALL_BIN}" ]; then
        "${NGM_INSTALL_BIN}" --version 2>&1 || true
    fi
}

main() {
    need_root

    [ -f /etc/debian_version ] || die "This script currently targets Debian/Ubuntu only"

    install_base_packages
    setup_openresty_repo
    install_openresty_packages
    install_opm_packages
    detect_web_group
    prepare_layout
    create_default_certs_if_missing
    install_master_nginx_conf
    install_logrotate_config
    install_php83_packages
    ensure_ngm_repo
    build_ngm
    install_ngm_binary
    install_ngm_systemd_unit
    reload_systemd
    test_nginx_config
    enable_openresty
    enable_ngm_service
    show_versions

    log "Deployment/update complete"
}

main "$@"

