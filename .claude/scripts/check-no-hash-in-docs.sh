#!/bin/sh
# 文档里禁止出现 git commit hash（squash merge 后会悬空）。
# 见 .claude/rules/no-hash-in-docs.md
#
# 用法：
#   sh .claude/scripts/check-no-hash-in-docs.sh          # 只扫 diff 的 + 行（日常）
#   sh .claude/scripts/check-no-hash-in-docs.sh --all    # 扫全仓 md，期望 0 命中
#
# 退出码：0 = 通过，1 = 命中

set -eu

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$REPO_ROOT"

MODE="diff"
[ "${1:-}" = "--all" ] && MODE="all"

# 候选 regex：反引号包裹的 7-40 位十六进制
# （刻意不做上下文判断，因为 hash 常出现在“落地提交：`xxx`”以外的位置）
HIT_FILE=$(mktemp)
trap 'rm -f "$HIT_FILE"' EXIT

# 剔除：内容哈希 / 错误码 / PID / 字节数 / 示例占位 / 标识符
# 这些是已知误报类，单独白名单化
filter_noise() {
  grep -v 'no-hash-check' |
  grep -vE '4003410[0-9]|4005400[0-9]|4005401[0-9]' |
  grep -vE '`(abcdef0|abc123def456|def456abc789|a1b2c3d4|a3f2b8c1|c2e4b683|aafee525|a9049ce1|ed25519|e3b0c44|803ae4f)`' |
  grep -vE '`[0-9]{7,40}`' || true
}

if [ "$MODE" = "all" ]; then
  # 全仓 md
  find . -name '*.md' \
    -not -path './node_modules/*' \
    -not -path './.git/*' \
    -not -path './dist/*' \
    -not -path './build/*' \
    -print0 | xargs -0 -r grep -nH -E '`[0-9a-f]{7,40}`' 2>/dev/null > "$HIT_FILE" || true

  # 噪声过滤（纯管道）
  filter_noise < "$HIT_FILE" > "${HIT_FILE}.f"
  mv "${HIT_FILE}.f" "$HIT_FILE"
else
  # 只看 diff 的 + 行，只算 md
  BASE=$(git rev-parse --verify origin/master 2>/dev/null && echo origin/master || echo HEAD~1)
  git diff "$BASE"...HEAD -- '*.md' 2>/dev/null \
    | grep -E '^\+' | grep -vE '^\+\+\+' \
    | grep -E '`[0-9a-f]{7,40}`' > "$HIT_FILE" || true
fi

if [ -s "$HIT_FILE" ] && [ -n "$(grep -vE '^\s*$' "$HIT_FILE" || true)" ]; then
  echo "" >&2
  echo "✖ 文档里发现疑似 git commit hash（squash merge 后必然悬空）：" >&2
  echo "" >&2
  grep -vE '^\s*$' "$HIT_FILE" | head -50 >&2
  echo "" >&2
  echo "修复：改用「提交标题 + 日期」定位，见 .claude/rules/no-hash-in-docs.md" >&2
  echo "若确实是内容哈希/错误码被误判，在该行末加 <!-- no-hash-check: 原因 -->" >&2
  echo "" >&2
  N=$(grep -vE '^\s*$' "$HIT_FILE" | wc -l)
  echo "共 $N 处" >&2
  exit 1
fi

echo "✅ 文档无 git commit hash 引用"
exit 0
