#!/usr/bin/env bash
# The steps of the GitHub Action. action.yml runs them one at a time:
#
#   action.sh restore   bring back the store an earlier run saved
#   action.sh sync      read what changed on GitHub, Linear and Jira into the store
#   action.sh share     decide what may leave the runner
#   action.sh report    write the report and the outputs
#
# Inputs arrive in FM_* variables, never in the script text, so an input
# cannot run as shell. It runs on bash 3.2, which is what macOS runners have.
set -euo pipefail

fm="${FM_BIN:-$RUNNER_TEMP/factorymeter}"
store="$RUNNER_TEMP/factorymeter-store"
out="$RUNNER_TEMP/factorymeter-report"

target="${FM_TARGET#https://github.com/}"
target="${target%/}"
owner="${target%%/*}"

lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# One line per output. Line breaks are removed so a value cannot add an output
# of its own.
output() { printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr '\r\n' '  ')" >> "$GITHUB_OUTPUT"; }

warn() { echo "::warning::factorymeter: $1"; }

# One store per owner, so the artifact carries the owner in its name.
artifact_name() {
  printf '%s-%s' "${FM_STORE_NAME:-factorymeter-store}" "$(lower "$owner" | tr -cd 'a-z0-9._-')"
}

fetch() {
  local name="$1" run held
  rm -rf "$store"
  if [ "${FM_STORE:-true}" != "true" ]; then
    echo "The store is not kept between runs. This run reads the whole history."
    return 1
  fi
  if ! command -v gh > /dev/null; then
    warn "the GitHub CLI is not on this runner, so the store of an earlier run cannot be restored. This run reads the whole history."
    return 1
  fi
  # The newest store that a run of this repository's own code saved. A pull
  # request from a fork can upload an artifact under any name, so the
  # artifacts of such a run are never taken.
  if ! run="$(gh api -X GET "repos/$GITHUB_REPOSITORY/actions/artifacts" -f name="$name" -F per_page=100 \
    --jq '[.artifacts[] | select(.expired | not) | select(.workflow_run.repository_id != null and .workflow_run.head_repository_id == .workflow_run.repository_id)] | sort_by(.created_at) | last | .workflow_run.id // empty')"; then
    warn "could not list the artifacts of $GITHUB_REPOSITORY. The job needs the permission actions: read. This run reads the whole history."
    return 1
  fi
  if [ -z "$run" ]; then
    echo "No store from an earlier run. This run reads the whole history."
    return 1
  fi
  if ! gh run download "$run" --repo "$GITHUB_REPOSITORY" --name "$name" --dir "$store"; then
    warn "could not download the store that run $run saved. This run reads the whole history."
    return 1
  fi
  rm -f "$store/store.lock"
  # A store from a newer version, or a damaged one, is not worth keeping.
  if ! held="$("$fm" store info "$owner" --store "$store" --format json | jq -r '.owner')"; then
    warn "this version cannot read the store that run $run saved. This run reads the whole history."
    return 1
  fi
  if [ "$(lower "$held")" != "$(lower "$owner")" ]; then
    warn "the store that run $run saved holds $held, not $owner. This run reads the whole history."
    return 1
  fi
  echo "Restored the store that run $run saved."
  "$fm" store info "$owner" --store "$store" || true
}

do_restore() {
  local name
  name="$(artifact_name)"
  output name "$name"
  if fetch "$name"; then
    output restored true
  else
    rm -rf "$store"
    output restored false
  fi
}

