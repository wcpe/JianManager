#!/bin/sh
# docs 登记门禁：列出 docs/ 下未登记的目录/文件。
#
# 规则见 .claude/rules/docs-registry.md
# 退出码：0 = 全部已登记；1 = 有未登记项
#
# 用法：
#   sh .claude/scripts/check-docs-registry.sh        # 检查
#   sh .claude/scripts/check-docs-registry.sh -v     # 同时列出已登记的

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
DOCS="$ROOT/docs"
VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

[ -d "$DOCS" ] || { echo "docs/ 不存在，跳过"; exit 0; }

# ── 1. 允许的顶层条目（对应 doc-evolution.md 的文档类型表） ──
ALLOWED_TOP="PRD.md ARCHITECTURE.md API.md LOGS.md CONVENTIONS.md DEPLOY.md LIFECYCLE.md CONTRIBUTING.md README.md"
ALLOWED_DIRS="adr specs openapi screenshots"

# ── 2. 登记判定 ──
#
# “登记”不等于“在 PRD 里写了一行”：实际项目中 spec 可能被这些地方引用：
#   - PRD 的 FR 行 / 规格索引
#   - 其他 spec（如 agent-safety-gate/contract.md 引 approval-center/）
#   - ADR 正文
#   - specs/README
# 因此口径：**全仓 .md 里被引用过**（不含目录自引用）即算登记。
#
# 我们要拦的是「零引用、自建的新目录」（如 _evidence），
# 而不是已存在的 240+ 个 spec 目录。

MISSING=0
report_missing() {
  # 默认静默，只累计；有 -v 或最终比对时才输出
  [ "${SHOW_ALL:-0}" = "1" ] && printf '  ✗ %s\n' "$1"
  MISSING=$((MISSING + 1))
}

# 某个 spec 目录名是否被任何 .md 文件引用过（排除目录内部自引用）
is_referenced() {
  name=$1
  hits=$(grep -rlF "$name" --include='*.md' "$DOCS" "$ROOT/CHANGELOG.md" 2>/dev/null \
         | grep -v "/docs/specs/$name/" | head -1)
  [ -n "$hits" ]
}

# ── 3. 检查顶层条目 ──
for entry in "$DOCS"/* "$DOCS"/.[!.]*; do
  [ -e "$entry" ] || continue
  name=$(basename "$entry")

  if [ -d "$entry" ]; then
    # 目录必须在 ALLOWED_DIRS 里
    case " $ALLOWED_DIRS " in
      *" $name "*) : ;;
      *) report_missing "docs/$name/  （禁止自建子目录）"; continue ;;
    esac
  else
    case " $ALLOWED_TOP " in
      *" $name "*) : ;;
      *) report_missing "docs/$name  （不在允许的顶层文档集内）" ;;
    esac
  fi
done

# ── 4. 检查 docs/specs 下每个子目录是否被引用过 ──
if [ -d "$DOCS/specs" ]; then
  for d in "$DOCS/specs"/*/; do
    [ -d "$d" ] || continue
    name=$(basename "$d")
    case "$name" in
      _template) : ;;
      _*)
        # _ 前缀不构成豁免：_evidence 这类必须报
        report_missing "docs/specs/$name/  （_ 前缀不构成豁免，禁止自建目录）"
        ;;
      *)
        if is_referenced "$name"; then
          [ $VERBOSE -eq 1 ] && printf '  ✓ docs/specs/%s/\n' "$name"
        else
          report_missing "docs/specs/$name/  （全仓零引用，未登记）"
        fi
        ;;
    esac
  done
fi

# ── 5. 检查 docs/adr 下文件是否被引用过 ──
#
# 注意：adr/README.md 只列「最近新增」，不是全量索引（README 自己写着“别慌通读”）。
# 所以不能拿「不在 README 里」当未登记——按全仓引用判定。
if [ -d "$DOCS/adr" ]; then
  for f in "$DOCS/adr"/*.md; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    [ "$name" = "README.md" ] && continue
    stem=${name%.md}
    # 被任一 .md 引用（含 ARCHITECTURE / 其他 ADR / spec / README）即算登记
    if grep -rlF "$stem" --include='*.md' "$DOCS" "$ROOT/CHANGELOG.md" \
         "$ROOT/.claude/rules" >/dev/null 2>&1; then
      [ $VERBOSE -eq 1 ] && printf '  ✓ docs/adr/%s\n' "$name"
    else
      report_missing "docs/adr/$name  （全仓零引用，未登记）"
    fi
  done
fi

echo ""
if [ $MISSING -eq 0 ]; then
  echo "docs 登记检查通过（无未登记项）"
  exit 0
fi

# ── 6. 与存量基线对比：只拦「新增」违规 ──
#
# 背景：仓库里已有 66 个未登记项（38 个 ADR + 28 个 spec）——那是历史遗留，
# 与本次改动无关，不应妨碍任何人提交。
#
# 但基线不能是“永久免死金牌”：_evidence 这类零引用自建目录永远不会进基线，
# 所以它每次都会被拦。存量要清，靠单独的清理任务，不靠门禁。
BASELINE="$ROOT/.claude/baselines/docs-registry-baseline.txt"
if [ ! -f "$BASELINE" ]; then
  echo "发现 $MISSING 个未登记项（无基线，全部视为新增）。规则见 .claude/rules/docs-registry.md"
  exit 1
fi

NEW=0
# 重新走一遍收集，这次与基线对比
check_new() {
  item=$1
  if ! grep -Fxq "$item" "$BASELINE" 2>/dev/null; then
    printf '  ✗ %s\n' "$item"
    NEW=$((NEW + 1))
  fi
}

for entry in "$DOCS"/* "$DOCS"/.[!.]*; do
  [ -e "$entry" ] || continue
  name=$(basename "$entry")
  if [ -d "$entry" ]; then
    case " $ALLOWED_DIRS " in *" $name "*) : ;; *) check_new "docs/$name" ;; esac
  else
    case " $ALLOWED_TOP " in *" $name "*) : ;; *) check_new "docs/$name" ;; esac
  fi
done

if [ -d "$DOCS/specs" ]; then
  for d in "$DOCS/specs"/*/; do
    [ -d "$d" ] || continue
    name=$(basename "$d")
    case "$name" in
      _template) : ;;
      _*) check_new "docs/specs/$name" ;;
      *) is_referenced "$name" || check_new "docs/specs/$name" ;;
    esac
  done
fi

if [ -d "$DOCS/adr" ]; then
  for f in "$DOCS/adr"/*.md; do
    [ -f "$f" ] || continue
    name=$(basename "$f")
    [ "$name" = "README.md" ] && continue
    stem=${name%.md}
    grep -rlF "$stem" --include='*.md' "$DOCS" "$ROOT/CHANGELOG.md" "$ROOT/.claude/rules" >/dev/null 2>&1 \
      || check_new "docs/adr/$name"
  done
fi

echo ""
if [ $NEW -eq 0 ]; then
  echo "docs 登记检查通过（存量 $MISSING 项已基线化，无新增违规）"
  exit 0
fi
echo "发现 $NEW 个**新增**未登记项。规则见 .claude/rules/docs-registry.md"
echo "（存量 $MISSING 项在 .claude/baselines/docs-registry-baseline.txt 中放行）"
exit 1
