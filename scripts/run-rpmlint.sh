#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH
#
# Run rpmlint over the given spec/SRPM/RPM files and fail on any finding
# that is not explicitly justified below. This keeps "rpmlint clean" an
# enforced property rather than an aspiration, across the rpmlint versions
# shipped by EL9, EL10 and Fedora.
set -euo pipefail
[ $# -ge 1 ] || { echo "usage: $0 <spec-srpm-or-rpm>..." >&2; exit 2; }

# Justified findings (extended regular expression, one alternative per
# line-comment):
# - spelling-error: rpmlint's dictionary trips over domain terms (hwmon,
#   sysfs, slurmd, systemd, cgroup, DMI, ...).
# - invalid-license: EL9's rpmlint predates Fedora's move to SPDX license
#   identifiers; `Apache-2.0` is the correct current form.
# - no-buildroot-tag: rpm has ignored `BuildRoot:` since 4.6 and the
#   Fedora/EL packaging guidelines forbid it; rpmlint still asks for it.
# - invalid-url Source0: rpmlint fetches the URL, which is the GitHub
#   archive of the tag being built. Every untagged commit CI builds 404s
#   there by construction; the release workflow verifies the tag itself.
# - only-non-binary-in-usr-lib: the systemd units and the sysusers file live
#   under /usr/lib by design; rpmlint 1.11 on EL9 only exempts a fixed list
#   of other /usr/lib subtrees.
# - unstripped-binary-or-object / statically-linked-binary: Go binaries are
#   statically linked and keep their symbol table, which the Go packaging
#   guidelines require for the debuginfo the %gobuild macro generates.
# - no-manual-page-for-binary: the binary documents itself through --help,
#   and README.md carries the operational documentation.
# - position-independent-executable-suggested: %gobuild builds with
#   -buildmode=pie on the architectures that support it; the finding fires
#   on EL9's rpmlint regardless.
allow='spelling-error|invalid-license|no-buildroot-tag|invalid-url Source0|only-non-binary-in-usr-lib|unstripped-binary-or-object|statically-linked-binary|no-manual-page-for-binary|position-independent-executable-suggested'

# One rpmlint run per file. rpmlint's spec checker keeps its section state
# across the files of a single run, so checking the spec file and then the
# SRPM (which carries the same spec) reports the second spec's preamble as
# a stray %changelog section.
fail=0
for f in "$@"; do
  rc=0
  out="$(rpmlint "$f" 2>&1)" || rc=$?
  printf '%s\n' "$out"
  # 0: clean or warnings only; 64/65: errors found; 66: badness threshold
  # exceeded. Anything else means rpmlint itself did not run (traceback,
  # bad invocation), which must not pass as "no findings".
  case "$rc" in
    0|64|65|66) ;;
    *) echo "rpmlint exited with status $rc on $f" >&2; fail=1; continue ;;
  esac
  bad="$(printf '%s\n' "$out" | grep -E ' [EW]: ' | grep -vE "$allow" || true)"
  if [ -n "$bad" ]; then
    echo >&2
    echo "unjustified rpmlint findings in $f (fix them or justify them in $0):" >&2
    printf '%s\n' "$bad" >&2
    fail=1
  fi
done
[ $fail -eq 0 ] || exit 1
echo "rpmlint: clean (or justified)"
