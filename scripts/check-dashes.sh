#!/usr/bin/env bash
# This project writes no em dashes and no en dashes, in code, docs or fixtures.
# Go sources spell them as escapes where a test needs one.
set -euo pipefail
cd "$(dirname "$0")/.."
if git ls-files -z | xargs -0 grep -nP '[\x{2013}\x{2014}]' --binary-files=without-match; then
  echo "Found a long dash. Use a comma, a colon or a full stop." >&2
  exit 1
fi