# Linear is read when the workflow gives a key. The key stays in
# LINEAR_API_KEY, where the binary reads it, and is never an argument. The
# teams are split without globbing, so a team cannot name files.
#
# Jira is read when the workflow gives a site, an email and a token. The email
# and the token stay in JIRA_EMAIL and JIRA_API_TOKEN, where the binary reads
# them. The site is not a secret and is an argument. With a part of the three
# missing, as for a pull request from a fork, Jira is not read.
do_sync() {
  local log="$RUNNER_TEMP/factorymeter-sync.log" team project missing=""
  local args=(sync "$target" --store "$store" --since "${FM_HISTORY:-26w}")
  if [ -n "${LINEAR_API_KEY:-}" ]; then
    args+=(--linear)
    set -f
    for team in $(printf '%s' "${FM_LINEAR_TEAMS:-}" | tr ',\r\n\t' '    '); do
      args+=(--linear-team "$team")
    done
    set +f
  elif [ -n "${FM_LINEAR_TEAMS:-}" ]; then
    warn "linear-teams is set and linear-api-key is empty, so Linear is not read."
  fi
  [ -n "${FM_JIRA_SITE:-}" ] || missing="$missing jira-site"
  [ -n "${JIRA_EMAIL:-}" ] || missing="$missing jira-email"
  [ -n "${JIRA_API_TOKEN:-}" ] || missing="$missing jira-api-token"
  if [ -z "$missing" ]; then
    args+=(--jira "$FM_JIRA_SITE")
    set -f
    for project in $(printf '%s' "${FM_JIRA_PROJECTS:-}" | tr ',\r\n\t' '    '); do
      args+=(--jira-project "$project")
    done
    set +f
  elif [ -n "${FM_JIRA_SITE:-}${JIRA_EMAIL:-}${JIRA_API_TOKEN:-}${FM_JIRA_PROJECTS:-}" ]; then
    warn "Jira needs jira-site, jira-email and jira-api-token. Empty:$missing. So Jira is not read."
  fi
  "$fm" "${args[@]}" | tee "$log"
  output requests "$(sed -n 's/^\([0-9][0-9]*\) requests to GitHub.*/\1/p' "$log" | tail -n 1)"
}

# Whether the repository the workflow runs in is private. Anything but a
# clear yes counts as public.
repo_is_private() {
  local v=""
  if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; then
    v="$(jq -r '.repository.private | tostring' "$GITHUB_EVENT_PATH" 2> /dev/null || true)"
  fi
  case "$v" in
    true | false) ;;
    *) v="$(gh api "repos/$GITHUB_REPOSITORY" --jq '.private' 2> /dev/null || true)" ;;
  esac
  [ "$v" = "true" ]
}

# The artifacts and the job summary of a public repository can be read by
# anyone. A store that holds a private repository carries its pull request
# titles and logins, and one that holds tickets of Linear or Jira carries
# their titles. Such tickets always count as private. So in a public
# repository such a store stays on the runner and the report is written
# without names.
do_share() {
  local names=true save=false private
  if [ -f "$store/store.json" ]; then
    private="$("$fm" store info "$owner" --store "$store" --format json | jq -r '[.scopes[].private] | any' || true)"
    if [ "$private" != "false" ] && ! repo_is_private; then
      names=false
      warn "$GITHUB_REPOSITORY is public and the store holds a private repository or tickets of Linear or Jira. The report is written without names, and the store is not saved, so every run reads the whole history. Run the workflow in a private repository to keep both."
    elif [ "${FM_STORE:-true}" = "true" ]; then
      save=true
    fi
  fi
  output names "$names"
  output save "$save"
}

do_report() {
  local json="$out/factorymeter.json"
  mkdir -p "$out"
  set -- report "$target" --store "$store" --since "${FM_SINCE:-13w}" --format markdown \
    --html "$out/factorymeter.html" --json "$json"
  # Names are written only on a clear yes from the share step.
  if [ "${FM_NAMES:-}" != "true" ]; then
    set -- "$@" --summary
  fi
  "$fm" "$@" > "$out/factorymeter.md"
  if [ "${FM_SUMMARY:-true}" = "true" ]; then
    cat "$out/factorymeter.md" >> "$GITHUB_STEP_SUMMARY"
  fi
  one() { jq -r "$1 // \"\" | tostring" "$json"; }
  output headline "$(one .verdict.headline)"
  output constraint "$(one .verdict.constraint)"
  output constraint-share "$(one '[.stations[] | select(.constraint) | .held_share][0]')"
  output agent-share "$(one '[.metrics[] | select(.metric == "line.agent_share" and .slice.class == "" and .slice.agent == "" and .slice.repo == "") | .value][0]')"
  output recommendation "$(one '.recommendations[0].title')"
  output html "$out/factorymeter.html"
  output json "$json"
  output markdown "$out/factorymeter.md"
  output store "$store"
}

case "${1:-}" in
  restore) do_restore ;;
  sync) do_sync ;;
  share) do_share ;;
  report) do_report ;;
  *)
    echo "usage: action.sh restore | sync | share | report" >&2
    exit 2
    ;;
esac
