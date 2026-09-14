# Contributing

## Scope comes first

This package does exactly one thing: it keeps a SLURM worker node from
running jobs while a temperature sensor is over the limit configured for
its mainboard. It is a safety interlock, not a monitoring tool. Pull
requests that turn it into a temperature exporter, add metrics or an HTTP
endpoint, grow a second trigger condition (fan speed, power, ECC errors),
or make the guard itself do the killing will be declined. Temperature
data belongs in node_exporter's `hwmon` collector, which reads the same
sysfs attributes and is presumably already scraping these nodes.

Adding a mainboard to `packaging/config/boards.conf` is the most useful
contribution there is, and it needs no code. Include in the commit body
the board name as the kernel reports it, the `slurm-temperature-check
--list` output for the node, and why that sensor and that limit.

Two properties are load-bearing and changes have to keep them:

- **Every failure takes the node out of service, and how far follows what
  the node was promised.** An unreadable sensor, an unparseable reading
  and a wedged check loop end with the job steps killed, because the node
  was being watched and is not any more. An unknown board, an absent
  chip, a table that does not parse and a bad flag end with the node
  drained and the jobs already running on it left to finish, because the
  guard never took a reading and its failing to start changed nothing
  about the node's temperature. A change that makes any failure keep the
  node in service needs to argue why a temperature that is not known is
  safe; a change that moves a failure from the second response to the
  first needs to say what promise it withdrew.
- **The guard holds no privilege.** It reads world-readable sysfs and
  exits with a status. Everything that can kill a process lives in
  `slurm-temperature-check-emergency-stop.service` and everything that
  can change the node's state in SLURM lives in
  `slurm-temperature-check-drain.service`; the commands of both are fixed
  in the unit files and take no input from the configuration, the sensors
  or anything else. Which of the two runs is decided from a single input,
  the guard unit's exit status as systemd reports it.

## No dependencies

`go.mod` carries no `require` block and there is no `go.sum`. CI fails if
either changes. A safety interlock that runs on every worker node should
not be able to break because a third-party module did, and everything
this program needs — sysfs, a strict `key = value` parser, `log/slog`,
the three sd_notify messages — is in the standard library.

Dependabot therefore opens weekly, grouped pull requests for the workflow
actions only (`.github/dependabot.yml`); its generated commit messages
are exempt from the commit-message checks below.

## Commit messages

