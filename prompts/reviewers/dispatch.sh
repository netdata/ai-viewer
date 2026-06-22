#!/usr/bin/env bash
# Dispatcher: concatenates the common brief + scope-specific brief
# into a single prompt for one reviewer, then invokes the
# appropriate CLI. 30-minute timeout per the ~/.AGENTS.md protocol.
#
# Usage: dispatch.sh <reviewer-number>  (1-8)
#
# Reviewer 9 (operator UX) is done by the CTO directly, not via
# a CLI dispatcher. The prompt lives at prompts/reviewers/
# reviewer-9-ux.md for future re-dispatch if a CLI becomes
# available.
#
# Note: this is run by the CTO, NOT by the reviewer itself. The
# reviewers are LLM CLI tools, not agents that can run this script.
set -euo pipefail

cd /home/costa/src/ai-viewer.git
COMMON=prompts/reviewers/common-brief.md

run_codex() {
  # OpenAI codex CLI (gpt-5.5)
  local scope=$1
  {
    cat "$COMMON"
    cat "prompts/reviewers/$scope"
  } > /tmp/reviewer-prompt.txt
  timeout 1800 codex2 exec --skip-git-repo-check "$(cat /tmp/reviewer-prompt.txt)"
}

run_claude() {
  # Anthropic Claude Code CLI (claude-opus-4.8)
  local scope=$1
  {
    cat "$COMMON"
    cat "prompts/reviewers/$scope"
  } > /tmp/reviewer-prompt.txt
  CLAUDECODE="" timeout 1800 claude -p "$(cat /tmp/reviewer-prompt.txt)"
}

run_opencode_model() {
  # Opencode CLI with a model from litellm
  local model=$1
  local scope=$2
  {
    cat "$COMMON"
    cat "prompts/reviewers/$scope"
  } > /tmp/reviewer-prompt.txt
  timeout 1800 opencode run -m "llm-netdata-cloud/$model" --variant max --agent code-reviewer "$(cat /tmp/reviewer-prompt.txt)"
}

case "${1:-}" in
  1) run_codex reviewer-1-codex.md ;;
  2) run_claude reviewer-2-claude.md ;;
  3) run_opencode_model glm-5.2-max reviewer-3-canonical-glm.md ;;
  4) run_opencode_model minimax-m3-coder reviewer-4-v2-minimax.md ;;
  5) run_opencode_model mimo-v2.5-pro reviewer-5-v3-mimo.md ;;
  6) run_opencode_model kimi-k2.7-code reviewer-6-opencode-kimi.md ;;
  7) run_opencode_model deepseek-v4-pro reviewer-7-framework-deepseek.md ;;
  8) run_opencode_model qwen3.7-plus reviewer-8-sql-qwen.md ;;
  *) echo "usage: $0 <1-8>  (9 is CTO UX review)" >&2; exit 2 ;;
esac
