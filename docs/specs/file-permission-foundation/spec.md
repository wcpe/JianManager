# 功能规格：文件元数据与权限底座

> 状态：开发中　·　关联 PRD：FR-373　·　分支：feature/fr-373-file-permission-foundation  
> 下游：FR-374（导入权限引导）、FR-375（权限列/写预检 UI）、FR-378（Capability 可写/可 chmod）  
> 计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`  
> **不新增 ADR**（在既有文件 gRPC 面上加性扩展权限元数据与单 path chmod；不改三进程边界、不 root/chown）

## 1. 背景与目标

### 问题
- 导入/浏览节点目录时 `permission denied` 只透出裸 errno，用户不知道「谁不能读、能否修」。
- 实例文件列表 `FileInfo` 仅有 `name/isDir/size/modTime`，前端无法展示权限、写前无法预检。
- 写 `server.properties` 等配置失败才暴露无写权限，体验差且导入后易「半成功」。

### 目标
在 **Worker 进程用户** 视角提供：
1. 列表/浏览条目上的 **mode 展示 + readable/writable**（及可取时的属主信息）；
2. **写前可写探测**（单 path / 可选批量）；
3. **可选尝试修复**：单 path 非递归 `chmod`（二次确认由上层 UI 做）；
4. **中文权限诊断**，替代裸 `permission denied`。

属阶段：P0（阻塞 FR-374/375）。

## 2. 需求（要什么）

### 范围内
1. **扩展 `FileInfo`（实例文件 ListFiles）**  
   - `mode`：展示用字符串（Unix 优先 `rwxr-xr-x` 或八进制 `0755` 二选一，**本 spec 定：`modeOctal` 字符串如 `"0644"` + `modeString` 如 `"rw-r--r--"`**；Windows 无 POSIX mode 时 `modeString` 用简化语义或空，仍填 `readable`/`writable`）。  
   - `readable` / `writable`：相对 **当前 Worker 进程有效用户** 能否读/写该条目（目录「写」= 能否在该目录创建/删除子项的探测语义，见 §3）。  
   - `owner` / `group`：能取则取（Unix uid→名，失败则数字串）；Windows 可填账户名或空。  
   - 字段均为加性，旧客户端忽略未知字段不崩。

2. **扩展 `BrowseDirEntry`（节点 BrowseDir）**  
   - 同上：`modeOctal` / `modeString` / `readable` / `writable` / `owner`（可选 `group`）。  
   - 目录本身不可读时：`BrowseDir` 返回 **success=false + 中文 error**（含建议动作），不得空列表冒充空目录。

3. **写前预检 RPC/HTTP**  
   - Worker：`CheckPathAccess(instance_uuid?, path, absolute?)`  
     - 实例内路径：相对工作目录（与现有 files 一致，`validatePath`）。  
     - 节点绝对路径：供导入/BrowseDir 场景（**仅 BrowseDir 已允许的绝对路径语义**，不扩大路径穿越面；实例内调用不得用绝对路径绕过 workDir）。  
   - 响应：`{ readable, writable, exists, isDir, modeOctal?, modeString?, owner?, reason? }`；`reason` 为中文诊断（不可写/不存在/越界等）。

4. **Chmod 修复 RPC/HTTP**  
   - Worker：`ChmodPath(...)`：单 path、**禁止递归**；模式为显式 octal 或「u+rwX」语义（实现可映射为：文件 `u+rw`、目录 `u+rwx` 最小集合）。  
   - 仅当 Worker 用户对目标有 `chmod` 能力时成功；失败返回中文原因（EPERM/只读挂载等）。  
   - **不做**：chown、递归、sudo/root、Windows ACL 细改、SELinux。

5. **错误中文化**  
   - `ListFiles` / `ReadFile` / `WriteFile` / `BrowseDir` / `InspectServerDir` 等路径上，将 `permission denied` / `EACCES` / `EPERM` 映射为稳定中文文案，并附简短建议（换路径 / migrate 进托管区 / 用正确用户跑 Worker / 尝试 chmod）。  
   - gRPC error 与 BrowseDir `error` 字段均适用；CP 透传不吞掉。

6. **CP HTTP 面**  
   - `GET /instances/:id/files` 响应条目扩字段。  
   - `GET /nodes/:id/browse-dir`（或现有 BrowseDir 路由）条目扩字段。  
   - 新增：  
     - `POST /instances/:id/files/check-access` `{ path }`  
     - `POST /instances/:id/files/chmod` `{ path, mode? }`（mode 省略=平台默认 u+rwX 语义）  
     - 节点级（导入用）：`POST /nodes/:id/fs/check-access` `{ path }`（绝对路径）  
     - 节点级：`POST /nodes/:id/fs/chmod` `{ path, mode? }`  
   - 鉴权：与现有文件/节点浏览同级（实例 CanAccess + 平台管理节点操作，跟现有 BrowseDir/文件写一致，不新开 RBAC 角色）。

7. **前端类型**  
   - `api/files.ts` `FileInfo` 与 `nodeRuntime` BrowseDir 类型同步扩字段；本 FR **不强制** 做完整权限列 UI（那是 FR-375），但类型与 hooks 就绪，便于下游。

### 不做（范围外）
- chown / 递归 chmod / root / sudo / Windows ACL 编辑 / SELinux。  
- 导入向导 UI、资源管理器权限列与滚动/历史（FR-374/375）。  
- 统一壳 Capability 全场景接入（FR-378）。  
- 改删除/备份语义；不扩大实例路径越出 workDir。

## 3. 设计（怎么做）

### 3.1 可写/可读探测口径（Worker）

相对 **Worker 进程 euid/用户**：

| 对象 | readable | writable |
|---|---|---|
| 文件 | 能否 `os.Open` 只读打开 | 能否以写模式打开（不截断内容的探测：`O_WRONLY` 打开后立即 close；或 `unix.Access(W_OK)`，实现选一种并单测锁死） |
| 目录 | 能否 `ReadDir` | 能否在目录内创建临时文件并删除（`os.CreateTemp` + remove），失败则 false |

- 探测失败（不存在）→ `exists=false`，readable/writable=false，reason 说明。  
- 路径越界 → 与现有 `validatePath` 一致，拒绝。  
- Windows：用 `os.OpenFile` 探测；modeString 可空或填 `-----`；modeOctal 可空。

### 3.2 chmod 口径

- 输入：path + 可选 `mode` 八进制字符串（如 `"0644"` / `"0755"`）。  
- 省略 mode：文件 `0644` 基础上 **保证 owner 可读写**（`u+rw`）；目录 **保证 owner 可读写执行**（`u+rwx`）。不降低 other 位除非实现简单到整体 `chmod` 为固定 mask——**推荐**：读取现 mode，OR 上 `0o600`（文件）或 `0o700`（目录），再 `os.Chmod`。  
- 禁止：`mode` 含递归标志；path 为符号链接时 chmod **跟随/不跟随** 与 Go `os.Chmod` 默认一致，spec 注明。  
- 审计：CP 对 chmod 记审计 action `file.chmod` / `node.fs.chmod`（中英 `audit.actions.*` 随本 FR）。

### 3.3 Proto 变更（加性）

```protobuf
message FileInfo {
  string name = 1;
  bool is_dir = 2;
  int64 size = 3;
  int64 mod_time = 4;
  string mode_octal = 5;   // "0644"，可空
  string mode_string = 6;  // "rw-r--r--"，可空
  bool readable = 7;
  bool writable = 8;
  string owner = 9;        // 可空
  string group = 10;       // 可空
}

