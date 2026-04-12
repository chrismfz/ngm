Name:           ngm
Version:        2026.04.12
Release:        1.152106%{?dist}
Summary:        NGM – Nginx Go Manager
License:        MIT
URL:            https://infected.gr
BuildArch:      %{_arch}
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
NGM (Nginx Go Manager) – Go control-plane for a custom Nginx install.
Manages domains/vhosts, certificates, PHP-FPM pools, and safe atomic
nginx config apply with rollback.

%prep
# nothing – binary is pre-built and staged into pkgroot by make

%build
# nothing

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}

if [ -d "%{pkgroot}/usr" ]; then
  cp -a "%{pkgroot}/usr" "%{buildroot}/"
fi
if [ -d "%{pkgroot}/etc" ]; then
  cp -a "%{pkgroot}/etc" "%{buildroot}/"
fi

# Normalise systemd unit path: move from /lib if it ended up there
if [ -f "%{buildroot}/lib/systemd/system/ngm.service" ]; then
  mkdir -p "%{buildroot}%{_unitdir}"
  mv "%{buildroot}/lib/systemd/system/ngm.service" "%{buildroot}%{_unitdir}/"
  rm -rf "%{buildroot}/lib/systemd"
fi

%files
%{_bindir}/ngm
%{_unitdir}/ngm.service
%config(noreplace) /etc/ngm/config.yaml
%dir %{_datadir}/ngm
%dir %{_datadir}/ngm/configs
%{_datadir}/ngm/configs/*

%post
systemctl daemon-reload || true

# If already installed and running, restart to pick up new binary
if [ "$1" -ge 2 ]; then
  # upgrade
  if systemctl is-active --quiet ngm.service; then
    echo "→ Upgrade: restarting ngm..."
    systemctl restart ngm.service || true
  fi
else
  # fresh install: enable but don't auto-start (let the admin decide)
  systemctl enable ngm.service || true
  echo "✅ ngm installed. Start with: systemctl start ngm"
fi

%preun
%systemd_preun ngm.service

%postun
%systemd_postun_with_restart ngm.service

%changelog
* Sun Apr 12 2026 Chris <chris@nixpal.com> - 2026.04.12-1
- Removed CFM-specific %post logic; clean rpm-only packaging
* Tue Sep 02 2025 Chris <chris@nixpal.com> - 0.0.0-1
- Initial RPM packaging
