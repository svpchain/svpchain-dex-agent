#!/usr/bin/env bash
#
# scripts/lib/common.sh — sourceable helpers for the deploy script.
#
# Source (don't execute):
#   source "$(dirname "${BASH_SOURCE[0]}")/lib/common.sh"
#
# Provides:
#   - C_GREEN / C_RED / C_YELLOW / C_BOLD / C_RESET   ANSI escape codes
#     (empty strings if stdout is not a TTY or the term advertises <8 colors)
#   - step / pass / info                              colored progress output
#   - require_cmd                                     prereq check + clear error
#
# Intentionally does NOT define `fail` / `log` / `ok` — each caller defines its
# own `fail` matching its style, so sourcing this file never clobbers them.

# Idempotent: safe to source from multiple places without re-running setup.
if [[ -n "${__SCRIPTS_LIB_COMMON_LOADED:-}" ]]; then return 0; fi
__SCRIPTS_LIB_COMMON_LOADED=1

if [[ -t 1 ]] && command -v tput >/dev/null 2>&1 \
   && [[ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]]; then
  C_GREEN=$(tput setaf 2); C_RED=$(tput setaf 1); C_YELLOW=$(tput setaf 3)
  C_BOLD=$(tput bold);     C_RESET=$(tput sgr0)
else
  C_GREEN=""; C_RED=""; C_YELLOW=""; C_BOLD=""; C_RESET=""
fi

# step "..."  Section heading. Bold; preceded by a blank line.
step() { printf "\n${C_BOLD}== %s ==${C_RESET}\n" "$*"; }

# pass "..."  Green-check line. For step-internal success indicators.
pass() { printf "  ${C_GREEN}✓${C_RESET} %s\n" "$*"; }

# info "..."  Yellow-dot line. For non-fatal notices (skipped steps, hints).
info() { printf "  ${C_YELLOW}·${C_RESET} %s\n" "$*"; }

# require_cmd CMD — exit 1 with a clear message if CMD is not on PATH.
require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    printf "  ${C_RED}✗${C_RESET} required command not found: %s\n" "$1" >&2
    exit 1
  }
}
