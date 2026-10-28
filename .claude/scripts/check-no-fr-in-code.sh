#!/bin/sh
# 禁止代码/文件名/注释/测试描述出现 FR 编号（见 .claude/rules/no-fr-in-code.md）。
#
# 两种模式：
#   diff（默认）  ：只算「本次新引入」的 FR 编号。日常提交前用这个。
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

EXTS='go|ts|tsx|js|jsx|mjs|cjs|vue|svelte'

# 抽出文本里的 FR 编号（去重）。排除字母/数字/下划线/连字符前缀，
# 避免把 XFR-1 这种标识符当成 FR 编号。
extract_fr() {
  grep -oE '(^|[^A-Za-z0-9_-])FR-[0-9]+' 2>/dev/null \
    | grep -oE 'FR-[0-9]+' | sort -u || true
}

if [ "${1:-}" = "--stock" ]; then
  # ── 存量模式：扫全仓代码现有 FR 编号 ───────────────────────────────
  out=$(grep -rInE '(^|[^A-Za-z0-9_-])FR-[0-9]+' apps internal packages \
    --include='*.go' --include='*.ts' --include='*.tsx' --include='*.js' \
    --include='*.jsx' --include='*.mjs' --include='*.cjs' \
    --exclude-dir=node_modules --exclude-dir=dist --exclude-dir=.git 2>/dev/null \
    | grep -v 'no-fr-check' || true)

  if [ -n "$out" ]; then
    n=$(printf '%s\n' "$out" | wc -l | tr -d ' ')
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

# ── diff 模式：只算「本次新引入」的 FR 编号 ────────────────────────
#
# 不能只看「新增行里有没有 FR 编号」。只改同一行里的一个词（例如把注释里的 ADR
# 编号换掉），整行就会被 diff 当成「删旧行 + 增新行」，行内本来就有的存量 FR 编号
# 会凭空变成「新增」。所以要比对集合：某文件新增行里出现、且该文件删除行里没出现过
# 的 FR 编号，才算本次新引入。
BASE="${1:-origin/master}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# 1) 把 diff 拆成「新增行」与「删除行」两份清单，各自带文件名。
git diff -U0 "$BASE"...HEAD -- . \
  | awk -v out="$TMP" '
      /^\+\+\+ b\// { file = substr($0, 7); next }
      /^@@ / {
        if (match($0, /\+[0-9]+/)) { line = substr($0, RSTART + 1, RLENGTH - 1) + 0 } else { line = 0 }
        next
      }
      /^\+/ && !/^\+\+\+/ {
        gsub(/\t/, " ")
        print file "\t" line "\t" substr($0, 2) >> (out "/added")
        line++
        next
      }
      /^-/ && !/^---/ {
        gsub(/\t/, " ")
        print file "\t" substr($0, 2) >> (out "/removed")
        next
      }
    '

[ -f "$TMP/added" ] || { echo "✅ 未发现新增的 FR 编号"; exit 0; }

# 2) 逐文件处理。
: > "$TMP/violations"
cut -f1 "$TMP/added" | sort -u | while IFS= read -r f; do
  # 只查代码文件（文档与规则目录允许出现 FR 编号）
  printf '%s' "$f" | grep -qE "\.($EXTS)$" || continue
  printf '%s' "$f" | grep -qE '^(apps|internal|packages)/' || continue

  # 该文件删除行里的 FR 编号 = 它本来就有的
  if [ -f "$TMP/removed" ]; then
    removed=$(awk -F'\t' -v want="$f" '$1 == want { $1=""; print }' "$TMP/removed" | extract_fr)
  else
    removed=""
  fi

  # 该文件新增行：行内出现、且不在 removed 里的 FR 编号 => 新引入
  awk -F'\t' -v want="$f" '$1 == want { print $2 "\t" $3 }' "$TMP/added" \
    | while IFS="$(printf '\t')" read -r line content; do
        case "$content" in *no-fr-check*) continue ;; esac
        for fr in $(printf '%s' "$content" | extract_fr); do
          if ! printf '%s\n' "$removed" | grep -qx "$fr"; then
            printf '%s:%s:%s\n' "$f" "$line" "$content"
            break
          fi
        done
      done >> "$TMP/violations"
done

vcount=$(wc -l < "$TMP/violations" | tr -d ' ')
if [ "$vcount" != "0" ]; then
  echo "❌ 代码中新出现了 FR 编号（规则见 .claude/rules/no-fr-in-code.md）"
  echo "   FR 编号只属于文档；要说明实现缘由请引 ADR 编号。"
  echo ""
  head -40 "$TMP/violations"
  echo ""
  echo "共 $vcount 处。同一行里本来就有的存量编号不算违规；用 --stock 检查存量。"
  exit 1
fi

echo "✅ 未发现新增的 FR 编号"
