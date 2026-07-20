#!/usr/bin/env bash
set -euo pipefail

wedge="${1:-}"
core="${2:-}"

if [[ -z "${wedge}" || ! -s "${wedge}" ]]; then
  printf '错误：客户端更新器楔子 jar 缺失或为空：%s\n' "${wedge:-未提供路径}" >&2
  exit 1
fi

if [[ -z "${core}" || ! -s "${core}" ]]; then
  printf '错误：客户端 updater-core jar 缺失或为空：%s\n' "${core:-未提供路径}" >&2
  exit 1
fi

printf '客户端更新器内嵌资产校验通过：wedge.jar 与 updater-core.jar 均存在且非空\n'
