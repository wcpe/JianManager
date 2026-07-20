#!/usr/bin/env bash
set -euo pipefail

root="$(mktemp -d)"
trap 'rm -rf "${root}"' EXIT

wedge="${root}/wedge.jar"
core="${root}/updater-core.jar"
checker="$(cd "$(dirname "$0")/.." && pwd)/scripts/verify-client-updater-embed.sh"

expect_failure() {
  local expected="$1"
  shift
  local output
  if output="$(${checker} "$@" 2>&1)"; then
    printf '预期校验失败，但命令成功：%s\n' "${expected}" >&2
    exit 1
  fi
  if [[ "${output}" != *"${expected}"* ]]; then
    printf '失败提示不符合预期：%s\n实际输出：%s\n' "${expected}" "${output}" >&2
    exit 1
  fi
}

expect_failure "楔子 jar 缺失或为空" "${wedge}" "${core}"
printf 'wedge' > "${wedge}"
: > "${core}"
expect_failure "updater-core jar 缺失或为空" "${wedge}" "${core}"
printf 'core' > "${core}"
"${checker}" "${wedge}" "${core}"

printf '客户端更新器内嵌资产校验测试通过\n'
