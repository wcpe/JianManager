# API Spec — FR-075 归档浏览与反编译

> 关联 FR: FR-075 | 优先级: P1 | 依赖: FR-070 | 关联 ADR: ADR-018

## 概述

在 FR-070 资源管理器上叠加两类能力：

1. **打开归档（jar/zip）浏览内部**：Worker 用 Go `archive/zip` 列条目树、流式读内部文本条目到只读编辑器；资源管理器目录树支持把 `.jar`/`.zip` 展开为归档子树（懒加载，不真解压、零落盘）。
2. **反编译 class/jar**：Worker 经实例绑定 JDK（或系统 JDK）跑 CFR 单 jar，把 class/jar 反编译为 Java 源码流到只读编辑器；超时 + 体积上限 + 失败降级 + 受控 exec（不运行目标代码）。

严守架构不变量：列举/反编译由 CP 经 gRPC 委托 Worker（CP 不直接碰节点文件/进程）。归档浏览不依赖 JDK；反编译需 JDK，无则降级。

---

## 边界与安全（ADR-018）

- **路径**：归档路径与目标路径经既有 `validatePath` 防工作目录越界；zip 条目名经 zip-slip 校验（拒 `..`/绝对路径条目）。
- **只读**：归档浏览只读不落盘；反编译只读——CFR 仅静态分析字节码输出源码，不加载/运行目标 class、不写工作目录、不联网执行。
- **超时**：反编译 `context.WithTimeout`（默认 30s）。
- **体积上限**：归档单条目读取截断（默认 4 MiB）；反编译输入 class/jar 字节上限（默认 16 MiB）、输出源码截断（默认 4 MiB，`truncated=true`）。
- **CFR 分发**：配置路径 > 内嵌（`make embed-cfr`，gitignore 不入库）> 数据根缓存 `var/tools/cfr-<ver>.jar` > Maven Central 按需下载（sha256 pin 校验）。

---

## gRPC（proto/worker.proto，加性新增，protoc 重新生成）

### rpc ListArchiveEntries(ListArchiveEntriesRequest) returns (ListArchiveEntriesResponse)

列出归档（jar/zip）内全部条目（扁平，前端按「/」重建子树）。

```proto
message ListArchiveEntriesRequest {
  string instance_uuid = 1;
  string path = 2;          // 相对工作目录的归档文件路径（.jar/.zip）
}
message ArchiveEntry {
  string name = 1;          // 归档内条目名（「/」分隔；目录条目以「/」结尾）
  bool is_dir = 2;
  int64 size = 3;           // 解压后字节
  int64 compressed_size = 4;
  int64 modified = 5;       // Unix 秒
  uint32 crc32 = 6;
}
message ListArchiveEntriesResponse {
  repeated ArchiveEntry entries = 1;
  bool truncated = 2;       // 条目数超上限被截断
}
```

### rpc ReadArchiveEntry(ReadArchiveEntryRequest) returns (ReadArchiveEntryResponse)

读取归档内某条目内容（流式截断到上限）。

```proto
message ReadArchiveEntryRequest {
  string instance_uuid = 1;
  string path = 2;          // 归档文件路径
  string entry = 3;         // 归档内条目名
}
message ReadArchiveEntryResponse {
  bytes content = 1;        // 内容（截断到上限）
  bool truncated = 2;
  bool binary = 3;          // 嗅探为二进制（前端不文本预览）
}
```

### rpc DecompileClass(DecompileClassRequest) returns (DecompileClassResponse)

反编译工作目录内 class/jar，或归档内某 class，输出 Java 源码。

```proto
message DecompileClassRequest {
  string instance_uuid = 1;
  string path = 2;          // 工作目录内 .class 或 .jar 路径
  string entry = 3;         // 可选：当 path 为 .jar 时，jar 内某 .class 条目；空=反编译整个 jar
}
message DecompileClassResponse {
  bool success = 1;
  string error = 2;         // 失败/降级原因（无 JDK / 无 CFR / 超时 / 超限 / CFR 非 0 退出）
  string source = 3;        // 反编译 Java 源码（截断到上限）
  bool truncated = 4;
  string decompiler = 5;    // 反编译器标识（如 "CFR 0.152"）
}
```

