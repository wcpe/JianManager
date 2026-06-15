#!/bin/bash
# SDD 合规检查脚本
# 用法: bash scripts/check-sdd.sh [gate]
# gate: prd | sdd | api | merge | all (默认 all)

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

    p0_count=$(grep -c '优先级.*P0' docs/PRD.md || true)
    if [ "$p0_count" -eq 0 ]; then fail "PRD 中无 P0 FR"; else ok "P0 FR 数量: $p0_count"; fi

    # 检查 P0 FR 是否有验收标准
    while IFS= read -r fr; do
        if ! grep -A10 "$fr" docs/PRD.md | grep -q '\- \[ \]'; then
            warn "$fr 缺少验收标准"
        fi
    done < <(grep -oP '### FR-\d+' docs/PRD.md)

    if [ ! -f "docs/ARCHITECTURE.md" ]; then fail "缺少 docs/ARCHITECTURE.md"; else ok "ARCHITECTURE.md 存在"; fi
    echo ""
}

# ─── Gate 2: SDD ───
check_sdd() {
    echo "--- Gate 2: SDD → Feature ---"
    for f in docs/ARCHITECTURE.md docs/API.md docs/conventions.md; do
        if [ ! -f "$f" ]; then fail "缺少 $f"; else ok "$f 存在"; fi
    done

    adr_count=$(ls docs/adr/*.md 2>/dev/null | wc -l || true)
    if [ "$adr_count" -eq 0 ]; then warn "无 ADR 记录"; else ok "ADR 数量: $adr_count"; fi
    echo ""
}

# ─── Gate 3: API ───
check_api() {
    echo "--- Gate 3: API Spec ---"
    if [ ! -f "docs/API.md" ]; then fail "缺少 docs/API.md"; return; fi

    endpoint_count=$(grep -c '^### [A-Z]' docs/API.md || true)
    if [ "$endpoint_count" -eq 0 ]; then warn "API.md 中无 endpoint 定义"; else ok "API endpoint 数量: $endpoint_count"; fi

    # 检查 endpoint 是否有权限标注
    missing_perm=$(grep -B1 '^### [A-Z]' docs/API.md | grep -c '权限' || true)
    if [ "$missing_perm" -lt "$endpoint_count" ]; then
        warn "部分 endpoint 缺少权限标注 ($missing_perm/$endpoint_count)"
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
