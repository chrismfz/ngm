#!/usr/bin/env bash
set -euo pipefail

# Fresh EL10-style bootstrap for NGM
# Assumptions:
# - If config is missing, bootstrap from src/configs/config.yaml
# - you want nginx runtime group handling around "nobody"
# - PHP is Remi parallel PHP 8.3 (php83-php-fpm)
#
# Optional env overrides:
#   REPO_URL=...
#   INSTALL_DIR=/opt/ngm
#   RUNTIME_DIR=/opt/ngm
#   CFG_FILE=/opt/ngm/config.yaml
#   DNS_ENABLED=true
#   FIREWALLD_DISABLE=true
#
# Example:
#   chmod +x bootstrap_ngm_el10_nobody.sh
#   # Optional pre-run: place your config file at /opt/ngm/config.yaml (or set CFG_FILE to another path)
#   # If omitted, installer mode will copy src/configs/config.yaml to CFG_FILE.
#   ./bootstrap_ngm_el10_nobody.sh

REPO_URL="${REPO_URL:-https://github.com/chrismfz/ngm.git}"
INSTALL_DIR="${INSTALL_DIR:-/opt/ngm}"
SRC_DIR="${SRC_DIR:-$INSTALL_DIR/src}"
RUNTIME_DIR="${RUNTIME_DIR:-$INSTALL_DIR}"
BIN_DIR="$RUNTIME_DIR"
CFG_DIR="$RUNTIME_DIR"
CFG_FILE="${CFG_FILE:-$RUNTIME_DIR/config.yaml}"

# Hardcoded runtime identity model for this script
NGINX_USER="nginx"
NGINX_GROUP="nobody"

# Optional feature toggles
DNS_ENABLED="${DNS_ENABLED:-false}"
FIREWALLD_DISABLE="${FIREWALLD_DISABLE:-true}"

# Remi PHP 8.3 layout
PHP_VERSION="8.3"
PHP_SERVICE="php83-php-fpm"
PHP_POOLS_DIR="/etc/opt/remi/php83/php-fpm.d"
PHP_SOCK_DIR="/var/opt/remi/php83/run/php-fpm"

# Nginx layout
NGINX_ROOT="/etc/nginx"
NGINX_MAIN_CONF="nginx.conf"
NGINX_SITES_DIR="conf/sites"
NGINX_BIN="/usr/sbin/nginx"
NGINX_SERVICE="nginx"
NGINX_CACHE_ROOT="/var/cache/nginx"
NGM_SYSTEMD_UNIT_SRC="configs/ngm.service"
NGM_SYSTEMD_UNIT_DEST="/etc/systemd/system/ngm.service"

# ACME webroot default
CERTBOT_WEBROOT="/var/www/html"

need_root() {
  if [[ ${EUID} -ne 0 ]]; then
    echo "Run as root." >&2
    exit 1
  fi
}

install_packages() {
  dnf -y update

  # Helpful on EL clones. Ignore if not needed.
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

  # Remi parallel PHP 8.3 packages
  dnf -y install \
    php83 php83-php-fpm php83-php-cli php83-php-common php83-php-opcache \
    php83-php-mbstring php83-php-xml php83-php-pdo php83-php-mysqlnd \
    php83-php-gd php83-php-intl php83-php-zip
}

