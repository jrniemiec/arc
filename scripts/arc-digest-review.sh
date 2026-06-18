#!/usr/bin/env bash
set -euo pipefail

# ---- globals / defaults ------------------------------------------------------
PROG="${0##*/}"

ARC="${ARC:-arc}"
ARC_AGENT_DIR="${ARC_AGENT_DIR:-$HOME/.arc/agent}"

RUN_ID=""
DEBUG=false
QUIET=false

# ---- helpers -----------------------------------------------------------------
RED="\033[31m"
YELLOW="\033[33m"
GREEN="\033[32m"
RESET="\033[0m"

log()  { $QUIET || printf '%s\n' "$*" >&2; }
dbg()  { $DEBUG && printf "${YELLOW}DEBUG:${RESET} %s\n" "$*" >&2 || true; }
die()  { printf "${RED}ERROR:${RESET} %s\n" "$*" >&2; exit 1; }

# ---- usage -------------------------------------------------------------------
usage() {
  cat <<EOF
Usage: $PROG [options] [run-id]

Interactively review skipped and maybe articles from an agent run.
Select articles to promote to ingest, then optionally trigger arc-digest-rerun.

Arguments:
  [run-id]    Run ID to review (default: last run)

Environment variables:
  ARC         Path to arc binary (default: arc)
  ARC_AGENT_DIR  Arc agent directory (default: ~/.arc/agent)

Options:
  -q, --quiet      Suppress log output
  -D, --debug      Debug output
  -h, --help       Show this help

Keys in fzf:
  Tab        Mark/unmark article for ingest
  Enter      Confirm selections
  Esc        Quit without changes

Examples:
  arc-digest-review
  arc-digest-review agent-20260617-181418
EOF
}

# ---- arg parsing -------------------------------------------------------------
parse_args() {
  while (($#)); do
    case "$1" in
      -q|--quiet)  QUIET=true ;;
      -D|--debug)  DEBUG=true ;;
      -h|--help)   usage; exit 0 ;;
      --) shift; break ;;
      -*) die "unknown flag: $1 (try --help)" ;;
      *)  RUN_ID="$1" ;;
    esac
    shift
  done
}

# ---- cleanup / traps ---------------------------------------------------------
TMPDIR_WORK=""
cleanup() {
  [[ -n "$TMPDIR_WORK" ]] && rm -rf "$TMPDIR_WORK"
}
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

  command -v fzf >/dev/null 2>&1 || die "fzf not found (brew install fzf)"
  command -v jq  >/dev/null 2>&1 || die "jq not found (brew install jq)"

  # Resolve run ID.
  if [[ -z "$RUN_ID" ]]; then
    RUN_ID=$("$ARC" agent log -n 1 | awk '{print $NF}')
    [[ -n "$RUN_ID" ]] || die "could not determine last run ID"
    log "reviewing run: $RUN_ID"
  fi

  DECISIONS_FILE="$ARC_AGENT_DIR/decisions-${RUN_ID}.json"
  [[ -f "$DECISIONS_FILE" ]] || die "decisions file not found: $DECISIONS_FILE"

  # Build temp dir with per-item data files for preview.
  TMPDIR_WORK=$(mktemp -d)
  ITEMS_FILE="$TMPDIR_WORK/items.tsv"   # idx TAB verdict TAB title
  DETAIL_DIR="$TMPDIR_WORK/details"     # per-item detail files
  mkdir -p "$DETAIL_DIR"

  # Extract skip/maybe items from decisions file.
  jq -r '
    .feeds[] |
    .name as $feed |
    .items[] |
    select(.verdict == "skip" or .verdict == "maybe") |
    select(.status == "pending") |
    [$feed, .verdict, .title, .url, .reason] | @tsv
  ' "$DECISIONS_FILE" > "$TMPDIR_WORK/raw.tsv"

  if [[ ! -s "$TMPDIR_WORK/raw.tsv" ]]; then
    echo "No pending skip/maybe articles to review in run $RUN_ID."
    exit 0
  fi

  # Build items list and detail files.
  idx=0
  while IFS=$'\t' read -r feed verdict title url reason; do
    printf '%d\t%s\t%s\n' "$idx" "$verdict" "$title" >> "$ITEMS_FILE"
    printf 'Feed:    %s\nVerdict: %s\nURL:     %s\n\nReason:\n%s\n' \
      "$feed" "$verdict" "$url" "$reason" > "$DETAIL_DIR/$idx"
    idx=$((idx + 1))
  done < "$TMPDIR_WORK/raw.tsv"

  TOTAL=$idx

  # Format list for fzf: [verdict  ]  title
  # Pad verdict to 5 chars for alignment.
  FZF_LIST=$(awk -F'\t' '{
    v = $2; title = $3; idx = $1
    printf "[%-5s]  %s\t%s\n", v, title, idx
  }' "$ITEMS_FILE")

  # Run fzf.
  SELECTED=$(printf '%s\n' "$FZF_LIST" | fzf \
    --multi \
    --ansi \
    --marker '>' \
    --prompt 'Select articles to ingest > ' \
    --header "Run: $RUN_ID | $TOTAL articles | Tab=select  Enter=confirm  Esc=quit" \
    --preview "cat \"$DETAIL_DIR/\$(echo {} | awk -F'\t' '{print \$2}')\"" \
    --preview-window 'right:50%:wrap' \
    --with-nth 1 \
    --delimiter $'\t' \
    || true)

  if [[ -z "$SELECTED" ]]; then
    echo "No articles selected — no changes made."
    exit 0
  fi

  # Collect selected titles for confirmation.
  echo ""
  echo "Pending changes:"
  echo ""
  declare -a SELECTED_TITLES=()
  while IFS=$'\t' read -r display idx; do
    title=$(awk -F'\t' -v i="$idx" '$1 == i {print $3}' "$ITEMS_FILE")
    printf "  ${GREEN}+${RESET} INGEST  %s\n" "$title"
    SELECTED_TITLES+=("$title")
  done <<< "$SELECTED"

  echo ""
  printf "Confirm? [y/N]: "
  read -r answer
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "Aborted — no changes made."; exit 0 ;;
  esac

  # Write updated decisions file — flip selected items to action:"+", status:"pending".
  # Build a list of titles to match (jq will match by title).
  TITLES_JSON=$(printf '%s\n' "${SELECTED_TITLES[@]}" | jq -R . | jq -s .)

  UPDATED=$(jq --argjson titles "$TITLES_JSON" '
    .feeds[].items[] |= (
      if (.verdict == "skip" or .verdict == "maybe")
        and .status == "pending"
        and ([$titles[] == .title] | any)
      then . + {"action": "+"}
      else .
      end
    )
  ' "$DECISIONS_FILE")

  printf '%s\n' "$UPDATED" > "$DECISIONS_FILE"
  echo "Decisions file updated."

  # Offer to run arc-digest-rerun.
  echo ""
  printf "Run arc-digest-rerun now? [y/N]: "
  read -r rerun_answer
  case "$rerun_answer" in
    y|Y|yes|YES)
      arc-digest-rerun "$RUN_ID"
      ;;
    *)
      echo "Run manually with: arc-digest-rerun $RUN_ID"
      ;;
  esac
}

# ---- entrypoint --------------------------------------------------------------
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
  exit $?
fi
