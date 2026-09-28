#!/bin/bash
# 按包分组跑非 race 的全量 Go 测试（真机/Windows 可用的全量门禁运行方式）。
#
# 背景：`internal/controlplane/router` 是既有规模问题包（837 处 `setupTestRouter*`/`setupTestDB`
# 调用点、每次 AutoMigrate 115 个模型）。Windows 真机实测该包**单独**跑 480–520s，已逼近
# `go test` 默认 10m 上限；与其它重包并行时实测 `panic: test timed out after 10m0s`——注意这是
# **包级超时**而非用例失败，超时会把该包里真正的用例失败一起掩盖（本轮真机复验即先撞到它）。
# release 门禁用的 `go test ./...` 没有显式时限，在慢机/满载机器上同样不可靠。
#
# 本脚本把非 race 全量拆成两组，使整仓仍可被覆盖：
#   1) core  ：除 router 外的全部包，默认 10m 时限内可完成；
#   2) router：单独以放宽的时限运行（默认 25m）。
#
# 分组口径与 `scripts/go-test-race.sh` 一致（那支是 -race 版，router 默认 60m）。
#
# 用法：
#   scripts/go-test.sh              # 两组都跑
#   scripts/go-test.sh core         # 只跑除 router 外的包
#   scripts/go-test.sh router       # 只跑 router 包
#   ROUTER_TIMEOUT=40m scripts/go-test.sh
#   GO_TEST_FLAGS=-cover scripts/go-test.sh   # 追加原样透传给 go test 的参数
#
# 退出码：任一组失败即非零（与 `go test` 一致）。
set -uo pipefail

MODE="${1:-all}"
ROUTER_TIMEOUT="${ROUTER_TIMEOUT:-25m}"
CORE_TIMEOUT="${CORE_TIMEOUT:-10m}"
# 额外 go test 参数（如 -cover）。刻意用未加引号的展开以支持多参数，属有意为之。
GO_TEST_FLAGS="${GO_TEST_FLAGS:-}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT" || exit 1

ROUTER_PKG="./internal/controlplane/router/..."

# all_pkgs 列出 `go test` 会覆盖的包（排除本仓不参与单测的第三方目录），
# core_pkgs 为其中剔除 router 子树后的结果。
all_pkgs() { go list ./... | grep -v '/node_modules/'; }
core_pkgs() { all_pkgs | grep -v '^github.com/[^/]*/JianManager/internal/controlplane/router$'; }

fail=0

run_group() {
  local label="$1" timeout="$2"
  shift 2
  if [ "$#" -eq 0 ]; then
    echo "── go test: $label：无包可跑，跳过 ──"
    return 0
  fi
  echo "── go test: $label (timeout=$timeout, packages=$#) ──"
  # shellcheck disable=SC2086  # GO_TEST_FLAGS 需按空格拆分为多个参数
  if ! go test -count=1 -timeout "$timeout" $GO_TEST_FLAGS "$@"; then
    echo "!! go test 组失败: $label" >&2
    fail=1
  fi
}

case "$MODE" in
  core)
    mapfile -t pkgs < <(core_pkgs)
    run_group core "$CORE_TIMEOUT" "${pkgs[@]}"
    ;;
  router)
    run_group router "$ROUTER_TIMEOUT" "$ROUTER_PKG"
    ;;
  all)
    mapfile -t pkgs < <(core_pkgs)
    run_group core "$CORE_TIMEOUT" "${pkgs[@]}"
    run_group router "$ROUTER_TIMEOUT" "$ROUTER_PKG"
    ;;
  *)
    echo "用法: $0 [all|core|router]" >&2
    exit 2
    ;;
esac

if [ "$fail" -ne 0 ]; then
  echo "go test 门禁未全部通过（详见上方分组输出）" >&2
fi
exit "$fail"