message BrowseDirEntry {
  string name = 1;
  string path = 2;
  string mode_octal = 3;
  string mode_string = 4;
  bool readable = 5;
  bool writable = 6;
  string owner = 7;
  string group = 8;
}

rpc CheckPathAccess(CheckPathAccessRequest) returns (CheckPathAccessResponse);
rpc ChmodPath(ChmodPathRequest) returns (ChmodPathResponse);

message CheckPathAccessRequest {
  string instance_uuid = 1; // 空=节点绝对路径模式
  string path = 2;          // 实例相对 或 绝对
}
message CheckPathAccessResponse {
  bool success = 1;
  string error = 2;
  bool exists = 3;
  bool is_dir = 4;
  bool readable = 5;
  bool writable = 6;
  string mode_octal = 7;
  string mode_string = 8;
  string owner = 9;
  string group = 10;
  string reason = 11;       // 中文诊断
}

message ChmodPathRequest {
  string instance_uuid = 1;
  string path = 2;
  string mode = 3;          // 可选八进制
}
message ChmodPathResponse {
  bool success = 1;
  string error = 2;
  string mode_octal = 3;    // 修改后
}
```

- `instance_uuid` 空且 path 为绝对：仅允许 **与 BrowseDir 相同的节点路径**（不进托管区自吞逻辑之外的任意系统路径——与 BrowseDir 守卫一致即可）。  
- 实例模式：强制 `validatePath(workDir, join(workDir, path))`。

### 3.4 模块落点

| 层 | 改动 |
|---|---|
| `proto/worker.proto` + 生成 | FileInfo/BrowseDirEntry 扩字段；CheckPathAccess/ChmodPath |
| `internal/worker/grpc/file_ops.go`（或新 `perm_ops.go`） | 填充元数据、探测、chmod、错误映射纯函数可测 |
| `internal/worker/grpc` BrowseDir 实现处 | 条目填充 + 目录不可读中文 error |
| `internal/controlplane/service/file.go` + `node_runtime.go` | 透传字段与新方法 |
| `internal/controlplane/router/file.go` + node 路由 | 新 HTTP 端点 |
| `apps/control-plane-web/src/api/files.ts` + `nodeRuntime.ts` | 类型与 API 函数 |
| 单测 | Worker 纯函数 + tmp 目录真权限场景（可读不可写目录等） |

### 3.5 错误映射（示例文案）

| 条件 | 文案要点 |
|---|---|
| EACCES 读目录 | `没有权限读取该目录（Worker 用户无法列出内容）。可换路径、改用「搬进托管区」，或以有权限的用户运行 Worker；若属主是你，可尝试「修复权限」。` |
| EACCES 写文件 | `没有权限写入该文件。请检查属主/只读挂载，或尝试「修复权限」。` |
| chmod EPERM | `无法修改权限：Worker 用户不是属主或文件系统不允许 chmod。` |

## 4. 任务拆分

- [x] Proto：扩 `FileInfo`/`BrowseDirEntry` + `CheckPathAccess`/`ChmodPath`；`scripts/proto-gen` 重生成  
- [x] Worker：元数据填充 + 可写/可读探测纯函数 + 单测（tmp 文件树）  
- [x] Worker：`ListFiles`/`BrowseDir` 接线；读失败中文错误  
- [x] Worker：`ChmodPath`（非递归）+ 单测  
- [x] CP service/router：字段透传 + check-access/chmod HTTP + 审计  
- [x] 前端类型与 API 函数（不做完整 UI）  
- [x] 文档：`docs/API.md`、`docs/ARCHITECTURE.md` 文件章节、`CHANGELOG` Unreleased、PRD FR-373 → 开发中  
- [x] 本地全栈（Windows 本机独立端口 CP+Worker HTTP）：browse 200 + 权限字段；check-access exists/readable/writable；chmod 返回 modeOctal — 证据 `.tmp/acceptance-fr373-local-2026-07-23.md`（2026-07-23）
- [ ] Linux 真机（可选/用户确认）：无权限目录 BrowseDir 中文错误；0444 与 WriteFile 一致性（本机 Windows 难造不可读目录，`TestBrowseDir_ChineseErrorOnUnreadable` 在 Windows skip）

## 5. 验收标准

1. **加性兼容**：旧前端不识别新字段仍可列表；新字段在有权限目录有合理 mode/readable/writable。  
2. **探测一致**：真机建「仅可读文件」与「可写文件」，`CheckPathAccess`/`ListFiles` 的 writable 与随后 `WriteFile` 成败一致。  
3. **BrowseDir 无权限**：对 Worker 不可读目录返回 success=false + 中文 error，非空 dirs 冒充。  
4. **chmod**：对 Worker 属主文件，省略 mode 的修复后 writable=true 且 WriteFile 成功；对无权限 path 失败文案可诊断；**不**递归改子树。  
5. **越界**：实例相对路径 `../` 仍拒绝。  
6. **自动化**：Worker 权限探测与 chmod 单测全绿；`go test` 相关包绿。  
7. **真机（需用户确认）**：至少一条 Linux 节点上不可读 home 子目录 / 不可写 properties 的复现与修复或明确失败提示。

## 6. 风险 / 待定

- **Windows 权限语义弱**：mode 展示可空，以 readable/writable 为准；真机以 Linux 节点为主验收。  
- **探测副作用**：目录可写探测会创建临时文件，需保证删除；并发目录下极低概率名冲突（`CreateTemp` 规避）。  
- **chmod 安全**：仅实例 workDir 内或 BrowseDir 已允许的绝对路径；管理员误点修复由 FR-375 二次确认兜底，本 FR API 仍须校验路径。  
- **与 FR-374 边界**：本 FR 提供节点 check-access/chmod API；向导文案与按钮在 374。
