# SPDX-License-Identifier: Apache-2.0
# Packaged following the Fedora Go and systemd packaging guidelines. The
# program has no dependencies of its own, so the build needs no vendor
# directory and no network: GOPROXY is forced off and the module graph is
# the module itself.

%global goipath         github.com/GSI-HPC/slurm-temperature-check
%global gomodulesmode   GO111MODULE=on
Version:                0.10.0

%gometa

%global common_description %{expand:
Keeps a SLURM worker node from running jobs while one of its temperature
sensors is over the limit configured for its mainboard. A small unprivileged
service reads the sensor from the kernel's hwmon interface in sysfs once per
interval and exits non-zero as soon as the node must stop; systemd turns that
exit into a separate, privileged unit that kills the running job steps and
stops slurmd.

Thresholds and the sensor to watch are per mainboard, matched on the DMI board
name, in /etc/slurm-temperature-check/boards.conf. Every failure mode is a
stop: an unreadable sensor, an unparseable reading, a board that is not in the
table and a check loop that stops turning all end with the node out of
service, because a temperature that is not known is not known to be safe.}

Name:           slurm-temperature-check
Release:        1%{?dist}
Summary:        Stop SLURM jobs on a worker node that exceeds its temperature limit

License:        Apache-2.0
URL:            %{gourl}
Source0:        %{gosource}
# The sysusers file is also in the tarball (packaging/systemd/), but %%pre has
# to read it at spec parse time, before %%prep unpacks anything, so it is a
# source of its own that the SRPM carries next to the tarball. A relative path
# into the tree resolves against rpmbuild's working directory instead, which
# silently leaves %%pre empty — and an empty %%pre means the service user is
# never created and the unit cannot start.
Source1:        %{name}.sysusers

BuildRequires:  golang >= 1.23
BuildRequires:  systemd-rpm-macros
%{?sysusers_requires_compat}
# EL9's own go-rpm-macros predate the current Fedora macros, so EPEL 9 ships
# them as an -epel overlay; EL10 and Fedora carry the current ones.
%if 0%{?fedora} || 0%{?rhel} >= 10
BuildRequires:  go-rpm-macros
%else
BuildRequires:  go-rpm-macros-epel
%endif

# The emergency stop is three systemctl calls, and the guard is a notify-type
# unit with a watchdog. Nothing else is needed at run time: the readings come
# from sysfs rather than from lm_sensors, and there is no interpreter.
Requires:       systemd

# slurm is deliberately not a dependency. The package installs cleanly on a
# node without it (the drop-in is inert and the emergency stop's kills are
# prefixed with "-"), which is what lets a node be prepared before slurmd is
# rolled out to it.

%description %{common_description}

%prep
# No -k: that flag preserves a committed vendor/ directory, and this tree has
# none to preserve. The build reaches the network for nothing, which the CI
# rebuild proves by running rpmbuild in a network namespace with no
# interfaces.
%goprep

%build
export GOPROXY=off
export LDFLAGS="-X main.version=%{version}-%{release}"
%gobuild -o %{gobuilddir}/bin/%{name} %{goipath}/cmd/%{name}

%install
install -D -m 0755 -vp %{gobuilddir}/bin/%{name} %{buildroot}%{_bindir}/%{name}
install -D -m 0644 -vp packaging/systemd/%{name}.service \
    %{buildroot}%{_unitdir}/%{name}.service
install -D -m 0644 -vp packaging/systemd/%{name}-emergency-stop.service \
    %{buildroot}%{_unitdir}/%{name}-emergency-stop.service
install -D -m 0644 -vp packaging/systemd/slurmd.service.d/temperature-check.conf \
    %{buildroot}%{_unitdir}/slurmd.service.d/temperature-check.conf
install -D -m 0644 -vp %{SOURCE1} \
    %{buildroot}%{_sysusersdir}/%{name}.conf
install -D -m 0644 -vp packaging/config/boards.conf \
    %{buildroot}%{_sysconfdir}/%{name}/boards.conf
install -D -m 0644 -vp packaging/sysconfig/%{name} \
    %{buildroot}%{_sysconfdir}/sysconfig/%{name}

%check
export GOPROXY=off
%gocheck

# On Fedora, rpm creates the user from the sysusers.d file itself and the
# compat macro is defined empty, which would leave an empty %%pre; EL9 and
# EL10 still create the user through the generated scriptlet.
%if 0%{?rhel}
%pre
%sysusers_create_compat %{SOURCE1}
%endif

%post
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

# Deliberately %%systemd_postun and not %%systemd_postun_with_restart.
#
# Restarting the guard as part of a transaction would stop a running guard,
# and a stop of the guard is indistinguishable to systemd from the guard
# having finished; worse, any failure on the way back up — a board that is not
# in a newly shipped table, a sensor that moved — lands in the failed state
# that arms the emergency stop and kills the jobs on the node. An upgrade must
# never be able to do that. The running guard therefore keeps the old binary
# until someone restarts it deliberately, which on a drained node is
# `systemctl restart slurm-temperature-check`, and otherwise happens at the
# next reboot.
#
# There is no %%postun at all because %%systemd_postun expands to nothing on
# every supported target: the daemon-reload it used to perform is done by the
# systemd package's RPM file trigger at the end of the transaction.

%files
%license LICENSE NOTICE
%doc README.md CHANGELOG.md
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%{_unitdir}/%{name}-emergency-stop.service
%dir %{_unitdir}/slurmd.service.d
%{_unitdir}/slurmd.service.d/temperature-check.conf
%{_sysusersdir}/%{name}.conf
%dir %{_sysconfdir}/%{name}
%config(noreplace) %{_sysconfdir}/%{name}/boards.conf
%config(noreplace) %{_sysconfdir}/sysconfig/%{name}

%changelog
* Sat Sep 12 2026 Dennis Klein <d.klein@gsi.de> - 0.10.0-1
- Single unprivileged service reading hwmon sysfs directly, replacing the
  reader/checker pair that communicated through a file in /run
- Emergency stop kills SLURM job steps by cgroup through systemctl rather
  than by walking a process tree
- Per-board sensor and threshold in one table, /etc/slurm-temperature-check/
  boards.conf