Every commit follows [Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <description>` with types
`feat fix docs test refactor perf build ci chore revert` and scopes
`guard hwmon config systemd sysusers rpm ci docs scripts deps`. Subject in
the imperative mood, lower case after the type, no trailing period, at
most 72 characters. Only the first word is held to lower case: an
identifier keeps the spelling the code gives it, so `read Tctl rather
than temp1` is right and `read tctl` is not. Prefer a bullet-point body, one bullet per discrete
change or rationale, over prose. Breaking changes use `!` and a
`BREAKING CHANGE:` footer.

Commits must state self-contained facts. A message has to remain fully
intelligible to someone reading `git log` years from now with no access
to any surrounding context: state what changed and why in terms of the
files and the system, never in terms of the process that produced the
change. Do not reference conversations, review rounds, tickets that may
become unreachable, relative time ("yesterday", "the previous commit")
or the author's working state. If a bug motivated the change, describe
its observable symptom and mechanism in the body. Issue/PR numbers may
be added as trailers for convenience, but the message must stand alone
if those links die. Commit messages must not reference AI tooling: no
co-author trailers, no generation notes.

CI enforces structure with commitlint (`.commitlintrc.yml`) and the
context rule heuristically with `scripts/check-commit-context.sh`, both
over the full PR commit range. Run the same checks locally per commit by
opting into the shipped hook:

```console
$ git config core.hooksPath .githooks
```

## Merge policy: signed commits, fast-forward only

`main` accepts only signed commits, and neither force pushes nor
deletion (repository rulesets: *Require signed commits*, *Block force
pushes*, *Restrict deletions*; keep them across maintainer changes).
Sign your commits with GPG or SSH (`git config commit.gpgsign true`).
Unsigned commits cannot reach `main` at all.

Pull requests are merged by fast-forwarding `main` to the PR head, never
by squash or merge commits. Squash merging would discard the individual
commit messages that commit linting exists to protect, and a merge
commit would break the linear history; keep both buttons disabled in the
repository settings. GitHub's "Rebase and merge" button is unusable as
well: it rewrites every commit with GitHub as the committer, which drops
the authors' signatures, so GitHub refuses it on a branch that requires
signed commits. A maintainer merges from a clone instead:

```console
$ gh pr checkout <number>            # CI green, every commit signed
$ git switch main
$ git merge --ff-only @{-1}          # fast-forward to the PR head
$ git push origin main
```

GitHub marks the pull request as merged once `main` contains its head
commit. If `main` moved after the PR was last rebased, rebase the PR
branch and push it first; the person rebasing re-signs the commits.
Every commit therefore reaches `main` verbatim and each linted message
survives into permanent history. A PR title check is deliberately
absent, because titles never reach `main`.

The corollary: every commit in a PR is a public commit and must
independently satisfy the rules above and build cleanly. Clean up your
branch with an interactive rebase before requesting review; do not append
"fix typo" commits.

## Tests

`README.md`'s "Expected behaviour" table is the specification. Its first
ten rows are `TestTruthTable` in `internal/guard/guard_test.go` row for
row, and the last three are `TestCannotArm`,
`TestDisableFileOutranksArming` and `TestRearmWhenResumed` in
`cmd/slurm-temperature-check/main_test.go`. A change to the table or to
any of those tests has to change both, in the same commit.

Which of the two responses a failure gets is decided by the exit status
alone, which makes the exit statuses a specification of their own:
`TestExitCodeSeparatesArmingFromTripping` holds exit 2 to "never took a
reading" and exit 1 to everything after that, and CI exercises the
shipped classifier case by case in its "Which failure goes to which unit"
step. A change to either side needs the other.

Run the suite with `go test -race ./...`. The fixtures build fabricated
sysfs trees in a temporary directory rather than committing captured
ones, because none of the boards in the table are available to capture
from; `TestChipNames` pins the composed chip names to the names the
deployed board table already uses, which is the closest thing to a
capture there is. `TestShippedTableParses` reads
`packaging/config/boards.conf` itself, so a broken table fails the unit
tests and not only the package build.

CI additionally runs `gofmt`, `go vet`, golangci-lint and, over the
shipped files, `shellcheck`, `systemd-sysusers --dry-run`,
`systemd-analyze verify` of all three units and the `slurmd` drop-in
(against a stand-in `slurmd.service`), and `systemd-analyze security`
with a threshold on the guard. The emergency stop and the drain are
deliberately not scored: they have to run as root and reach systemd's
private bus, and the drain the network besides, which caps what their
scores can be, so their hardening is reviewed by reading the units.

The RPM is built in a Rocky 9 container (required) and in Rocky 10 and
Fedora (advisory), rebuilt from its SRPM inside a network namespace with
no interfaces so the build has to be self-contained and offline, passed
through the rpmlint policy check (`scripts/run-rpmlint.sh`), then
installed, exercised end to end against a fabricated sysfs tree matching
the shipped table's `TESTBOARD` entry, and removed again.

What CI cannot do is heat up a mainboard. Any change to the guard, the
units or the scriptlets therefore needs the README's
[Verifying it works](README.md#verifying-it-works) procedure run on a
real, drained node — including a reboot — and the commit body says so.

## Releases

Semantic versioning, `CHANGELOG.md` in Keep a Changelog format. A release
is a `vX.Y.Z` tag: the release workflow verifies tag, spec `Version:` and
the newest CHANGELOG entry agree, then builds and attaches the RPM, SRPM
and `SHA256SUMS` to the GitHub release. Bump `Version:` and add the spec
`%changelog` entry in the same commit as the CHANGELOG entry.

Upgrades deliberately do not restart the guard: `%postun` uses
`%systemd_postun` and not `%systemd_postun_with_restart`. A restart
inside a transaction would stop a running guard, and any failure on the
way back up — a board missing from a newly shipped table, a sensor that
moved — would land in the failed state, which drains the node where the
new guard cannot arm and kills its jobs where it armed and then tripped.
An upgrade must never be able to do either on its own. A running guard therefore keeps
the old binary until someone restarts it deliberately on a drained node,
or until the next reboot. A release whose changes only take effect after
such a restart says so in its CHANGELOG entry.
