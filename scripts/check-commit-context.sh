#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH
#
# Guard for the self-contained-commit-message rule: a commit message must
# stay fully intelligible to someone reading `git log` years from now, so
# it must not reference conversations, review rounds, relative time or
# other context that will be unavailable then. commitlint checks structure;
# this script heuristically catches the common context-reference lapses.
#
# Usage:
#   check-commit-context.sh <rev-range>          # e.g. origin/main..HEAD
#   check-commit-context.sh --message-file <f>   # commit-msg hook mode
set -euo pipefail

# Forbidden phrasings, case-insensitive. Deliberately a heuristic: it
# catches the common lapses, not every possible one.
pattern='as discussed|as requested|per (review|feedback)|address(ing)? (review )?comments|see (the )?(linked )?(thread|discussion|chat)|from yesterday|earlier today|the previous commit|my last commit|revert my|as mentioned above|per the brief|as specified in the brief'

fail=0

check_message() {
  local label="$1" msg="$2" hits
  if hits="$(printf '%s\n' "$msg" | grep -inoE "$pattern")"; then
    echo "$label references context that will be unavailable to future readers:"
    printf '%s\n' "$hits" | sed 's/^/    /'
    fail=1
  fi
}

if [ "${1:-}" = "--message-file" ]; then
  [ $# -eq 2 ] || { echo "usage: $0 --message-file <file>" >&2; exit 2; }
  # Strip comment lines the way git commit does.
  check_message "commit message" "$(grep -v '^#' "$2" || true)"
else
  [ $# -eq 1 ] || { echo "usage: $0 <rev-range> | --message-file <file>" >&2; exit 2; }
  while read -r sha; do
    check_message "commit $sha" "$(git log -1 --format='%B' "$sha")"
  done < <(git rev-list --no-merges "$1")
fi

exit $fail
