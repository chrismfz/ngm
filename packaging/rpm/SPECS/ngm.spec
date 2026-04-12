Name:           ngm
Version:        2026.04.11
Release:        1.195524%{?dist}
Summary:        NGM
License:        MIT
URL:            https://infected.gr
BuildArch:      x86_64
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

%description
ngm

%prep
# nothing

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

if [ -f "%{buildroot}/lib/systemd/system/ngm.service" ]; then
  mkdir -p "%{buildroot}%{_unitdir}"
  mv "%{buildroot}/lib/systemd/system/ngm.service" "%{buildroot}%{_unitdir}/"
  rm -rf "%{buildroot}/lib/systemd"
fi




%files
%{_bindir}/ngm
%{_unitdir}/ngm.service
%config(noreplace) /etc/ngm/config.yaml

# shared examples (always overwritten on upgrade)
%dir %{_datadir}/ngm
%dir %{_datadir}/ngm/configs
%{_datadir}/ngm/configs/*


%post
# Ensure correct SELinux context in case older versions used /lib path
[ -f /lib/systemd/system/ngm.service ] && \
  chcon -h system_u:object_r:systemd_unit_file_t:s0 /lib/systemd/system/ngm.service || true

systemctl daemon-reload || true

# Step 1: check if running
was_active=0
if systemctl is-active --quiet ngm.service; then
    echo "ngm is currently running — stopping..."
    was_active=1
    systemctl stop ngm.service || true
fi

# Step 2: always disable (flush nftables)
if [ -x "%{_bindir}/ngm" ]; then
    echo "Flushing tables with: ngm disable"
    "%{_bindir}/ngm" disable || true
fi

# Step 3: if it was running, start again
if [ "$was_active" -eq 1 ]; then
    echo "ngm was active — starting it again..."
    systemctl start ngm.service || true
else
    echo "ngm was not running — leaving stopped."
fi


%preun
%systemd_preun ngm.service

%postun
%systemd_postun_with_restart ngm.service

%changelog
* Tue Sep 02 2025 Chris <chris@nixpal.com> - 0.0.0-1
- Initial RPM packaging
