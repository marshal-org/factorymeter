#!/usr/bin/env bash
# Run factorymeter against real systems: a GitHub repository, a Linear
# workspace and a Jira Cloud site kept for testing. It is not part of
# "go test ./..." or of CI. See test/live/README.md.
#
#   scripts/live-test.sh                 every live test
#   scripts/live-test.sh -run Releases   only the install script and upgrade
#
# Settings come from the environment, or from .env.live at the root of the
# repository (ignored by git). test/live/env.example lists them.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ -f .env.live ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env.live
  set +a
fi
exec go test -tags live -count=1 -timeout 30m -v ./test/live "$@"
