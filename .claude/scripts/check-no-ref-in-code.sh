#!/bin/sh
# 禁止代码/文件名/注释/测试描述出现 FR 与 ADR 编号（见 .claude/rules/no-ref-in-code.md）。
#
# 两种模式：
#   diff（默认）  ：只算「本次新引入」的编号。日常提交前用这个。
#   --stock       ：扫全仓代码现有的编号。全量清理的验收用这个，期望 0 命中。
#
# 用法：
#   .claude/scripts/check-no-ref-in-code.sh                 # 对比 origin/master（diff 模式）
#   .claude/scripts/check-no-ref-in-code.sh <base-ref>      # 对比指定基线
#   .claude/scripts/check-no-ref-in-code.sh --stock         # 存量模式（对工作区当前内容）
#
# 豁免：文档路径（docs/、CHANGELOG.md）与本规则目录不在扫描范围；
#       代码行内可加 `no-ref-check` 标记逐行豁免（需在 PR 描述里说明原因）；
#       验收工具目录 scripts/fr-acceptance/ 整目录豁免（编号是用例主键，见下）。
#
# 退出码：0 = 通过；1 = 命中违规
set -eu

EXTS='go|ts|tsx|js|jsx|mjs|cjs|vue|svelte|java|kts|proto'
# 扫描的代码根。docs/ 与 CHANGELOG 不在内（编号在那里是有效信息）。
ROOTS='apps internal packages scripts proto client-updater configs'
# 生成物不算人手写的（protoc 产物会从 .proto 注释里带出编号，改源即可）。
EXCLUDES=':!*.pb.go :!:!*.md :!*.json'
# 编号正则：前面不得紧邻字母/数字/下划线/连字符，避开 XFR-1 这类标识符。
PAT='(^|[^A-Za-z0-9_-])(FR|ADR)-[0-9]+'

# 目录级豁免：验收工具把 FR 号当作「用例主键」——它是数据，不是解释性注释。
#
# 判据：删掉这个号，脚本就失去身份（pending.py 正是靠扫 FR-(\d+)
# 统计「哪些已被断言过」）。这与规则要防的失效模式不同：规则防的是
# 「注释写了个编号当门牌号，号变了注释就成了死引用，且没有编译错误」。
# 这里的编号有机器消费者，改号时必然要改这处数据，不会静默失效。
#
# 豁免仅限这一个目录；生产代码（apps/ internal/ packages/ proto/ 等）照旧要求 0。
ALLOW_DIRS='scripts/fr-acceptance'

# 扫「代码内容」的存量违规。
grep_stock() {
  # shellcheck disable=SC2086
  git grep -InE "$PAT" -- $ROOTS ':!*.md' ':!*.json' ':!*.pb.go' 2>/dev/null \
    | grep -v 'no-ref-check' \
    | grep -vE "^($ALLOW_DIRS)/" || true
}

# 扫「文件名」的存量违规（FR / ADR 两种前缀，大小写不敏感）。
grep_names() {
  # shellcheck disable=SC2086
  git ls-files -- $ROOTS 2>/dev/null \
    | grep -vE "^($ALLOW_DIRS)/" \
    | grep -iE '(^|[^a-z0-9])(fr|adr)[0-9]+' || true
}

if [ "${1:-}" = "--stock" ]; then
  out=$(grep_stock)
  name_out=$(grep_names)

  fail=0
  if [ -n "$out" ]; then
    n=$(printf '%s\n' "$out" | wc -l | tr -d ' ')
    echo "❌ 代码内容里仍有 FR/ADR 编号共 $n 处（规则要求 0）"
    echo "   规则见 .claude/rules/no-ref-in-code.md"
    echo ""
    printf '%s\n' "$out" | head -60
    echo ""
    fail=1
  fi

  if [ -n "$name_out" ]; then
    m=$(printf '%s\n' "$name_out" | wc -l | tr -d ' ')
    echo "❌ 文件名里仍有 FR/ADR 编号共 $m 个（规则要求 0）"
    echo ""
    printf '%s\n' "$name_out" | head -60
    echo ""
    fail=1
  fi

  if [ "$fail" = "1" ]; then
    exit 1
  fi
  echo "✅ 存量模式：代码内容与文件名中已无 FR/ADR 编号"
  exit 0
