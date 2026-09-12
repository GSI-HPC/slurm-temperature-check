# Changelog

All notable changes to this project are documented in this file. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Before this file begins

The package was written and put into service inside GSI well before it
was published here. Versions up to 0.9.3 exist only in an internal
repository and were never released publicly, so the first entry below
describes a rewrite of something no reader outside GSI has seen. That is
the reason it arrives without a predecessor to compare against.

The numbering continues the internal series instead of restarting at
0.1.0. The nodes already running the package carry those versions in
their RPM database, and a package numbered 0.1.0 would be older than what
is installed on them, so `dnf` would refuse it as a downgrade.

The Changed, Removed and Fixed entries of 0.10.0 are kept for the two
audiences they still serve: the operators upgrading the machines that run
the internal version, who need to know what changes under them, and
anyone later asking why the package is shaped the way it is. Read them as
history rather than as a diff against a public release.

## [Unreleased]

### Fixed

- The shipped board table pinned a single DIMM on the Dell board.
  `FRANMDCP07` named its sensor `spd5118-i2c-20-50`, a chip name in
  full, and a populated DDR5 slot registers an SPD hub of its own, so
  the hubs at `-20-51` and beyond were not selected at all. The limit is
  compared against the hottest of the *selected* sensors, so any other
  DIMM could sit above 60 °C indefinitely without tripping, with nothing
  in the log and `--check` reporting `ok` off the one watched hub. The
  entry now uses the driver prefix, as the dual-socket entries do, which
  watches every hub and compares the hottest of them. It also stops the
  entry depending on the i2c adapter number, which the kernel assigns at
  probe time.
- A sensor followed the `/sys/class/hwmon/hwmonN` index rather than the
  device behind it. The path was resolved once, when the guard armed,
  and read unchanged for the life of the process; the kernel allocates
  that number from an IDA that hands out the lowest free one, so a
  watched device that is unbound frees its number for whatever registers
  next. The guard then read the new device's temperature, compared it
  against a limit chosen for the old one and logged it under the old
  one's name. Nothing failed, so the retry budget was reset rather than
  spent, and a node whose sensor had gone away kept running jobs while
  reporting `ok`. Each reading is now confirmed against the chip name
  the sensor was selected as, and a mismatch is treated as an unreadable
  sensor: tolerated within the retry budget, then a trip.
- `--help` reported failure. The flag package returns its help request as
  a parse error, which fell into the branch that exits 2, so the usage
  went to stderr and the documented way to discover the flags — the
  sysconfig file points at it — came back empty from a pager or a grep
  and aborted any wrapper run under `set -e`. It exits 0 and prints to
  stdout now. A flag the program does not have is still a
  misconfiguration and still exits 2.
- The commit-message lint rejected any subject holding a capital letter
  anywhere in it. `subject-case: [2, always, lower-case]` is satisfied
  only when the subject equals its own lower-cased form, so no subject
  could name a sensor label, a bus or a distribution the way the code
  and the board table spell it — `Tctl`, `DMI`, `EL9` — and a
  contributor's choice was to misspell the identifier or not to name
  it. Only the first word is held to lower case now, which is what
  CONTRIBUTING.md described all along.

## [0.11.0] - 2026-09-12

Correctness fixes to the interlock itself. Four of them could leave a node
running jobs above its limit, and three could stop a node that was not too
hot. No configuration format, flag or exit code changes meaning, and the
`--check` output gains one line.

**These fixes do not take effect when the package is upgraded.** `%postun`
deliberately does not restart the guard, so a running guard keeps the old
binary until someone restarts it on a drained node, or until the node next
reboots. Plan that restart: until it happens, the node is still running the
behaviour described below.

**Check a locally modified `boards.conf` before that restart.** Composed chip
names are corrected in this release, so a `chip` that names a device in full
may now spell it differently:

| Device | Was | Now |
|---|---|---|
| PCI, bus other than 0 | `i350bb-pci-0001` | `i350bb-pci-8301` |
| ACPI | `acpitz-isa-0000` | `acpitz-acpi-0` |
| behind a class device (NVMe, thermal zone) | `nvme-virtual-0` | `nvme-pci-0300` |

