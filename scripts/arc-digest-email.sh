#!/usr/bin/env bash
set -euo pipefail

# ---- globals / defaults ------------------------------------------------------
PROG="${0##*/}"

ARC="${ARC:-arc}"
ARC_RECIPIENT="${ARC_RECIPIENT:-}"
ARC_FROM="${ARC_FROM:-}"

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
Usage: $PROG [options] [run-id ...]

Build and send the arc digest email for one or more runs.
Each run's digest is concatenated into a single email.
Defaults to the last daily run if no run IDs are given.

Arguments:
  [run-id ...]   One or more run IDs to include (e.g. agent-20260618-163055)

Environment variables:
  ARC_RECIPIENT  Email recipient address (required)
  ARC_FROM       Email sender address (required)
  ARC            Path to arc binary (default: arc)

Options:
  -n, --dry-run    Build digest but don't send email
  -q, --quiet      Suppress log output
  -D, --debug      Debug output
  -h, --help       Show this help

Examples:
  arc-digest-email
  arc-digest-email agent-20260618-163055
  arc-digest-email agent-20260618-163055 agent-20260618-185129
  arc-digest-email --dry-run
EOF
}

# ---- arg parsing -------------------------------------------------------------
declare -a RUN_IDS=()

parse_args() {
  while (($#)); do
    case "$1" in
      -n|--dry-run) DRY_RUN=true ;;
      -q|--quiet)   QUIET=true ;;
      -D|--debug)   DEBUG=true ;;
      -h|--help)    usage; exit 0 ;;
      --) shift; break ;;
      -*) die "unknown flag: $1 (try --help)" ;;
      *)  RUN_IDS+=("$1") ;;
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

  # Default to last daily run if no run IDs given.
  if [[ ${#RUN_IDS[@]} -eq 0 ]]; then
    LAST_DAILY=$("$ARC" agent log | grep -v '\[decisions\]' | tail -1 | awk '{print $NF}')
    [[ -n "$LAST_DAILY" ]] || die "could not determine last daily run ID"
    RUN_IDS=("$LAST_DAILY")
    log "using last daily run: $LAST_DAILY"
  fi

  # Build combined digest bodies.
  BODY=""
  BODY_TTS=""
  for run_id in "${RUN_IDS[@]}"; do
    log "generating digest for run $run_id..."
    PART=$("$ARC" agent digest --run "$run_id" || true)
    PART_TTS=$("$ARC" agent digest --tts --run "$run_id" || true)
    if [[ -n "$PART" ]]; then
      BODY="${BODY:+$BODY$'\n\n'}$PART"
      BODY_TTS="${BODY_TTS:+$BODY_TTS$'\n\n'}$PART_TTS"
    fi
  done

  if [[ -z "$BODY" ]]; then
    log "nothing ingested across all runs — skipping email."
    exit 0
  fi

  COUNT=$(printf '%s\n' "$BODY_TTS" | grep -c '^[0-9]\+\.' || true)
  SUBJECT="arc digest — $(date '+%a %b %e' | tr -s ' ') (${COUNT} articles)"

  log "sending: $SUBJECT"
  send_email "$ARC_RECIPIENT" "$ARC_FROM" "$SUBJECT" "$BODY"
  send_email "$ARC_RECIPIENT" "$ARC_FROM" "$SUBJECT [tts]" "$BODY_TTS"
  log "emails sent."
}

# ---- entrypoint --------------------------------------------------------------
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
  exit $?
fi
