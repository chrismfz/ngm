#!/usr/bin/env bash
set -euo pipefail

# EL10 bootstrap for NGM
#
# Goals:
# - use the real config.yaml before provisioning
# - provision nginx first, then start nginx, then request LE certs
# - avoid hard-failing on hostname cert if local ACME path is not yet reachable
# - keep service enable/start order predictable
#
# Optional env overrides:
#   REPO_URL=...
#   INSTALL_DIR=/opt/ngm
#   RUNTIME_DIR=/opt/ngm
#   CFG_FILE=/opt/ngm/config.yaml
#   DNS_ENABLED=true
#   FIREWALLD_DISABLE=true
#   ISSUE_HOSTNAME_CERT=true
#   CERTBOT_WEBROOT=/var/www/html   # optional hard override
#
# Notes:
# - bootstrap/provision logic inside ngm may still try an early cert attempt during
#   `ngm provision init`. This script cannot suppress that unless the Go side adds a flag.
# - this script only requests the hostname cert once *after* nginx is up and the ACME path
#   is locally reachable for the requested host.

REPO_URL="${REPO_URL:-https://github.com/chrismfz/ngm.git}"
INSTALL_DIR="${INSTALL_DIR:-/opt/ngm}"
SRC_DIR="${SRC_DIR:-$INSTALL_DIR/src}"
RUNTIME_DIR="${RUNTIME_DIR:-$INSTALL_DIR}"
BIN_DIR="$RUNTIME_DIR"
CFG_DIR="$RUNTIME_DIR"
CFG_FILE="${CFG_FILE:-$RUNTIME_DIR/config.yaml}"

# Defaults used until config.yaml is present and parsed.
NGINX_USER="${NGINX_USER:-nginx}"
NGINX_GROUP="${NGINX_GROUP:-nobody}"
NGINX_ROOT="${NGINX_ROOT:-/etc/nginx}"
NGINX_MAIN_CONF_REL="${NGINX_MAIN_CONF_REL:-nginx.conf}"
NGINX_SITES_DIR_REL="${NGINX_SITES_DIR_REL:-conf/sites}"
NGINX_STAGING_DIR_REL="${NGINX_STAGING_DIR_REL:-conf/.staging}"
NGINX_BACKUP_DIR_REL="${NGINX_BACKUP_DIR_REL:-conf/.backup}"
NGINX_SERVICE="${NGINX_SERVICE:-nginx}"
NGINX_CACHE_ROOT="${NGINX_CACHE_ROOT:-/var/cache/nginx}"
CERTBOT_WEBROOT="${CERTBOT_WEBROOT:-/var/www/html}"

DNS_ENABLED="${DNS_ENABLED:-false}"
FIREWALLD_DISABLE="${FIREWALLD_DISABLE:-true}"
ISSUE_HOSTNAME_CERT="${ISSUE_HOSTNAME_CERT:-true}"

PHP_SERVICE="${PHP_SERVICE:-php83-php-fpm}"
PHP_POOLS_DIR="${PHP_POOLS_DIR:-/etc/opt/remi/php83/php-fpm.d}"
PHP_SOCK_DIR="${PHP_SOCK_DIR:-/var/opt/remi/php83/run/php-fpm}"

NGM_SYSTEMD_UNIT_SRC="configs/ngm.service"
NGM_SYSTEMD_UNIT_DEST="/etc/systemd/system/ngm.service"

log() {
  printf '[el_deploy] %s\n' "$*"
}

need_root() {
  if [[ ${EUID} -ne 0 ]]; then
    echo "Run as root." >&2
    exit 1
  fi
}

install_packages() {
  dnf -y update

  dnf -y install 'dnf-command(config-manager)' || true
  dnf config-manager --set-enabled crb >/dev/null 2>&1 || true

  dnf -y install epel-release
  dnf -y install https://rpms.remirepo.net/enterprise/remi-release-10.rpm

  dnf -y install \
    nginx \
    bind bind-utils \
    certbot python3-certbot-nginx \
    git curl wget tar openssl \
    go-toolset \
    policycoreutils-python-utils

  dnf -y install \
    php83 php83-php-fpm php83-php-cli php83-php-common php83-php-opcache \
    php83-php-mbstring php83-php-xml php83-php-pdo php83-php-mysqlnd \
    php83-php-gd php83-php-intl php83-php-zip
}