Names on PCI bus 0 (`k10temp-pci-00c3`), platform devices
(`coretemp-isa-0000`) and I2C (`spd5118-i2c-20-50`) are unchanged, so every
entry in the shipped table still resolves. A table edited on the node is kept
as `%config(noreplace)` and is not: run `slurm-temperature-check --list` and
`--check` on such a node before restarting the guard, because a `chip` that
no longer resolves makes the guard refuse to arm, and a guard that cannot arm
arms the emergency stop. Using the bare driver prefix (`k10temp`) avoids the
question entirely and is the better form anyway.

An unmodified `boards.conf` is replaced by the packaged one, which picks up
the dual-socket corrections below; a modified one keeps whatever it has.

### Fixed

- A board entry naming its chip by driver prefix watched only one hwmon
  device per composed chip name. Two devices can compose to the same
  name — the name is built from the parent's bus address, and a driver
  whose parent sits on an unrecognised bus falls back to
  `<driver>-virtual-0` for every instance — and the second was dropped
  from the selection. Since the limit is compared against the hottest of
  the *selected* sensors, a dropped device could sit above the limit
  indefinitely without tripping, with nothing in the log and `--list`
  and `--check` both looking healthy. Selection is now keyed on the
  hwmon device.
- Composed chip names did not match the ones `sensors` prints, which the
  board table's `chip` key is documented to take. The PCI address left
  out the bus, so every device outside bus 0 was named as though it were
  on it (`amdgpu-pci-0000` for `0000:04:00.0`, where `sensors` says
  `amdgpu-pci-0400`), and only the first `device` link was followed, so a
  driver registering its hwmon device below a class device — `nvme`, an
  ACPI thermal zone — fell back to `<driver>-virtual-0`. Both made
  distinct devices compose one name, which is how the selection defect
  above became reachable. ACPI devices are also spelled `-acpi-0` now
  rather than `-isa-0000`.
- The shipped board table pinned a single chip on boards that have two.
  `H11DSi-NT`, `H12DSi-N6` and `MZB3-PE0-000` named one `k10temp` PCI
  function and `BC11SPSCA0` named `coretemp-isa-0000` with
  `sensor = Package id 0`, so on a populated second socket that socket
  was not watched at all: it could pass the limit while the guard kept
  reporting `ok` off the other one, and `--check` agreed. All four now
  use the driver prefix, which watches every socket and compares the
  hottest of them.
- The override file failed open on a value that is not a temperature.
  `strconv.ParseFloat` accepts `nan` and `inf`, and converting an
  out-of-range float to `int64` is implementation-defined — on x86-64
  every such value becomes the most negative `int64`, about
  -9.2e15 °C, which compares below any limit. So `echo nan >` the
  override file, or enough nines to overflow, made a node that was over
  its limit report `ok` and exit 0. Values that are not temperatures are
  now rejected, which spends the retry budget and then trips, as the
  specification's "not a number" rows require. The conversion to
  millidegrees also rounds rather than truncates.
- `max_celsius = nan` passed the 20–150 °C range check, because every
  comparison against NaN is false and the check asked whether the value
  was below the minimum or above the maximum. The limit then became
  about -9.2e15 °C, which every reading exceeds, so the node tripped on
  its first pass and the emergency stop killed its jobs. NaN is now
  rejected, and this threshold rounds to millidegrees rather than
  truncating as well.
- An override file taken into use was invisible. The journal suppressed
  repeated identical verdicts by comparing the verdict alone, and the
  verdict stays `ok` across the change from the sensors to the override,
  so nothing was logged at `info`; `--check` printed the armed hwmon
  sensors beside a reading that had not come from them; and the
  `systemctl status` line, set once at startup, went on naming the
  sensors. A node left with an override after a test therefore reported
  a number an operator typed, for as long as the file was there, and
  looked healthy from every angle. The source is now part of what
  suppression compares, taking an override into use is logged at
  `warn`, `--check` prints a `source` line, and the status line follows
  the loop.