---

## REST API（CP，挂 /instances/:id/files 组下，加性追加）

> 权限：归档浏览与反编译均为只读，复用文件「查看」级（`canAccessInstance`）。

### GET /api/v1/instances/:id/files/archive/entries

- **描述**: 列出某归档（jar/zip）内条目
- **关联 FR**: FR-075
- **权限**: 实例查看
- **查询参数**: `path`（归档文件相对路径，必填）
- **响应** (200):
  ```json
  {
    "entries": [
      { "name": "plugin.yml", "isDir": false, "size": 320, "compressedSize": 210, "modified": 1700000000, "crc32": 123456 },
      { "name": "META-INF/", "isDir": true, "size": 0, "compressedSize": 0, "modified": 0, "crc32": 0 }
    ],
    "truncated": false
  }
  ```
- **错误**: 400 INVALID_REQUEST（缺 path）| 404 NOT_FOUND（无权/实例不存在）| 422 BUSINESS_ERROR（非归档/越界/节点未连接）

### GET /api/v1/instances/:id/files/archive/read

- **描述**: 读取归档内某条目内容（文本预览）
- **关联 FR**: FR-075
- **权限**: 实例查看
- **查询参数**: `path`（归档文件，必填）、`entry`（归档内条目名，必填）
- **响应** (200): `application/octet-stream`（条目原始字节，截断到上限）；响应头 `X-Truncated: true|false`、`X-Binary: true|false`
- **错误**: 400 INVALID_REQUEST | 404 NOT_FOUND | 422 BUSINESS_ERROR

### POST /api/v1/instances/:id/files/decompile

- **描述**: 反编译工作目录内 class/jar（或归档内某 class）为 Java 源码
- **关联 FR**: FR-075
- **权限**: 实例查看
- **请求体**:
  ```json
  { "path": "plugins/Foo.jar", "entry": "com/example/Foo.class" }
  ```
  （`entry` 可选；`path` 为 `.class` 时忽略 `entry`；`path` 为 `.jar` 且 `entry` 空时反编译整个 jar）
- **响应** (200):
  ```json
  {
    "success": true,
    "source": "/*\n * Decompiled with CFR 0.152.\n */\npublic class Foo { ... }\n",
    "truncated": false,
    "decompiler": "CFR 0.152"
  }
  ```
- **降级响应** (200, `success:false`): `{ "success": false, "error": "无可用 JDK，反编译降级" }`
- **错误**: 400 INVALID_REQUEST（缺 path）| 404 NOT_FOUND | 422 BUSINESS_ERROR（越界/节点未连接）

---

## 前端（复用 FR-070 资源管理器）

- `FileTree`：`.jar`/`.zip` 节点可展开为归档子树（懒加载 `ListArchiveEntries`，按「/」重建层级）；内部条目点选触发只读查看。
- 只读查看：归档内文本条目 → `CodeEditor`（`readOnly`）；`.class`（工作目录或归档内）/`.jar` → 「反编译」动作 → 只读 `CodeEditor`（Java 高亮）展示 CFR 源码，含加载/降级/截断态。
- API client：`web/src/api/archive.ts` 新增 `listArchiveEntries` / `readArchiveEntry` / `decompile`。

---

## 验收标准映射（PRD FR-075）

| 验收标准 | 落地 |
|---|---|
| 先写 ADR-018（CFR 打包/调用；只读+超时+体积上限+受控 exec） | `docs/adr/018-decompiler-integration.md` |
| 打开 jar/zip 列条目树；查看内部文本流式到只读编辑器；树内展开归档为子树 | `ListArchiveEntries`/`ReadArchiveEntry` + `FileTree` 归档子树 + 只读 `CodeEditor` |
| 反编译 class/jar：经实例 JDK 跑 CFR → 源码流（超时+体积上限+失败降级） | `DecompileClass` + CFR 受控 exec + 降级 |
| 真机：打开真 plugin jar、看内部 plugin.yml、反编译 class 出源码 | 真机验（环境有 JDK） |
