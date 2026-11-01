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
  # 只扫 git 跟踪的代码文件：.tmp/ 与 dist/ 是构建产物，不应参与验收。
  out=$(git grep -InE '(^|[^A-Za-z0-9_-])FR-[0-9]+' -- apps internal packages scripts \
    ':!*.md' ':!*.json' 2>/dev/null \
    | grep -v 'no-fr-check' || true)

    # 文件名也要查。规则 .claude/rules/no-fr-in-code.md 的「禁止」表里
    # 明确列了「文件名」（示例 `proxy_fr035_test.go`、`fr513-host-obs-visual.spec.ts`），
    # 而早前本脚本只 git grep **内容**，对文件名零覆盖——
    # 于是一整批 `*_fr5xx_test.go` 能在“已全量清零”的情况下安静地存在。
    #
    # 存量模式对文件名**只报告不失败**：仓库存量有 71 个这种文件名，
    # 一次性打开门禁会要求“改这一处顺便重命名 71 个文件”（每个都要改引用与用例路径），
    # 那是另一个量级的改动。真正该硬拦的是「**新引入**」，见 diff 模式。
    name_out=$(git ls-files -- apps internal packages scripts \
      | grep -iE 'fr[0-9]+' || true)

    if [ -n "$out" ]; then
      n=$(printf '%s\n' "$out" | wc -l | tr -d ' ')
      echo "❌ 代码内容里仍有 FR 编号共 $n 处（规则要求 0）"
      echo "   规则见 .claude/rules/no-fr-in-code.md"
      echo ""
      printf '%s\n' "$out" | head -40
      echo ""
      if [ -n "$name_out" ]; then
        echo "（另有文件名命中 FR 编号 $(( $(printf '%s\n' "$name_out" | wc -l) )) 个，存量债，不在此处阻断）"
      fi
      exit 1
    fi

    if [ -n "$name_out" ]; then
      m=$(printf '%s\n' "$name_out" | wc -l | tr -d ' ')
      echo "⚠️  代码内容已无 FR 编号；但仍有 $m 个文件名带 FR 编号（存量债）"
      echo "   规则禁止文件名带 FR，但一次性改名成本另计；新增文件由 diff 模式硬拦。"
      echo "   前 10 个："
      printf '%s\n' "$name_out" | head -10
      exit 0
    fi

    echo "✅ 存量模式：代码里与文件名中已无 FR 编号"
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

# 3) **新引入的文件名**也不得带 FR 编号。
#
# 规则 .claude/rules/no-fr-in-code.md 的「禁止」表里「文件名」是单独一行，
# 但早前脚本只查内容——于是一批 `*_fr5xx_test.go` 与 `fr513-*.spec.ts`
# 能带着“已全量清零”的结论被新增进来。这里只看 **A（新增）/R（重命名后）**，
# 存量文件不追究（它们由 --stock 报告但不阻断）。
name_added=$(git diff --diff-filter=AR --name-only "$BASE"...HEAD -- apps internal packages scripts \
  | grep -iE 'fr[0-9]+' || true)
if [ -n "$name_added" ]; then
  echo "❌ 新增/重命名的文件名里带了 FR 编号（规则见 .claude/rules/no-fr-in-code.md）"
  echo "   文件名不得含 FR 编号；要说明归属请把 FR 写进文档或提交信息。"
  echo ""
  printf '%s\n' "$name_added" | head -20
  echo ""
  exit 1
fi

echo "✅ 未发现新增的 FR 编号"