clone_or_update_repo() {
  if [[ -d "$SRC_DIR/.git" ]]; then
    git -C "$SRC_DIR" pull --ff-only
  else
    rm -rf "$SRC_DIR"
    git clone "$REPO_URL" "$SRC_DIR"
  fi
}

build_ngm() {
  cd "$SRC_DIR"
  go build -o "$BIN_DIR/ngm" ./cmd/ngm
  chmod +x "$BIN_DIR/ngm"
}

ensure_runtime_config() {
  local default_cfg_template="$SRC_DIR/configs/config.yaml"

  if [[ -f "$CFG_FILE" ]]; then
    return 0
  fi

  if [[ ! -f "$default_cfg_template" ]]; then
    echo "ERROR: Config file not found at resolved CFG_FILE path: $CFG_FILE" >&2
    echo "ERROR: Default template not found at: $default_cfg_template" >&2
    echo "ERROR: Example fix: install -m 0644 ./config.yaml \"$CFG_FILE\"" >&2
    echo "ERROR: Deployment aborted. Provide a valid config at $CFG_FILE and re-run deployment." >&2
    return 1
  fi

  install -m 0644 "$default_cfg_template" "$CFG_FILE"
  log "Installed default config template from $default_cfg_template to $CFG_FILE"

  if grep -q 'change-me-please' "$CFG_FILE"; then
    echo "WARNING: Config contains placeholder values (e.g., change-me-please)." >&2
    echo "WARNING: Update and harden $CFG_FILE before using in production." >&2
  fi
}

