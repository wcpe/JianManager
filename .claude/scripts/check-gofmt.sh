#!/bin/sh
# 检查 Go 源码格式（gofmt）。见 .claude/rules/code-format.md。
#
# 用法：
#   .claude/scripts/check-gofmt.sh          # 检查（期望 0 命中）
#   .claude/scripts/check-gofmt.sh --fix    # 就地格式化
#
# 退出码：0 = 通过；1 = 有文件不合规
set -eu

ROOTS='internal apps scripts proto .'

if [ "${1:-}" = "--fix" ]; then
  # shellcheck disable=SC2086
  gofmt -w $ROOTS 2>/dev/null || true
  echo "✅ 已就地格式化"
  exit 0
fi

# shellcheck disable=SC2086
out=$(gofmt -l $ROOTS 2>/dev/null | grep -v '^vendor/' || true)

if [ -n "$out" ]; then
  n=$(printf '%s\n' "$out" | wc -l | tr -d ' ')
  echo "❌ 有 $n 个 Go 文件格式不合规（gofmt）"
  echo "   规则见 .claude/rules/code-format.md"
  echo ""
  printf '%s\n' "$out" | head -40
  echo ""
  echo "修复：sh .claude/scripts/check-gofmt.sh --fix"
  exit 1
fi

echo "✅ Go 源码格式全部合规"
