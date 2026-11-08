#!/bin/bash
# SDD 合规检查脚本
# 用法: bash scripts/check-sdd.sh [gate]
# gate: prd | sdd | api | merge | all (默认 all)
#
# ⚠️ 本脚本只做**结构性**检查（文件在不在、条目数对不对），不验证内容质量。
# 它没有接入 CI（見 .github/workflows），靠人手动跑；因此判据一旦与仓库
# 实际形态脱节，就会长期静默空转而不被发现（2026-11-08 实测：四条门禁全失效）。
# 改 PRD/API 的**格式**时，必须同步改本脚本的判据，否则它会继续拿「✅」骗人。

set -e

GATE="${1:-all}"
ERRORS=0
WARNINGS=0

info()  { echo "ℹ️  $*"; }
ok()    { echo "✅ $*"; }
warn()  { echo "⚠️  $*"; WARNINGS=$((WARNINGS + 1)); }
fail()  { echo "❌ $*"; ERRORS=$((ERRORS + 1)); }

echo "=== SDD 合规检查 ($GATE) ==="
echo ""

# ─── Gate 1: PRD ───
check_prd() {
    echo "--- Gate 1: PRD → SDD ---"
    if [ ! -f "docs/PRD.md" ]; then fail "缺少 docs/PRD.md"; return; fi

    # FR 在 PRD 里是**表格行**（`| FR-NNN | 标题 | 优先级 | 状态 |`），不是 `### FR-NNN` 小节。
    # 旧版按 `### FR-` 匹配、按 `优先级.*P0` 计数 ⇒ 恒得 0 / 1 条，静默空转。
    p0_count=$(grep -cE '^\| FR-[0-9]+ .*\| P0 \|' docs/PRD.md || true)
    if [ "$p0_count" -eq 0 ]; then
        fail "PRD 中未找到 P0 FR（若 PRD 改了格式，请同步本脚本判据）"
    else
        ok "P0 FR 数量: $p0_count"
    fi

    # 验收标准的承载地在 spec，不在 PRD 行里（PRD 是索引）。这里只校验
    # 「P0 FR 的 spec 链接至少能指到一个存在的文件」，这是能真正转红的部分。
    missing_spec=0
    while IFS= read -r line; do
        # 抽 spec 路径：`spec：`docs/specs/...`` 或 `[规格][sNNN]`（后者无法解路径，跳过）
        spec=$(printf '%s' "$line" | grep -oE 'docs/specs/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+\.md' | head -1 || true)
        [ -z "$spec" ] && continue
        if [ ! -f "$spec" ]; then
            fr=$(printf '%s' "$line" | grep -oE '^\| FR-[0-9]+' | tr -d '| ' || true)
            warn "${fr:-某 P0 FR} 引用的 spec 不存在: $spec"
            missing_spec=$((missing_spec + 1))
        fi
    done < <(grep -E '^\| FR-[0-9]+ .*\| P0 \|' docs/PRD.md)
    # 只在真的扫到过 P0 FR 时才报「全部存在」——否则 p0_count 已为 0、主判据已失败，
    # 这里再打一个 ✅ 会让人以为至少有一半是好的。
    if [ "$p0_count" -gt 0 ]; then
        if [ "$missing_spec" -eq 0 ]; then
            ok "P0 FR 引用的 spec 路径全部存在"
        fi
    fi

    if [ ! -f "docs/ARCHITECTURE.md" ]; then fail "缺少 docs/ARCHITECTURE.md"; else ok "ARCHITECTURE.md 存在"; fi
    echo ""
}