prepare_dirs() {
  local -a required_paths=(
    "$INSTALL_DIR"
    "$SRC_DIR"
    "$BIN_DIR"
    "$CFG_DIR"
    "/var/www/html"
    "/etc/named"
    "/var/named/ngm"
    "/var/lib/ngm"
    "/var/log/ngm"
    "$NGINX_ROOT/conf/sites"
    "$NGINX_ROOT/conf/.staging"
    "$NGINX_ROOT/conf/.backup"
    "$NGINX_CACHE_ROOT"
    "$NGINX_CACHE_ROOT/php"
    "$NGINX_CACHE_ROOT/proxy_micro"
    "$NGINX_CACHE_ROOT/proxy_static"
    "/run/nginx"
  )

  local path
  for path in "${required_paths[@]}"; do
    mkdir -p "$path"
  done

  # Keep certbot webroot aligned with runtime defaults unless explicitly overridden.
  mkdir -p "$CERTBOT_WEBROOT"

  # ACME webroot and cache dirs should be writable by the nginx runtime identity model.
  chown "${NGINX_USER}:${NGINX_GROUP}" "$CERTBOT_WEBROOT" || true
  chmod 0755 "$CERTBOT_WEBROOT" || true
  chown -R "${NGINX_USER}:${NGINX_GROUP}" "$NGINX_CACHE_ROOT" || true
  chmod 0755 "$NGINX_CACHE_ROOT" \
             "$NGINX_CACHE_ROOT/php" \
             "$NGINX_CACHE_ROOT/proxy_micro" \
             "$NGINX_CACHE_ROOT/proxy_static" || true

  printf 'prepare_dirs created/validated %d paths:\n' "${#required_paths[@]}"
  for path in "${required_paths[@]}"; do
    printf '  - %s\n' "$path"
  done
  if [[ "$CERTBOT_WEBROOT" != "/var/www/html" ]]; then
    printf '  - %s\n' "$CERTBOT_WEBROOT"
  fi
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

configure_selinux_for_home_sites() {
  if command -v getenforce >/dev/null 2>&1; then
    local mode
    mode="$(getenforce || true)"
    if [[ "$mode" == "Enforcing" || "$mode" == "Permissive" ]]; then
      # Allow web servers to traverse user home directories
      setsebool -P httpd_enable_homedirs 1 || true

      # Label /home/<user>/sites for web read access
      semanage fcontext -a -t httpd_sys_content_t '/home/[^/]+/sites(/.*)?' 2>/dev/null || \
      semanage fcontext -m -t httpd_sys_content_t '/home/[^/]+/sites(/.*)?' || true

      restorecon -Rv /home || true
    fi
  fi
}

enable_services() {
  systemctl enable --now "$PHP_SERVICE"

  if [[ "$DNS_ENABLED" == "true" ]]; then
    systemctl enable --now named
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
    echo "firewalld disable skipped: FIREWALLD_DISABLE=$FIREWALLD_DISABLE"
    return 0
  fi

  if ! systemctl list-unit-files | grep -q 'firewalld'; then
    echo "firewalld disable skipped: firewalld unit file not present."
    return 0
  fi

  systemctl disable --now firewalld
  echo "firewalld disabled and stopped."
}

maybe_run_ngm_provision() {
  if "$BIN_DIR/ngm" -c "$CFG_FILE" help provision >/dev/null 2>&1; then
    if ! "$BIN_DIR/ngm" -c "$CFG_FILE" provision init; then
      echo "ERROR: ngm provision init failed." >&2
      echo "ERROR: Fix nginx config mismatch in $CFG_FILE before proceeding with deployment." >&2
      return 1
    fi

    if ! "$BIN_DIR/ngm" -c "$CFG_FILE" provision test; then
      echo "ERROR: ngm provision test failed." >&2
      echo "ERROR: Fix nginx config mismatch in $CFG_FILE before proceeding with deployment." >&2
      return 1
    fi
  else
    echo "ERROR: ngm provision commands are unavailable; cannot validate nginx provisioning." >&2
    return 1
  fi
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
  echo "INFO: Installed default config template from $default_cfg_template to $CFG_FILE"

  if grep -q 'change-me-please' "$CFG_FILE"; then
    echo "WARNING: Config contains placeholder values (e.g., change-me-please)." >&2
    echo "WARNING: Update and harden $CFG_FILE before using in production." >&2
  fi
}

ask_hostname_certificate() {
  local host=""
  host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
  host="${host,,}"

  if [[ -z "$host" || "$host" == "localhost" ]]; then
    echo "INFO: certificate request skipped: unable to resolve a usable hostname."
    return 0
  fi

  if "$BIN_DIR/ngm" -c "$CFG_FILE" cert issue --domain "$host"; then
    echo "INFO: certificate request succeeded for hostname: $host"
  else
    echo "WARN: certificate request failed for hostname: $host" >&2
  fi
}

print_next_steps() {
  cat <<EOF

Bootstrap complete.

Key paths:
  Runtime dir: $RUNTIME_DIR
  NGM binary : $BIN_DIR/ngm
  Config     : $CFG_FILE
  PHP pools  : $PHP_POOLS_DIR
  PHP sockdir: $PHP_SOCK_DIR

Hardcoded runtime assumptions in this script:
  nginx user : $NGINX_USER
  nginx group: $NGINX_GROUP

Notes:
- This script assumes Remi parallel PHP 8.3 (php83-php-fpm).
- If config is missing, installer mode bootstraps from src/configs/config.yaml.
- SELinux adjustments were applied for content under /home/<user>/sites.
- If DNS_ENABLED=false, bind was still installed, but named was not enabled.

Suggested checks:
  "$BIN_DIR/ngm" -c "$CFG_FILE" help
  "$BIN_DIR/ngm" -c "$CFG_FILE" provision test
  install -m 0644 ./config.yaml "$CFG_FILE"   # if config is missing
  systemctl status $PHP_SERVICE
  systemctl status nginx ngm --no-pager
  ls -ld $PHP_POOLS_DIR $PHP_SOCK_DIR
  getenforce || true

EOF
}

main() {
  need_root
  install_packages
  prepare_dirs
  clone_or_update_repo
  build_ngm
  ensure_runtime_config
  maybe_run_ngm_provision
  install_service_units
  configure_selinux_for_home_sites
  enable_services
  systemctl start "$NGINX_SERVICE"
  disable_firewalld_if_requested
  systemctl start ngm
  ask_hostname_certificate
  print_next_steps
}

main "$@"
