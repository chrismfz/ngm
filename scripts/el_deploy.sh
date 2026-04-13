#!/usr/bin/env bash
set -euo pipefail

# Fresh EL10-style bootstrap for NGM
# Assumptions:
# - config.yaml already exists and is correct
# - you want nginx runtime group handling around "nobody"
# - PHP is Remi parallel PHP 8.3 (php83-php-fpm)
#
# Optional env overrides:
#   REPO_URL=...
#   INSTALL_DIR=/opt/ngm
#   RUNTIME_DIR=/opt/ngm
#   CFG_FILE=/opt/ngm/config.yaml
#   DNS_ENABLED=true
#
# Example:
#   chmod +x bootstrap_ngm_el10_nobody.sh
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
  mkdir -p "$INSTALL_DIR" "$SRC_DIR" "$BIN_DIR" "$CFG_DIR"
  mkdir -p /var/lib/ngm /var/log/ngm
  mkdir -p "$NGINX_ROOT/conf/sites" "$NGINX_ROOT/conf/.staging" "$NGINX_ROOT/conf/.backup"
  mkdir -p "$NGINX_CACHE_ROOT/php" "$NGINX_CACHE_ROOT/proxy_micro" "$NGINX_CACHE_ROOT/proxy_static"
  mkdir -p "$CERTBOT_WEBROOT"

  # BIND-related dirs if you plan to enable DNS later
  mkdir -p /etc/named
  mkdir -p /var/named/ngm || true

  # Cache dirs should be writable by the nginx runtime identity model
  chown -R "${NGINX_USER}:${NGINX_GROUP}" "$NGINX_CACHE_ROOT" || true
  chmod 0755 "$NGINX_CACHE_ROOT" \
             "$NGINX_CACHE_ROOT/php" \
             "$NGINX_CACHE_ROOT/proxy_micro" \
             "$NGINX_CACHE_ROOT/proxy_static" || true
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

  # nginx is usually provisioned/tested first; do not force-start it here
}

maybe_run_ngm_provision() {
  if [[ ! -f "$CFG_FILE" ]]; then
    echo "Config file not found: $CFG_FILE"
    echo "Skipping ngm provision init."
    return 0
  fi

  if "$BIN_DIR/ngm" -c "$CFG_FILE" help provision >/dev/null 2>&1; then
    "$BIN_DIR/ngm" -c "$CFG_FILE" provision init || true
  else
    echo "ngm provision init not available yet; skipping automated nginx bootstrap."
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
- This script assumes your existing config.yaml already matches that layout.
- SELinux adjustments were applied for content under /home/<user>/sites.
- If DNS_ENABLED=false, bind was still installed, but named was not enabled.

Suggested checks:
  "$BIN_DIR/ngm" -c "$CFG_FILE" help
  "$BIN_DIR/ngm" -c "$CFG_FILE" provision test
  systemctl status $PHP_SERVICE
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
  configure_selinux_for_home_sites
  enable_services
  maybe_run_ngm_provision
  print_next_steps
}

main "$@"
