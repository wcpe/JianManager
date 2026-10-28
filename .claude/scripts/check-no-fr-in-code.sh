#!/bin/sh
# 禁止代码/文件名/注释/测试描述出现 FR 编号（见 .claude/rules/no-fr-in-code.md）。
#
# 两种模式：
#   diff（默认）  ：只扫 diff 的「新增行」，存量不参与。日常提交前用这个。
#   --stock       ：扫全仓代码现有的 FR 编号。验收 FR-508（全量清理完成）时用，期望 0 命中。
#
# 用法：
#   .claude/scripts/check-no-fr-in-code.sh                 # 对比 origin/master（diff 模式）
#   .claude/scripts/check-no-fr-in-code.sh <base-ref>      # 对比指定基线
#   .claude/scripts/check-no-fr-in-code.sh --stock         # 存量模式（对工作区当前内容）
#
# 豁免：文档路径（docs/、CHANGELOG.md）与本规则目录不在扫描范围；
#       代码行内可加 `no-fr-check` 标记逐行豁免（需在 PR 描述里说明原因）。
#
# 退出码：0 = 通过；1 = 命中违规
set -eu

# 只查代码文件；文档与规则本身允许出现 FR 编号。
CODE_PATHS='apps internal packages'
EXTS='go|ts|tsx|js|jsx|mjs|cjs|vue|svelte'

# 命中的正则：FR-<数字>；排除 A-Za-z0-9_- 前缀干扰（如 ADR-FR-1 这种拼接）。
FR_RE='(^|[^A-Za-z0-9_-])FR-[0-9]+'

if [ "${1:-}" = "--stock" ]; then
  # ── 存量模式：扫全仓代码现有 FR 编号 ───────────────────────────────
  # --include 逐扩展名筛；bin 文件与 .git/node_modules 自然被 grep -I 排除。
  out=$(grep -rInE "$FR_RE" $CODE_PATHS \
    --include='*.go' --include='*.ts' --include='*.tsx' --include='*.js' \
    --include='*.jsx' --include='*.mjs' --include='*.cjs' \
    --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=.git 2>/dev/null \
    | grep -v 'no-fr-check' || true)

  if [ -n "$out" ]; then
    n=$(printf '%s\n' "$out" | wc -l)
    echo "❌ 代码里仍有 FR 编号共 $n 处（FR-508 验收要求 0）"
    echo "   规则见 .claude/rules/no-fr-in-code.md"
    echo ""
    printf '%s\n' "$out" | head -40
    echo ""
    exit 1
  fi

  echo "✅ 存量模式：代码里已无 FR 编号"
  exit 0
fi

# ── diff 模式：只扫新增行 ───────────────────────────────────────
BASE="${1:-origin/master}"

# 收集 diff 的新增行：格式 "<file>:<line>:<content>"
violations=$(git diff -U0 "$BASE"...HEAD -- . \
  | awk -v paths="^(apps|internal|packages)/" -v exts="$EXTS" '
      # 进入新文件块时记录文件名
      /^\+\+\+ b\// { file = substr($0, 7); next }
      # 命中 hunk 头，记录当前新文件起始行号
      /^@@ / {
        if (match($0, /\+[0-9]+/)) { line = substr($0, RSTART + 1, RLENGTH - 1) + 0 } else { line = 0 }
        next
      }
      # 只取新增行
      /^\+/ && !/^\+\+\+/ {
        content = substr($0, 2)
        want = (file ~ paths) && (file ~ ("\\.(" exts ")$"))
        if (want && content !~ /no-fr-check/) {
          if (match(content, /(^|[^A-Za-z0-9_-])FR-[0-9]+/)) {
            printf "%s:%d:%s\n", file, line, content
          }
        }
        line++
        next
      }
    ')

if [ -n "$violations" ]; then
  echo "❌ 代码中新出现了 FR 编号（规则见 .claude/rules/no-fr-in-code.md）"
  echo "   FR 编号只属于文档；要说明实现缘由请引 ADR 编号。"
  echo ""
  echo "$violations" | head -40
  echo ""
  echo "存量 FR 编号不在本门禁范围（用 --stock 检查存量）；仅需去掉本次新增的。"
  exit 1
fi

echo "✅ 未发现新增的 FR 编号"
