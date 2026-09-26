# VictoriaLogs 发行资产清单（FR-476 资产审批）

> 受控文档：本清单是 FR-476 / FR-473 §6.6 实验所依据的 VL 资产基线。
> 任一字段与本地包不一致时**不得安装**（Worker `vlsup.VerifyAsset` 双重校验：包 SHA-256 + 解包后可执行 SHA-256）。
> 代码真源：`internal/platform/logasset/approved.go`（`Approved(goos, goarch)`）。

## 1. 审批基线

| 字段 | 值 |
|---|---|
| 上游项目 | VictoriaLogs（VictoriaMetrics）|
| Tag | `v1.52.0` |
| 构建标识（build_id） | `20260716-022147-tags-v1.52.0-0-g46a54c9` |
| 发布提交（release commit） | `46a54c9` |
| 版本串（`victoria-logs-prod --version`） | `victoria-logs-20260716-022147-tags-v1.52.0-0-g46a54c976f` |
| 许可证 | `Apache-2.0` |
| 构建日期 | `2026-07-16` |
| 第三方依赖 | 上游发行物内嵌 182 个 Go module（许可证以上游发行物为准，随包分发） |

## 2. 各平台制品指纹

| 平台 | 下载包 | 包 SHA-256 | 解包可执行 | 可执行 SHA-256 | 兼容测试 |
|---|---|---|---|---|---|
| linux/amd64 | `victoria-logs-linux-amd64-v1.52.0.tar.gz` | `d14f585144b8d6813f15e11f0041f487e15e10e5f5e5a31be0311367e93d3494` | `victoria-logs-prod` | `26941a2f987795dbae465089020b0b592b05718c5f93f4b328b32b429862899b` | 通过（远程 HOT/COLD/Rehydrate 三实例，见 runbook-checklist Runbook C）|
| windows/amd64 | `victoria-logs-windows-amd64-v1.52.0.zip` | `cec110095b02da7f9ed3946d1defacbfc015b4e8ad767659939ee1bcadbf43e4` | `victoria-logs-windows-amd64-prod.exe` | `858985a6dd387c841990d904c29fb1464bad7f896b225f8238115d707428accd` | 通过（解包与可执行校验；包内文件名按 zip 列出）|

## 3. 复现校验

```sh
# 包指纹（与本清单 §2 比对）
sha256sum victoria-logs-linux-amd64-v1.52.0.tar.gz
# 解包后校验可执行文件，并确认版本串
tar xf victoria-logs-linux-amd64-v1.52.0.tar.gz
sha256sum victoria-logs-prod
./victoria-logs-prod --version   # 期望含 20260716-022147-tags-v1.52.0-0-g46a54c976f
```

## 4. 分发与安装规则

- **CP 只分发审批 tag、哈希和许可清单**（FR-476 §2）；CP 不内嵌归档正文。管理员将审批包上传到 CP 资产缓存
  （`POST /api/v1/log-runtime/assets/:os/:arch`，落 `artifacts/log-vl/<sha2>/<sha>.ext`，按 SHA-256 校验），
  Worker 再经签名 token 下载并由 `vlsup.InstallApprovedURL` 安装。
- Worker 安装前必须同时匹配**包 SHA-256**与**解包后可执行 SHA-256**；任一不匹配即拒绝安装并报结构化原因。
- VL 仅监听 localhost，使用独立本地鉴权密钥与受管数据根；不向浏览器暴露（FR-473/ADR-094）。

## 5. 变更控制

- 换用任何其它 VL tag / 构建：必须重跑受影响能力验证（JSON stream 依赖行为、g1→g2 隔离重建、
  Search/Stats/Facets/Export 逻辑集合一致性、双平台包/解包校验）并更新本清单与 `approved.go` 常量。
- 本清单任一字段变更视同契约依赖变更，需同步 `worker-victorialogs-runtime` spec 与 CHANGELOG。