fi

# ── diff 模式：只算「本次新引入」的编号 ─────────────────────────────
#
# 不能只看「新增行里有没有编号」。只改同一行里的一个词，整行就会被 diff 当成
# 「删旧行 + 增新行」，行内本来就有的存量编号会凭空变成「新增」。所以要比对集合：
# 某文件新增行里出现、且该文件删除行里没出现过的编号，才算本次新引入。
BASE="${1:-origin/master}"
TMP=$(mktemp -d)
# shellcheck disable=SC2064
trap "rm -rf '$TMP'" EXIT

# 开启 -M：否则「重命名 + 改动」的文件会被当成整文件新增，
# 把搬过去的存量编号全部误报成新引入（实测 alert-helpers.ts 就是这么误报的）。
git diff -U0 -M "$BASE"...HEAD -- . \
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

if [ ! -f "$TMP/added" ]; then
  echo "✅ 未发现新增的 FR/ADR 编号"
  exit 0
fi

# 一次 awk 完成「逐文件集合差」：把删除行里的编号建索引，再筛新增行。
# （早前版本对每个文件起一次子 shell 循环，大仓 diff 会跑到分钟级。）
awk -F'\t' -v exts="$EXTS" '
function refs(s,  m, out) {
  out = ""
  while (match(s, /(^|[^A-Za-z0-9_-])(FR|ADR)-[0-9]+/)) {
    m = substr(s, RSTART, RLENGTH)
    sub(/^[^FA]*/, "", m)
    if (index(" " out " ", " " m " ") == 0) out = out " " m
    s = substr(s, RSTART + RLENGTH)
  }
  return out
}
FILENAME == ARGV[1] {
  n = split(refs($2), a, " ")
  for (i = 1; i <= n; i++) if (a[i] != "") removed[$1, a[i]] = 1
  next
}
{
  f = $1
  # 只查代码文件（文档与规则目录允许出现编号）
  if (f !~ "\\.(" exts ")$") next
  if (f !~ /^(apps|internal|packages|scripts|proto|client-updater|configs)\//) next
  if (f ~ /(\.pb\.go|\.md|\.json)$/) next
  if (f ~ /^scripts\/fr-acceptance\//) next
  if ($3 ~ /no-ref-check/) next
  n = split(refs($3), a, " ")
  for (i = 1; i <= n; i++) {
    if (a[i] == "") continue
    if (!((f, a[i]) in removed)) { printf "%s:%s:%s\n", f, $2, $3; break }
  }
}' "$TMP/removed" "$TMP/added" > "$TMP/violations"

vcount=$(wc -l < "$TMP/violations" | tr -d ' ')
if [ "$vcount" != "0" ]; then
  echo "❌ 代码中新出现了 FR/ADR 编号（规则见 .claude/rules/no-ref-in-code.md）"
  echo "   编号只属于文档；注释里写理由本身，不要写编号。"
  echo ""
  head -40 "$TMP/violations"
  echo ""
  echo "共 $vcount 处。同一行里本来就有的存量编号不算违规；用 --stock 检查存量。"
  exit 1
fi

# ── 新引入的文件名也不得带编号 ────────────────────────────────────
# 只看 A（新增）/R（重命名后）；存量文件由 --stock 报告。
# shellcheck disable=SC2086
name_added=$(git diff -M --diff-filter=AR --name-only "$BASE"...HEAD -- $ROOTS \
  | grep -iE '(^|[^a-z0-9])(fr|adr)[0-9]+' || true)
if [ -n "$name_added" ]; then
  echo "❌ 新增/重命名的文件名里带了 FR/ADR 编号（规则见 .claude/rules/no-ref-in-code.md）"
  echo "   文件名不得含编号；要说明归属请把编号写进文档或提交信息。"
  echo ""
  printf '%s\n' "$name_added" | head -20
  echo ""
  exit 1
fi

echo "✅ 未发现新增的 FR/ADR 编号"
