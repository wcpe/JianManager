# FR-475 Windows 平台资产验收记录（受控）

日期：2026-09-24
主机：node-main（生产主机，IP 已脱敏，linux-amd64）
受验包：`victoria-logs-windows-amd64-v1.52.0.zip`（6 127 882 字节）
脚本：`.tmp/fr436-windows/main.go`（不入库）

## 1. 先前误判的更正（重要）

本项此前被登记为「外部阻塞：本机网络不可达 GitHub」，**该判断是错的**——
我只测了 `github.com` 的 releases 下载端点（超时）就下了结论。

实际网络状况（实测）：

| 端点 | 结果 |
|---|---|
| `https://github.com/.../releases/download/...` | ❌ 超时（我据此误判为整体不可达） |
| `https://api.github.com` | ✅ 200 |
| `https://codeload.github.com` | ✅ 301 |
| `https://objects.githubusercontent.com` | ✅ 404（可达） |

**正确路径**：经 GitHub **asset API 端点**下载——
`GET https://api.github.com/repos/VictoriaMetrics/VictoriaLogs/releases/assets/478663512`，
带 `Accept: application/octet-stream`，得到 6 127 882 字节（与 API 报告的 size 完全一致）。

> 教训：判定「网络不可达」必须测足多个端点，不能用单一端点的失败代表整站。

## 2. 验收结果（全部走生产代码路径）

### 2.1 双层哈希与审批值一致

| 项 | 实得 | 审批值（`logasset/approved.go`） |
|---|---|---|
| 包 SHA-256 | `cec110095b02da7f9ed3946d1defacbfc015b4e8ad767659939ee1bcadbf43e4` | `cec11009…43e4` ✅ |
| 解包后可执行 SHA-256 | `858985a6dd387c841990d904c29fb1464bad7f896b225f8238115d707428accd` | `858985a6…accd` ✅ |

包内条目：`victoria-logs-windows-amd64-prod.exe`（15 851 008 字节），独立用 `zipfile` 解出核对，与审批值一致。

### 2.2 生产安装路径（`InstallApprovedPackage`）

```
JIANMANAGER_TEST_VL_ARCHIVE=/tmp/vl-win.zip \
JIANMANAGER_TEST_VL_PLATFORM=windows \
go test ./internal/worker/logs/vlsup/ -run TestInstallApprovedPackageRealArchive
→ --- PASS: TestInstallApprovedPackageRealArchive (0.43s)
```

即真实 Windows 包经**真实 zip 解包 + 双哈希校验**通过（此前该测试只有 Linux 真包数据）。

### 2.3 CP 资产分发链路（windows/amd64）

| 环节 | 结果 |
|---|---|
| `Cache` 接受真实包并落盘 | ✅ `/var/artifacts/log-vl/ce/cec11009….zip` |
| `Cache` 拒绝篡改内容 | ✅ `log-vl package sha256 mismatch` |
| `Open` 读回并校验 | ✅ 返回包哈希与审批值一致 |
| 缓存被改动后 `Open` 拒绝 | ✅ `log-vl cached package sha256 mismatch` |

### 2.4 已审批平台登记

`logasset.Approved("windows", "amd64")` 返回登记项（`victoria-logs-windows-amd64-v1.52.0.zip` /
`victoria-logs-windows-amd64-prod.exe`）。

## 3. 「Windows 运行时行为」的差异面盘点（缩小边界）

规格 §5 把「优雅停机、预算/RSS、进程重启、无 VL 降级」与「包校验、解包校验」并列为 Linux/Windows 双平台要求。
本轮逐项盘点 `vlsup` 的**平台相关代码**，确认差异面远小于「整段运行时」：

| Windows 差异点 | 位置 | 覆盖状态 |
|---|---|---|
| 包格式（zip vs tar.gz） | `install.go:177` | ✅ 已用真实 Windows 包验证（zip 解包 PASS） |
| 可执行文件名与审批哈希 | `approvedPackageSpec`（`install.go:31`） | ✅ 已验证（双层哈希一致） |
| 进程停止信号语义 | `exec.go:79-99` | ✅ 实现**已按平台差异处理**：先 `os.Interrupt`（注释明确「Windows 上可能等价于 Kill」），`grace` 超时后 `Kill()` 兜底——两条路径都收敛，不依赖 POSIX 信号语义 |
| 其余（优雅停机编排、预算/RSS 评估、进程重启、无 VL 降级） | `supervisor.go` / `budget.go` | ✅ **平台无关逻辑**，已有 `supervisor_test.go`（18 例）与 `budget_test.go`（7 例）覆盖 |

**实测平台分支总数**：`vlsup` 包内针对 Windows 的分支仅 **1 处**（`install.go` 的 zip 选择），且已被真实验证。

→ 即：**Windows 特有差异已全部覆盖**；尚未覆盖的不是「不同的逻辑」，而是「在同一份逻辑上于 Windows 主机做一次实机执行」。

## 4. 判定

规格 §5「Linux/Windows **包校验、解包校验**、Apache-2.0 记录…均有实验证据」中，
**Windows 的包校验与解包校验已补上真机证据**（此前只有 Linux 真包 + Windows 合成夹具）。

**仍未覆盖**（如实标注，不作为通过项）：

- **在 Windows 主机上的一次实机执行**。经 §3 盘点，这**不是逻辑差异未覆盖**
  （Windows 特有分支仅 1 处且已验证，其余为平台无关逻辑且有测试覆盖），
  而是「同一份逻辑在 Windows 上的实机部署验证」尚未做——本机 linux-amd64、
  容器（共享宿主内核）、且无 Wine/qemu（均已确认未安装），故无法执行 Windows PE。
- 结论：本记录已覆盖**分发、安装与平台差异逻辑**；剩余项为**实机部署验证**，
  需 Windows 主机。