- The `slurmd` drop-in carried `Wants=slurm-temperature-check.service`,
  which starts the guard whenever `slurmd` starts, whether or not the
  guard was ever enabled — an `[Install]` section has no say over a
  dependency another unit declares. On a node where the package was
  installed but the board was not in the table yet, which
  [Install](README.md#install) asks for explicitly, any `systemctl
  restart slurmd` or reboot therefore started a guard that exited 2,
  failed, and fired `OnFailure=`, killing every job step on a node
  nobody had enrolled. The drop-in now carries `After=` only, which is
  the ordering it always documented.
- The disable file was only consulted once the guard was already
  running, so a node the guard could not arm for — a board missing from
  the table, a sensor that had moved, a table that no longer parsed —
  exited 2 and fired the emergency stop on every start and every boot,
  and creating the disable file could not stop it. The file is now
  tested before arming, as specification row 1 always said it was: while
  it is present the guard waits, logs why it cannot arm, and arms itself
  within one interval of the file being removed. A `systemctl stop` in
  that state is still a clean stop.
- A sensor that was present but unreadable made the guard refuse to
  start, contradicting the "unreadable" and "not a number" rows of the
  specification, which absorb `--read-retries` failures before stopping.
  Arming took one reading and had no retry budget to spend, so a single
  transient bus error on a node that was still coming up exited 2 and
  fired the emergency stop, where the same error one interval later
  would have been tolerated twice. Arming no longer reads: the chip and
  attribute are still resolved, and the loop decides.
- The watchdog measured how long ago the check loop last completed a
  pass against the wall clock, which a `time.Time` rebuilt from a
  Unix timestamp follows because it carries no monotonic reading. A
  forward `CLOCK_REALTIME` step larger than the staleness window —
  chrony's `makestep` on a node whose RTC battery is dead, while the
  node is coming up — therefore withheld a keep-alive from a loop that
  was turning normally, and a backward step kept feeding the watchdog
  for a loop that had wedged. Progress is now a monotonic duration.
- The watchdog was documented as withholding the keep-alive after two
  `--interval`s; the window is the longer of two intervals and
  `WatchdogSec`, which is 60s and not 20s at the defaults, making wedge
  to kill 60–120s rather than the 20–80s the README implied. The
  keep-alive was also described as being sent from the check loop, where
  it comes from a ticker gated on the loop's progress. Advice to raise
  `WatchdogSec=` before raising `--interval`, in the unit, the sysconfig
  file and the README, was left over from that description and had it
  backwards: the window already follows `--interval`, and raising
  `WatchdogSec=` only slows detection down.
- `scripts/check-commit-context.sh` read its commit list from a process
  substitution, which `pipefail` does not observe, so a `git rev-list`
  that failed — an unreachable ref, a shallow clone without the base
  commit — fed the loop nothing and the script exited 0. A check that
  reports success having examined no commit at all now fails instead,
  and so does an empty range.

## [0.10.0] - 2026-09-12

First public release, and a reimplementation of the internal predecessor
as a single dependency-free binary. The interlock itself is unchanged — a
node over its mainboard's temperature limit stops running SLURM jobs —
but the program, its configuration format and the way the jobs are killed
are all new. For a node already running the internal version this is a
migration rather than an upgrade: the configuration is not read by the
new binary and has to be rewritten as a board table. Read
[Install](README.md#install) before rolling it out.

### Added

- `slurm-temperature-check`, one unprivileged `Type=notify` service that
  reads the mainboard's temperature sensor from the kernel's hwmon class
  in sysfs once per interval and exits non-zero when the node must stop
  running jobs. It holds no privilege: every attribute it reads is mode
  0444, its unit has an empty `CapabilityBoundingSet=`, no network, no
  devices and a system-call filter, and CI fails if a hardening directive
  is dropped.
- `slurm-temperature-check-emergency-stop.service`, the only privileged
  part, reachable along exactly one edge — the guard's `OnFailure=`. It
  kills the SLURM job steps by cgroup membership
  (`systemctl kill --kill-whom=all` on `slurmstepd.scope` and on
  `slurmd.service`, which covers both the cgroup/v2 scope layout of SLURM
  22.05+ and the older one) and then stops `slurmd`. Its commands are
  fixed in the unit file and take no input from the configuration or the
  sensors.
- `WatchdogSec=60` with the keep-alive gated on the check loop's
  progress, so a loop that stops turning is killed by systemd and lands
  in the failed state that arms the emergency stop. This is what replaces
  the previous design's staleness check on the file between the two
  processes.
- `--list`, which prints the node's chips, sensors, labels and live
  readings as the program computes them, and `--check`, which resolves the
  board table against the node and takes one reading without starting the
  guard. Both are meant to be run before the unit is enabled; a guard that
  cannot arm arms the emergency stop.
- One board table, `/etc/slurm-temperature-check/boards.conf`, holding the
  sensor *and* the threshold per mainboard. The parser refuses unknown
  keys, repeated sections, repeated keys, a missing threshold and a
  threshold outside 20–150 °C, because each of those would otherwise
  produce a node that looks guarded and is not.
- Multi-socket support: a bare driver prefix as the `chip` (`k10temp`
  rather than `k10temp-pci-00c3`) watches every chip of that driver and
  compares the hottest of them.
- CI: gofmt, go vet, golangci-lint and `go test -race`; `shellcheck`,
  `systemd-sysusers --dry-run`, `systemd-analyze verify` of both units and
  the `slurmd` drop-in, and a `systemd-analyze security` threshold on the
  guard; RPM builds on Rocky 9 (required) plus Rocky 10 and Fedora
  (advisory), each rebuilt from its SRPM inside a network namespace with
  no interfaces, checked by an rpmlint policy, then installed, exercised
  end to end against a fabricated sysfs tree and removed; commitlint and
  self-contained-commit-message enforcement; a release workflow producing
  RPM, SRPM and SHA256SUMS from a `vX.Y.Z` tag.
- Dependabot version updates for the workflow actions.

### Changed

- Readings come from sysfs directly instead of from a parsed `sensors`
  run, which removes the `lm_sensors` dependency, the per-reading
  subprocess and its timeout, and the regular expression over
  human-readable output. The previous expression, `(\d+)\.*\d+`, matched
  `450` as readily as `45.0` and worked only because a shell pipeline
  narrowed its input first.
- Thresholds are compared against the exact reading rather than a
  truncated integer. Readings are kept in the millidegrees the kernel
  reports, so a node at 85.4 °C against an 85 °C limit now stops where it
  previously did not.
- The guard and the sensor reader are one process. The previous pair
  communicated through `/run/temperature/temperature`, and that file was
  the sole cause of the partial-read handling, the atomic-write
  dependency, the error strings written where a number was expected and
  the staleness check.
- The `slurmd` drop-in carries `Wants=`/`After=` only. `BindsTo=` coupled
  `slurmd`'s lifecycle to the guard in both directions, so every stop of
  the guard — a package upgrade, a sensor repair — killed the jobs, and
  `systemctl restart slurmd` could not be done without losing the
  workload.
- Diagnostics go to the journal as structured `log/slog` records instead
  of to files under `/var/log/temperature`, so the logrotate and tmpfiles
  configuration is gone. Repeated identical verdicts are not re-logged;
  transitions and tolerated failures always are.
- The package is built from a release tarball by a single spec with a real
  `Source0`, rebuildable from its SRPM. The previous four spec files had
  drifted to three different versions and copied files out of `/vagrant`,
  so they only built inside a Vagrant VM.

### Removed

- `temperature-provider`, `temperature-provider.sh` and
  `get_slurm_pids`. The first two are replaced by reading sysfs; the third
  walked `pstree -p` output, which truncates at the terminal width unless
  given `-l`, so a job tree wider than that yielded truncated PIDs that
  belonged to unrelated processes and were then sent `SIGKILL`.
- The `python3`, `python3-PyYAML`, `python3-atomicwrites`, `lm_sensors`
  and `shadow-utils` dependencies. `python3-atomicwrites` is deprecated
  upstream, and the user is now created from a sysusers.d file.
- The `%post` scriptlet that moved the drop-ins to `/tmp`, restarted the
  services and moved them back, to keep an upgrade from killing the jobs.
  A transaction interrupted in the middle left the node running jobs with
  no thermal protection and nothing reporting it. Upgrades now do not
  restart the guard at all; see [Releases](CONTRIBUTING.md#releases).
- The CentOS 7, Rocky 8 and Vagrant build and test harnesses, and the
  2021–2022 design presentations under `doc/`. The state machine and the
  dependency structure they described are in `README.md`.

### Fixed

- A missing `temperature_file_maximum_age_seconds` or `board_filename` in
  the configuration raised an uncaught `KeyError`, which exited non-zero
  and so killed the jobs on the node. Configuration errors are now
  reported as messages naming the file, the line and the key.
- A fresh install shipped only `*.conf.default`, so the services had no
  configuration to read. A working board table is now installed as
  `%config(noreplace)`.
- The staleness check never applied to the override file, whose modification
  time was replaced with the current time on every pass, so the documented
  "override present but too old" case could not occur.

[Unreleased]: https://github.com/GSI-HPC/slurm-temperature-check/compare/v0.11.0...HEAD
[0.11.0]: https://github.com/GSI-HPC/slurm-temperature-check/releases/tag/v0.11.0
[0.10.0]: https://github.com/GSI-HPC/slurm-temperature-check/releases/tag/v0.10.0
