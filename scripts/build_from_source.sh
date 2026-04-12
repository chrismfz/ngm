#!/bin/bash
set -euo pipefail

# --- CONFIGURATION ---
DOMAIN="quic.myip.gr"
EMAIL="admin@$DOMAIN"
MASTER_CONF_URL="https://raw.githubusercontent.com/chrismfz/mynginx/refs/heads/main/nginx.conf.master"

OPENRESTY_VER="1.27.1.2"
OPENRESTY_TGZ="openresty-${OPENRESTY_VER}.tar.gz"
OPENRESTY_URL="https://openresty.org/download/${OPENRESTY_TGZ}"

OPENSSL_VER="3.5.4"
OPENSSL_TGZ="openssl-${OPENSSL_VER}.tar.gz"
OPENSSL_URL="https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VER}/${OPENSSL_TGZ}"

# Paths
SRC_DIR="/usr/local/src"
OPENRESTY_PREFIX="/opt/openresty"
NGINX_PREFIX="${OPENRESTY_PREFIX}/nginx"
HTML_PATH="${NGINX_PREFIX}/html"

echo "--- 1. Installing Build Dependencies & Tools ---"
apt update
apt install -y \
  build-essential cmake ninja-build git curl wget perl golang-go gpg lsb-release ca-certificates apt-transport-https \
  zlib1g-dev libpcre2-dev certbot net-tools sudo acl \
  libreadline-dev libunwind-dev patch

echo "--- 2. Preparing filesystem layout ---"
mkdir -p "${SRC_DIR}"
mkdir -p "${NGINX_PREFIX}/conf" "${NGINX_PREFIX}/conf/sites"
mkdir -p "${NGINX_PREFIX}/logs"
mkdir -p "${NGINX_PREFIX}/cache" \
         "${NGINX_PREFIX}/cache/proxy_micro" \
         "${NGINX_PREFIX}/cache/proxy_static" \
         "${NGINX_PREFIX}/cache/fastcgi"
mkdir -p "${HTML_PATH}"

chown -R www-data:www-data "${NGINX_PREFIX}/logs" "${NGINX_PREFIX}/cache"

echo "--- 3. Clone/Update ngx_brotli ---"
cd "${SRC_DIR}"
if [ -d "ngx_brotli" ]; then
  echo "Updating ngx_brotli..."
  cd ngx_brotli
  git pull
  git submodule update --init --recursive
else
  echo "Cloning ngx_brotli..."
  git clone --recursive https://github.com/google/ngx_brotli.git
fi






echo "--- 2.1 Self-signed cert for global QUIC reuseport listener ---"
SELFSIGNED_DIR="${NGINX_PREFIX}/conf/selfsigned"
mkdir -p "${SELFSIGNED_DIR}"

# Keep the private key readable by nginx worker user via group, but not world-readable
chown -R root:www-data "${SELFSIGNED_DIR}"
chmod 0750 "${SELFSIGNED_DIR}"

# Only generate if missing (so reruns don't rotate certs unexpectedly)
if [ ! -f "${SELFSIGNED_DIR}/privkey.pem" ] || [ ! -f "${SELFSIGNED_DIR}/fullchain.pem" ]; then
  umask 027

  # ECDSA P-256 (fast, TLS1.3-friendly)
  openssl req -x509 -newkey ec \
    -pkeyopt ec_paramgen_curve:prime256v1 \
    -nodes \
    -keyout "${SELFSIGNED_DIR}/privkey.pem" \
    -out "${SELFSIGNED_DIR}/fullchain.pem" \
    -days 3650 \
    -subj "/CN=localhost" \
    -addext "subjectAltName=DNS:localhost"

  chown root:www-data "${SELFSIGNED_DIR}/privkey.pem" "${SELFSIGNED_DIR}/fullchain.pem"
  chmod 0640 "${SELFSIGNED_DIR}/privkey.pem"
  chmod 0644 "${SELFSIGNED_DIR}/fullchain.pem"
fi








echo "--- 3.5 Build Brotli (static libs) for ngx_brotli ---"
cd "${SRC_DIR}/ngx_brotli/deps/brotli"
rm -rf out
mkdir -p out
cd out
cmake .. -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF
cmake --build . --config Release --target brotlienc brotllicommon brotlidec || cmake --build . --config Release --target brotlienc brotlicommon brotlidec

# Fail fast if libs are missing
if [ ! -f "${SRC_DIR}/ngx_brotli/deps/brotli/out/libbrotlienc.a" ] || \
   [ ! -f "${SRC_DIR}/ngx_brotli/deps/brotli/out/libbrotlicommon.a" ]; then
  echo "ERROR: Brotli static libs not found where expected:"
  ls -la "${SRC_DIR}/ngx_brotli/deps/brotli/out" || true
  exit 1
fi

echo "--- 4. Download OpenResty ${OPENRESTY_VER} ---"
cd "${SRC_DIR}"
if [ ! -f "${OPENRESTY_TGZ}" ]; then
  wget -O "${OPENRESTY_TGZ}" "${OPENRESTY_URL}"
