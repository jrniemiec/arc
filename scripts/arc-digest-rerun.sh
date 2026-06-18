#!/usr/bin/env bash
set -euo pipefail

# ---- globals / defaults ------------------------------------------------------
PROG="${0##*/}"

ARC="${ARC:-arc}"
ARC_AGENT_DIR="${ARC_AGENT_DIR:-$HOME/.arc/agent}"

RUN_ID=""
DEBUG=false
DRY_RUN=false
QUIET=false

# ---- helpers -----------------------------------------------------------------
RED="\033[31m"
YELLOW="\033[33m"
RESET="\033[0m"

log() { $QUIET || printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
dbg() { $DEBUG && printf "${YELLOW}DEBUG:${RESET} %s\n" "$*" >&2 || true; }
die() { printf "${RED}ERROR:${RESET} %s\n" "$*" >&2; exit 1; }

# ---- usage -------------------------------------------------------------------
usage() {
  cat <<EOF
Usage: $PROG [options] [run-id]

Process the decisions file for a given run ID.
Edit the decisions file first — change action:"-" to action:"+" on items
you want to force-ingest, then run this script.
To also send a combined digest email, pass all relevant run IDs to arc-digest-email:
  arc-digest-email <daily-id> <rerun-id>

Arguments:
  [run-id]               Run ID to process (e.g. agent-20260617-181418)
                         Omit to use the last run.

Environment variables:
  ARC_ANTHROPIC_API_KEY  Anthropic API key (falls back to macOS Keychain)
  ARC                    Path to arc binary (default: arc)
  ARC_AGENT_DIR          Arc agent directory (default: ~/.arc/agent)

Options:
  -n, --dry-run    Print what would run, don't execute
  -q, --quiet      Suppress log output
  -D, --debug      Debug output
  -h, --help       Show this help

Examples:
  arc-digest-rerun agent-20260617-181418
  arc-digest-rerun                          # uses last run ID
  arc-digest-rerun --dry-run agent-20260617-181418
EOF
}

# ---- arg parsing -------------------------------------------------------------
parse_args() {
  while (($#)); do
    case "$1" in
      -n|--dry-run) DRY_RUN=true ;;
      -q|--quiet)   QUIET=true ;;
      -D|--debug)   DEBUG=true ;;
      -h|--help)    usage; exit 0 ;;
      --) shift; break ;;
      -*) die "unknown flag: $1 (try --help)" ;;
      *)  RUN_ID="$1" ;;
    esac
    shift
  done
}

# ---- cleanup / traps ---------------------------------------------------------
cleanup() { :; }
on_err() { die "command failed (line $1): $2"; }
trap cleanup EXIT
trap 'on_err $LINENO "$BASH_COMMAND"' ERR

# ---- main --------------------------------------------------------------------
main() {
  parse_args "$@"

  if $DEBUG; then
    PS4='+ ${BASH_SOURCE}:${LINENO}: '
    set -x
  fi

  # Resolve run ID — default to last run.
  if [[ -z "$RUN_ID" ]]; then
    RUN_ID=$("$ARC" agent log -n 1 | awk '{print $NF}')
    [[ -n "$RUN_ID" ]] || die "could not determine last run ID"
    log "using last run: $RUN_ID"
  fi

  DECISIONS_FILE="$ARC_AGENT_DIR/decisions-${RUN_ID}.json"
  [[ -f "$DECISIONS_FILE" ]] || die "decisions file not found: $DECISIONS_FILE"

  # Load API key from Keychain if not set in environment.
  if [[ -z "${ARC_ANTHROPIC_API_KEY:-}" ]]; then
    ARC_ANTHROPIC_API_KEY=$(security find-generic-password -a anthropic -s arc -w 2>/dev/null) \
      || die "API key not found. Set ARC_ANTHROPIC_API_KEY or run: security add-generic-password -a anthropic -s arc -w <key>"
    export ARC_ANTHROPIC_API_KEY
  fi

  log "processing decisions for run $RUN_ID..."
  if ! $DRY_RUN; then
    "$ARC" agent run --decisions "$DECISIONS_FILE"
  else
    log "[dry-run] would run: $ARC agent run --decisions $DECISIONS_FILE"
  fi
  log "decisions run done."
}

# ---- entrypoint --------------------------------------------------------------
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
  exit $?
fi
