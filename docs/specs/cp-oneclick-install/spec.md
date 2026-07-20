# 功能规格：Control Plane 一键下载安装脚本

> 状态：开发中　·　关联 PRD：FR-355

## 1. 目标

提供与 Worker 安装脚本同级的 CP 下载入口：OS/arch 探测、GitHub Releases、失败可诊断。

## 2. 需求

- `scripts/install-cp.sh` + `install-cp.ps1`
- 不做服务化

## 3. 设计

- 资产名 `control-plane-<os>-<arch>[.exe]`
- `INSTALL_CP_TEST=1` 自检；失败 exit 1

## 4. 任务

- [x] 脚本实现
- [x] 自检与失败路径
- [x] README/DEPLOY 引用

## 5. 验收

1. 自检表全 OK — **已验** `INSTALL_CP_TEST=1`
2. 伪造 download-url → 中文错误 + exit 1 — **已验**

证据：`.tmp/acceptance-deploy-readme-security-2026-07-21.md`