fi
rm -rf "openresty-${OPENRESTY_VER}"
tar -xzf "${OPENRESTY_TGZ}"

echo "--- 5. Download OpenSSL ${OPENSSL_VER} ---"
cd "${SRC_DIR}"

rm -f "${OPENSSL_TGZ}"
rm -rf "openssl-${OPENSSL_VER}"

curl -fL --retry 5 --retry-delay 2 -o "${OPENSSL_TGZ}" "${OPENSSL_URL}"

if ! file "${OPENSSL_TGZ}" | grep -qi 'gzip compressed'; then
  echo "ERROR: ${OPENSSL_TGZ} is not a gzip tarball (download likely failed)."
  echo "First 5 lines:"
  head -n 5 "${OPENSSL_TGZ}" || true
  exit 1
fi

tar -xzf "${OPENSSL_TGZ}"

echo "--- 6. Build OpenResty (OpenSSL ${OPENSSL_VER} QUIC + brotli) ---"
cd "${SRC_DIR}/openresty-${OPENRESTY_VER}"

./configure \
  --prefix="${OPENRESTY_PREFIX}" \
  --with-pcre-jit \
  --with-threads \
  --with-file-aio \
  --with-http_ssl_module \
  --with-http_v2_module \
  --with-http_v3_module \
  --with-http_realip_module \
  --with-http_stub_status_module \
  --with-stream \
  --with-stream_ssl_module \
  --with-stream_realip_module \
  --with-stream_ssl_preread_module \
  --with-http_gzip_static_module \
  --add-module="${SRC_DIR}/ngx_brotli" \
  --with-openssl="${SRC_DIR}/openssl-${OPENSSL_VER}" \
  --with-openssl-opt="enable-quic no-shared no-tests" \
  --with-cc-opt="-I${SRC_DIR}/ngx_brotli/deps/brotli/c/include -Wno-error=sign-compare -Wno-sign-compare"


make -j"$(nproc)"
make install

echo "--- 7. Install master nginx.conf template ---"
curl -sL "${MASTER_CONF_URL}" -o "${NGINX_PREFIX}/conf/nginx.conf"

chown -R www-data:www-data "${NGINX_PREFIX}/logs" "${NGINX_PREFIX}/cache"

echo "--- 8. Systemd Unit (OpenResty) ---"
cat > /etc/systemd/system/openresty.service <<EOF
[Unit]
Description=OpenResty (custom build)
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
PIDFile=${NGINX_PREFIX}/logs/nginx.pid
ExecStartPre=${NGINX_PREFIX}/sbin/nginx -t -c ${NGINX_PREFIX}/conf/nginx.conf
ExecStart=${NGINX_PREFIX}/sbin/nginx -c ${NGINX_PREFIX}/conf/nginx.conf
ExecReload=${NGINX_PREFIX}/sbin/nginx -s reload -c ${NGINX_PREFIX}/conf/nginx.conf
ExecStop=/bin/kill -s QUIT \$MAINPID
PrivateTmp=true
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

echo "--- 9. Logrotate ---"
cat > /etc/logrotate.d/openresty-custom <<EOF
${NGINX_PREFIX}/logs/*.log {
    daily
    rotate 14
    compress
    notifempty
    create 0640 www-data www-data
    sharedscripts
    postrotate
        [ -f ${NGINX_PREFIX}/logs/nginx.pid ] && kill -USR1 \$(cat ${NGINX_PREFIX}/logs/nginx.pid)
    endscript
}
EOF

echo "--- 10. Install PHP 8.3 (Sury) ---"
apt-get update
apt-get install -y lsb-release ca-certificates apt-transport-https curl
curl -sSLo /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
dpkg -i /tmp/debsuryorg-archive-keyring.deb
sh -c 'echo "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ $(lsb_release -sc) main" > /etc/apt/sources.list.d/php.list'
apt-get update

apt install --no-install-recommends -y \
  php8.3-bcmath php8.3-bz2 php8.3-cli php8.3-common php8.3-curl php8.3-decimal php8.3-enchant php8.3-fpm php8.3-gd php8.3-grpc \
  php8.3-igbinary php8.3-imagick php8.3-imap php8.3-inotify php8.3-lz4 php8.3-mailparse php8.3-maxminddb php8.3-mbstring \
  php8.3-mcrypt php8.3-memcache php8.3-memcached php8.3-mysql php8.3-opcache php8.3-protobuf php8.3-redis php8.3-rrd \
  php8.3-soap php8.3-sqlite3 php8.3-tidy php8.3-uploadprogress php8.3-uuid php8.3-xml php8.3-xmlrpc php8.3-yaml \
  php8.3-zip php8.3-zstd

echo "--- 11. Enable OpenResty ---"
systemctl daemon-reload
systemctl enable --now openresty

echo "--- DEPLOYMENT COMPLETE ---"
"${NGINX_PREFIX}/sbin/nginx" -V 2>&1