yaml_read_under_section() {
  local section="$1"
  local key="$2"
  local file="$3"

  awk -v section="$section" -v key="$key" '
    function trim(s) {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", s)
      return s
    }
    /^[[:space:]]*#/ { next }
    {
      if ($0 ~ "^[A-Za-z0-9_-]+:[[:space:]]*$") {
        current=$0
        sub(/:.*/, "", current)
        in_section=(current == section)
        next
      }
      if (in_section && $0 ~ "^[[:space:]]+" key ":[[:space:]]*") {
        value=$0
        sub("^[[:space:]]+" key ":[[:space:]]*", "", value)
        value=trim(value)
        gsub(/^"/, "", value)
        gsub(/"$/, "", value)
        print value
        exit
      }
    }
  ' "$file"
}

resolve_runtime_from_config() {
  local v

  [[ -f "$CFG_FILE" ]] || return 0

  v="$(yaml_read_under_section nginx root "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_ROOT="$v"

  v="$(yaml_read_under_section nginx main_conf "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_MAIN_CONF_REL="$v"

  v="$(yaml_read_under_section nginx sites_dir "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_SITES_DIR_REL="$v"

  v="$(yaml_read_under_section nginx cache_root "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_CACHE_ROOT="$v"

  v="$(yaml_read_under_section nginx service_name "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_SERVICE="$v"

  v="$(yaml_read_under_section nginx user "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_USER="$v"

  v="$(yaml_read_under_section nginx group "$CFG_FILE" || true)"
  [[ -n "$v" ]] && NGINX_GROUP="$v"

  v="$(yaml_read_under_section certs webroot "$CFG_FILE" || true)"
  [[ -n "$v" ]] && CERTBOT_WEBROOT="$v"
}

join_under_root() {
  local root="$1"
  local path="$2"
  root="${root%/}"
  if [[ "$path" == /* ]]; then
    printf '%s\n' "$path"
  else
    printf '%s\n' "$root/$path"
  fi
}

prepare_dirs() {
  local nginx_sites_dir nginx_staging_dir nginx_backup_dir
  nginx_sites_dir="$(join_under_root "$NGINX_ROOT" "$NGINX_SITES_DIR_REL")"
  nginx_staging_dir="$(join_under_root "$NGINX_ROOT" "$NGINX_STAGING_DIR_REL")"
  nginx_backup_dir="$(join_under_root "$NGINX_ROOT" "$NGINX_BACKUP_DIR_REL")"

  declare -A seen=()
  local -a required_paths=(
    "$INSTALL_DIR"
    "$SRC_DIR"
    "$BIN_DIR"
    "$CFG_DIR"
    "$CERTBOT_WEBROOT"
    "/etc/named"
    "/var/named/ngm"
    "/var/lib/ngm"
    "/var/log/ngm"
    "$nginx_sites_dir"
    "$nginx_staging_dir"
    "$nginx_backup_dir"
    "$NGINX_CACHE_ROOT"
    "$NGINX_CACHE_ROOT/php"
    "$NGINX_CACHE_ROOT/proxy_micro"
    "$NGINX_CACHE_ROOT/proxy_static"
    "/run/nginx"
  )

  local path
  for path in "${required_paths[@]}"; do
    [[ -n "$path" ]] || continue
    if [[ -z "${seen[$path]:-}" ]]; then
      mkdir -p "$path"
      seen[$path]=1
    fi
  done

  chown "${NGINX_USER}:${NGINX_GROUP}" "$CERTBOT_WEBROOT" || true
  chmod 0755 "$CERTBOT_WEBROOT" || true
  chown -R "${NGINX_USER}:${NGINX_GROUP}" "$NGINX_CACHE_ROOT" || true
  chmod 0755 \
    "$NGINX_CACHE_ROOT" \
    "$NGINX_CACHE_ROOT/php" \
    "$NGINX_CACHE_ROOT/proxy_micro" \
    "$NGINX_CACHE_ROOT/proxy_static" || true

  log "prepare_dirs created/validated ${#seen[@]} unique paths"
  for path in "${!seen[@]}"; do
    printf '  - %s\n' "$path"
  done | sort
}

configure_selinux_for_home_sites() {
  if command -v getenforce >/dev/null 2>&1; then
    local mode
    mode="$(getenforce || true)"
    if [[ "$mode" == "Enforcing" || "$mode" == "Permissive" ]]; then
      setsebool -P httpd_enable_homedirs 1 || true
      semanage fcontext -a -t httpd_sys_content_t '/home/[^/]+/sites(/.*)?' 2>/dev/null || \
      semanage fcontext -m -t httpd_sys_content_t '/home/[^/]+/sites(/.*)?' || true
      restorecon -Rv /home || true
    fi
  fi
}

install_service_units() {
  local repo_unit_path="$SRC_DIR/$NGM_SYSTEMD_UNIT_SRC"
  if [[ ! -f "$repo_unit_path" ]]; then
    echo "ERROR: Service unit file not found: $repo_unit_path" >&2
    return 1
  fi

  install -m 0644 "$repo_unit_path" "$NGM_SYSTEMD_UNIT_DEST"
  systemctl daemon-reload
  systemctl enable "$NGINX_SERVICE"
  systemctl enable ngm
}

disable_firewalld_if_requested() {
  if [[ "$FIREWALLD_DISABLE" != "true" ]]; then
    log "firewalld disable skipped: FIREWALLD_DISABLE=$FIREWALLD_DISABLE"
    return 0
  fi

  if ! systemctl list-unit-files | grep -q '^firewalld\.service'; then
    log "firewalld disable skipped: firewalld unit file not present."
    return 0
  fi

  systemctl disable --now firewalld
  log "firewalld disabled and stopped."
}

enable_base_services() {
  systemctl enable --now "$PHP_SERVICE"

  if [[ "$DNS_ENABLED" == "true" ]]; then
    systemctl enable --now named
  fi
}

maybe_run_ngm_provision() {
  if ! "$BIN_DIR/ngm" -c "$CFG_FILE" help provision >/dev/null 2>&1; then
    echo "ERROR: ngm provision commands are unavailable; cannot validate nginx provisioning." >&2
    return 1
  fi

  "$BIN_DIR/ngm" -c "$CFG_FILE" provision init
  "$BIN_DIR/ngm" -c "$CFG_FILE" provision test
}

start_nginx_and_ngm() {
  systemctl start "$NGINX_SERVICE"
  systemctl start ngm
}

wait_for_http_local() {
  local tries="${1:-20}"
  local delay="${2:-1}"
  local i
  for ((i=1; i<=tries; i++)); do
    if curl -fsS http://127.0.0.1/ >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay"
  done
  return 1
}

probe_acme_path_for_host() {
  local host="$1"
  local token="ngm-acme-probe-$(date +%s)-$$"
  local probe_dir="$CERTBOT_WEBROOT/.well-known/acme-challenge"
  local probe_file="$probe_dir/$token"
  mkdir -p "$probe_dir"
  printf '%s\n' "$token" > "$probe_file"
  chmod 0644 "$probe_file" || true

  if curl -fsS --resolve "$host:80:127.0.0.1" "http://$host/.well-known/acme-challenge/$token" | grep -qx "$token"; then
    rm -f "$probe_file"
    return 0
  fi

  rm -f "$probe_file"
  return 1
}

ask_hostname_certificate() {
  local host=""

  if [[ "$ISSUE_HOSTNAME_CERT" != "true" ]]; then
    log "hostname cert request skipped: ISSUE_HOSTNAME_CERT=$ISSUE_HOSTNAME_CERT"
    return 0
  fi

  host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
  host="${host,,}"

  if [[ -z "$host" || "$host" == "localhost" ]]; then
    log "hostname cert request skipped: unable to resolve a usable hostname."
    return 0
  fi

  if ! wait_for_http_local 20 1; then
    echo "WARN: nginx does not appear reachable on local HTTP yet; skipping hostname cert request for $host" >&2
    return 0
  fi

  if ! probe_acme_path_for_host "$host"; then
    echo "WARN: ACME local probe failed for $host using webroot $CERTBOT_WEBROOT" >&2
    echo "WARN: Fix nginx ACME location / host routing before requesting the certificate." >&2
    return 0
  fi

  if "$BIN_DIR/ngm" -c "$CFG_FILE" cert issue --domain "$host"; then
    log "certificate request succeeded for hostname: $host"
  else
    echo "WARN: certificate request failed for hostname: $host" >&2
  fi
}

print_next_steps() {
  cat <<EOF2

Bootstrap complete.

Key paths:
  Runtime dir : $RUNTIME_DIR
  NGM binary  : $BIN_DIR/ngm
  Config      : $CFG_FILE
  ACME webroot: $CERTBOT_WEBROOT
  PHP pools   : $PHP_POOLS_DIR
  PHP sockdir : $PHP_SOCK_DIR

Runtime values in use:
  nginx root  : $NGINX_ROOT
  nginx user  : $NGINX_USER
  nginx group : $NGINX_GROUP
  nginx svc   : $NGINX_SERVICE

Notes:
- Config was loaded before provisioning, so nginx/cert paths can follow config.yaml.
- Hostname cert is requested only after provision init/test, nginx start, and a local ACME probe.
- If ngm itself attempts an early hostname cert during provision init, that behavior must be changed in Go code.

Suggested checks:
  "$BIN_DIR/ngm" -c "$CFG_FILE" help
  "$BIN_DIR/ngm" -c "$CFG_FILE" provision test
  systemctl status $PHP_SERVICE --no-pager
  systemctl status $NGINX_SERVICE ngm --no-pager
  curl -I http://127.0.0.1/
  getenforce || true

EOF2
}

main() {
  need_root
  install_packages
  clone_or_update_repo
  build_ngm
  ensure_runtime_config
  resolve_runtime_from_config
  prepare_dirs
  install_service_units
  configure_selinux_for_home_sites
  disable_firewalld_if_requested
  enable_base_services
  maybe_run_ngm_provision
  start_nginx_and_ngm
  ask_hostname_certificate
  print_next_steps
}

main "$@"
