#!/bin/bash
# 按包分组跑 Go 竞态检测（R31 的可行运行方式）。
#
# 背景：`go test -race ./...` 在 `internal/controlplane/router` 包上**必然超时**
# （实测：非 race 全包 316s，race 插桩约放大 10 倍 → 约 53 分钟，远超默认 10m 上限；
# 主因是该包 837 处 `setupTestRouter*`/`setupTestDB` 调用点、每次 AutoMigrate 115 个模型，
# 属既有规模问题而非本次引入）。结果是仓库文档化的 race 门禁在**权限/实例端点所在的
# 最大包**上实际从未被执行过。
#
# 本脚本把 race 检测拆成两组，使整仓仍可被覆盖：
#   1) core  ：除 router 外的全部包，默认 10m 时限内可完成；
#   2) router：单独以放宽的时限运行（默认 60m）。
#
# 用法：
#   scripts/go-test-race.sh              # 两组都跑
#   scripts/go-test-race.sh core         # 只跑除 router 外的包
#   scripts/go-test-race.sh router       # 只跑 router 包
#   RACE_TIMEOUT=90m scripts/go-test-race.sh
#   GO_TEST_FLAGS=-cover scripts/go-test-race.sh   # 追加原样透传给 go test 的参数
#
# 退出码：任一组失败即非零（与 `go test` 一致）。
set -uo pipefail

MODE="${1:-all}"
RACE_TIMEOUT="${RACE_TIMEOUT:-60m}"
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
    echo "── race: $label：无包可跑，跳过 ──"
    return 0
  fi
  echo "── race: $label (timeout=$timeout, packages=$#) ──"
  # shellcheck disable=SC2086  # GO_TEST_FLAGS 需按空格拆分为多个参数
  if ! go test -race -count=1 -timeout "$timeout" $GO_TEST_FLAGS "$@"; then
    echo "!! race 组失败: $label" >&2
    fail=1
  fi
}

case "$MODE" in
  core)
    mapfile -t pkgs < <(core_pkgs)
    run_group core "$CORE_TIMEOUT" "${pkgs[@]}"
    ;;
  router)
    run_group router "$RACE_TIMEOUT" "$ROUTER_PKG"
    ;;
  all)
    mapfile -t pkgs < <(core_pkgs)
    run_group core "$CORE_TIMEOUT" "${pkgs[@]}"
    run_group router "$RACE_TIMEOUT" "$ROUTER_PKG"
    ;;
  *)
    echo "用法: $0 [all|core|router]" >&2
    exit 2
    ;;
esac

if [ "$fail" -ne 0 ]; then
  echo "race 门禁未全部通过（详见上方分组输出）" >&2
fi
exit "$fail"