# ─── Gate 2: SDD ───
check_sdd() {
    echo "--- Gate 2: SDD → Feature ---"
    # 注意大小写：仓库里是 CONVENTIONS.md。写成小写时本门禁恒报「缺少」——
    # 而它的失败又没人看（未接入 CI），所以错了很多年。两种大小写都接受。
    for f in docs/ARCHITECTURE.md docs/API.md docs/CONVENTIONS.md; do
        if [ -f "$f" ]; then
            ok "$f 存在"
        else
            lower="$(dirname "$f")/$(basename "$f" | tr 'A-Z' 'a-z')"
            if [ -f "$lower" ]; then ok "$lower 存在（大小写与约定不同）"; else fail "缺少 $f"; fi
        fi
    done

    adr_count=$(ls docs/adr/*.md 2>/dev/null | wc -l || true)
    if [ "$adr_count" -eq 0 ]; then warn "无 ADR 记录"; else ok "ADR 数量: $adr_count"; fi
    echo ""
}

# ─── Gate 3: API ───
check_api() {
    echo "--- Gate 3: API Spec ---"
    if [ ! -f "docs/API.md" ]; then fail "缺少 docs/API.md"; return; fi

    # 只认真正的端点标题 `### METHOD /path`（或 `|` 合写的 `### M1 /  M2 /path`）。
    # 旧版用 `^### [A-Z]`，会把 `### FR-352 内部场景契约（非 HTTP endpoint）`、
    # `### Worker Log RPC 摘要（gRPC...）`、`### FR-264 消费端错误码` 也当成端点
    # ⇒ 永远多报 5 条假缺口（实测）。
    ep_re='^### (GET|POST|PUT|DELETE|PATCH|HEAD) /'
    endpoint_count=$(grep -cE "$ep_re" docs/API.md || true)
    if [ "$endpoint_count" -eq 0 ]; then warn "API.md 中无 endpoint 定义"; else ok "API endpoint 数量: $endpoint_count"; fi

    # endpoint 块级覆盖度：`### METHOD /path` 到下一个标题之间的**块内**应有「权限」行。
    missing_perm=$(awk -v re="$ep_re" '
        $0 ~ re { if (inblk && !has) n++; title=$0; has=0; inblk=1; next }
        /^#{1,3} /   { if (inblk && !has) n++; inblk=0; next }
        inblk && /权限/ { has=1 }
        END { if (inblk && !has) n++; print n+0 }
    ' docs/API.md)
    # 空值防护：awk 没输出（写错/无权限行也没触发 END）时不能当成「0 个缺失」——
    # 那会把一次判定失效变成一条 ✅。我自己就踩过：漏写 `print n` ⇒ 空字符串 ⇒
    # ${missing_perm:-0} 兜成 0 ⇒ 「全部 endpoint 均有权限标注」。宁肯报错。
    if [ -z "$missing_perm" ]; then
        fail "权限覆盖度统计失败（awk 无输出）——判据可能已与 API.md 格式脱节"
    elif [ "$missing_perm" -eq 0 ]; then
        ok "全部 endpoint 均有权限标注"
    else
        # 只告警（不计 ERROR、不影响退出码）：这是历史存量，不是本次改动引入的回归。
        warn "$missing_perm/$endpoint_count 个 endpoint 缺权限标注（历史存量）"
    fi
    echo ""
}

# ─── Gate 4: Merge ───
check_merge() {
    echo "--- Gate 4: 合并门禁 ---"
    if [ -f "CHANGELOG.md" ]; then ok "CHANGELOG.md 存在"; else warn "缺少 CHANGELOG.md"; fi

    # 检查 ARCHITECTURE.md 中提到的目录是否实际存在
    if grep -q 'internal/controlplane' docs/ARCHITECTURE.md 2>/dev/null; then
        if [ ! -d "internal/controlplane" ]; then
            warn "ARCHITECTURE.md 提到 internal/controlplane/ 但目录不存在"
        fi
    fi
    echo ""
}

# ─── 执行 ───
case "$GATE" in
    prd)   check_prd ;;
    sdd)   check_sdd ;;
    api)   check_api ;;
    merge) check_merge ;;
    all)
        check_prd
        check_sdd
        check_api
        check_merge
        ;;
    *)     echo "未知 gate: $GATE (可选: prd, sdd, api, merge, all)"; exit 1 ;;
esac

echo "=== 检查完成 ==="
echo "错误: $ERRORS | 警告: $WARNINGS"
if [ "$ERRORS" -gt 0 ]; then exit 1; fi
