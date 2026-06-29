# JDK 登记重做（FR-228）

> 状态：开发中 · 增强 FR-072/178（JDK 登记）· **改 proto**（新增 ProbeJDK RPC）

## 1. 需求

「登记已有 JDK」从「手填路径 + 手填厂商/版本/架构」改为**选目录 + 后端探测自动填**（原始诉求 #7）：

1. **路径用模态框选**（不内联动布局）：弹 `Dialog` 承载目录选择器，逐级浏览节点文件系统选定 JDK 目录。
2. **后端探测自动填参数**：选定后经 Worker 在该目录跑 `java -XshowSettings:properties -version` 探测，自动得出厂商/主版本/完整版本/架构——用户**不再手填**。
3. **「标记为 Worker 托管（仅作记录）」复选框置顶**：让用户先决定（影响登记的 `managed` 标记）。

## 2. 设计

### 2.1 ProbeJDK RPC（新）

```proto
rpc ProbeJDK(ProbeJDKRequest) returns (ProbeJDKResponse);
message ProbeJDKRequest { string path = 1; }   // JDK home 目录或 java 可执行文件路径，Worker 归一
message ProbeJDKResponse {
  bool valid = 1; string vendor = 2; int32 major_version = 3;
  string version = 4; string arch = 5; string java_home = 6; string error = 7;
}
```

- Worker：归一 path（以 `bin/java[.exe]` 结尾 → home = `../..`；否则 home = path），复用既有 `detectAt(home)` 探测；找不到 `bin/java` 或取不到版本 → `valid=false` + error。
- 复用 `detectAt`（已存在，FR-072 安装后探测用），不改其逻辑。

### 2.2 CP

- `POST /nodes/:id/jdks/probe {path}` → 调 Worker ProbeJDK，返回 `{valid,vendor,majorVersion,version,arch,javaHome,error}`。节点离线 503、不存在 404。仅平台管理员（与既有 JDK 登记一致）。

### 2.3 前端（NodeJDKPanel 登记 tab 重做）

- **托管复选框置顶**：表单第一项「标记为 Worker 托管（仅作记录）」。
- **选目录（模态）**：「选择 JDK 目录」按钮 → `Dialog` 内嵌 `DirectoryPicker`（浏览节点 FS）；选定 → 调 probe。
- **探测自动填**：probe 成功 → 只读展示厂商/主版本/完整版本/架构 + 归一 home 路径；失败 → 显错误、禁用登记。
- **登记**：用 probe 结果 + 托管标记调既有 `POST /nodes/:id/jdks`（厂商/版本/架构/path=javaHome/managed）。去掉原手填 vendor/major/version/arch 输入与手填 path 输入。

## 3. 验收

- [ ] `POST /nodes/:id/jdks/probe`：有效 JDK 目录/exe → valid + 正确厂商/主版本/版本/架构 + java_home；非 JDK 目录 → valid=false + error；节点离线 503。
- [ ] 前端：托管复选框在最上方；选路径走模态（不内联顶开布局）；选定后自动填参数（只读）、不需手填；探测失败显错误且不能登记；登记成功后列表出现该 JDK（managed 反映复选框）。
- [ ] 后端 ProbeJDK 单测（归一 exe→home / 有效目录 / 非 JDK 目录）+ 前端 tsc/lint/vitest 绿；mock 域补 probe。
- [x] **真机验**：真机选节点 GraalVM 21 目录 → 自动探出 Oracle/21/21.0.4/amd64 → 登记成功（2026-06-30）。

## 3.1 细化（FR-228 续，2026-06-30）

- **登记允许手动输入**：路径除模态选目录外，新增可手输文本框 + 「检测」按钮（手输 / 选目录都走同一 probe）。
- **删除按来源区分**（同步到「运行时与制品」页）：
  - 外部（managed=false）→ **只删记录、不删文件**。`JDKService.Delete` 仅当 `jdk.Managed` 才调 `RemoveJDK`（兼修旧 bug：外部 JDK 路径在托管根外，旧逻辑无条件调 `RemoveJDK` 被 worker 安全校验拒绝 → 外部删除直接失败）。前端轻确认（说明不动磁盘文件）。
  - 内部（managed=true）→ 删记录 + 移除 Worker 文件，前端 `DangerConfirm` confirmText 二次输入「厂商 主版本」。

## 4. 关联

proto `ProbeJDK`；worker `jdk/manager.go`(Probe)、`grpc/server.go`；CP `service/jdk.go`、`router/`；web `components/NodeJDKPanel.tsx`、`components/DirectoryPicker.tsx`、`api/jdks.ts`、`api/nodeRuntime.ts`。
