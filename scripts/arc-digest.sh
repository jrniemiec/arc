#!/usr/bin/env bash
set -euo pipefail

# ---- globals / defaults ------------------------------------------------------
PROG="${0##*/}"

ARC="${ARC:-arc}"
SECRETS_ENV="${SECRETS_ENV:-$HOME/.config/secrets.env}"

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
Usage: $PROG [options]

Run the arc feed agent. Intended to be called by launchd or cron at 6am.
To also send the digest email, chain with arc-digest-email:
  arc-digest && arc-digest-email

Environment variables:
  ARC_ANTHROPIC_API_KEY  Anthropic API key (loaded from ~/.config/secrets.env
                         when not already set in the environment)
  ARC                    Path to arc binary (default: arc)

Options:
  -n, --dry-run    Print what would run, don't execute
  -q, --quiet      Suppress log output
  -D, --debug      Debug output
  -h, --help       Show this help

Examples:
  arc-digest
  arc-digest --dry-run
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
      *)  die "unexpected argument: $1 (try --help)" ;;
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

  # Default ARC_HOME to ~/.ARC_ACTIVE_HOME symlink if not already set.
  if [[ -z "${ARC_HOME:-}" && -L "$HOME/.ARC_ACTIVE_HOME" ]]; then
    ARC_HOME=$(readlink -f "$HOME/.ARC_ACTIVE_HOME")
    export ARC_HOME
  fi

  # launchd invokes this via `bash -c` — a non-interactive shell, which never
  # reads .bashrc, so secrets.env is not loaded the way it is in a terminal.
  # It is the single source for API keys; there is deliberately no fallback.
  if [[ -r "$SECRETS_ENV" ]]; then
    # shellcheck source=/dev/null
    . "$SECRETS_ENV"
  fi
  export ARC_ANTHROPIC_API_KEY

  if [[ -z "${ARC_ANTHROPIC_API_KEY:-}" ]]; then
    die "ARC_ANTHROPIC_API_KEY not set and not found in $SECRETS_ENV"
  fi

  log "arc agent run starting..."
  if ! $DRY_RUN; then
    "$ARC" agent run
  else
    log "[dry-run] would run: $ARC agent run"
  fi
  log "arc agent run done."
}

# ---- entrypoint --------------------------------------------------------------
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
  exit $?
fi
