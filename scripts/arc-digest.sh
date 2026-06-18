#!/usr/bin/env bash
set -euo pipefail

# ---- globals / defaults ------------------------------------------------------
PROG="${0##*/}"

ARC="${ARC:-arc}"
ARC_RECIPIENT="${ARC_RECIPIENT:-}"
ARC_FROM="${ARC_FROM:-}"
ARC_AGENT_DIR="${ARC_AGENT_DIR:-$HOME/.arc/agent}"

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

Run the arc feed agent and email the briefing if articles were ingested.
Intended to be called by launchd or cron at 6am.

Environment variables:
  ARC_RECIPIENT          Email recipient address (required)
  ARC_FROM               Email sender address (required)
  ARC_ANTHROPIC_API_KEY  Anthropic API key (falls back to macOS Keychain)
  ARC                    Path to arc binary (default: arc)
  ARC_AGENT_DIR          Arc agent directory (default: ~/.arc/agent)

Options:
  -n, --dry-run    Print what would run, don't execute
  -q, --quiet      Suppress log output
  -D, --debug      Debug output
  -h, --help       Show this help

Examples:
  ARC_RECIPIENT=you@gmail.com ARC_FROM=you@gmail.com arc-digest
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

# ---- send email --------------------------------------------------------------
send_email() {
  local to="$1" from="$2" subject="$3" body="$4"
  if $DRY_RUN; then
    log "[dry-run] would send: $subject -> $to"
    return 0
  fi
  printf 'To: %s\nFrom: %s\nSubject: %s\nContent-Type: text/plain; charset=utf-8\n\n%s\n' \
    "$to" "$from" "$subject" "$body" \
    | msmtp "$to"
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

  [[ -n "$ARC_RECIPIENT" ]] || die "ARC_RECIPIENT is not set (try --help)"
  [[ -n "$ARC_FROM" ]]      || die "ARC_FROM is not set (try --help)"

  # Load API key from Keychain if not set in environment.
  if [[ -z "${ARC_ANTHROPIC_API_KEY:-}" ]]; then
    ARC_ANTHROPIC_API_KEY=$(security find-generic-password -a anthropic -s arc -w 2>/dev/null) \
      || die "API key not found. Set ARC_ANTHROPIC_API_KEY or run: security add-generic-password -a anthropic -s arc -w <key>"
    export ARC_ANTHROPIC_API_KEY
  fi

  log "arc agent run starting..."
  if ! $DRY_RUN; then
    "$ARC" agent run
  else
    log "[dry-run] would run: $ARC agent run"
  fi
  log "arc agent run done."

  log "generating briefing..."
  BRIEFING=$("$ARC" agent briefing)
  BRIEFING_TTS=$("$ARC" agent briefing --tts)

  if [[ -z "$BRIEFING" ]]; then
    log "nothing ingested — skipping email."
    exit 0
  fi

  COUNT=$(printf '%s\n' "$BRIEFING_TTS" | grep -c '^[0-9]\+\.' || true)
  SUBJECT="arc digest — $(date '+%a %b %e' | tr -s ' ') (${COUNT} articles)"

  log "sending: $SUBJECT"
  send_email "$ARC_RECIPIENT" "$ARC_FROM" "$SUBJECT" "$BRIEFING"
  send_email "$ARC_RECIPIENT" "$ARC_FROM" "$SUBJECT [tts]" "$BRIEFING_TTS"
  log "emails sent."
}

# ---- entrypoint --------------------------------------------------------------
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
  exit $?
fi
