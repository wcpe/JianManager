# FR-533 Beacon 配置纳管（非数据迁移）

> 状态：草拟 · 优先级：P1 · 依赖：FR-529~532 全部完成
> **2026-10-24 重大修订**：原设计为「Beacon 导出 → JM 导入」的数据搬运，用户澄清「文件都是已经有的，重新读取就行」，故改为**纳管现有文件**。

## 1. 背景与修订说明

### 1.1 原设计的错误假设

原 spec 假设 Beacon 里存着配置内容，需要导出/导入来搬运数据。这个假设**不成立**：

- 配置文件的**真正落点是服务器磁盘**（`plugins/Foo/config.yml` 等）
- Beacon 的 `config_layer_version` 存的是"渲染后的内容"，但它只是**副本**
- 迁移时搬运副本毫无意义——磁盘上的文件才是事实

### 1.2 正确的做法

**JM 直接扫描并纳管现有文件**，把磁盘上的实际状态作为基线，之后由 JM 接管版本管理与下发。

这带来的好处：

| 原设计 | 修订后 |
|---|---|
| 需要 Beacon 导出接口（FR-282） | **不需要**（Beacon 导出可保留作审计备份） |
| 需要数据格式映射（namespace→group 等） | **不需要** |
| 迁移期间双写风险 | **不存在**（JM 只读磁盘，不写） |
| 需要 2 周回退窗口 | **不需要**（未改动任何数据） |

### 1.3 一个仍然需要的东西

Beacon 里的**分层意图**（哪个值属于大区、哪个属于小区）无法从磁盘反推——磁盘上只有合并后的最终值。

所以：**分层结构需要人工重建，但内容不需要搬运。**

## 2. 目标

- 把磁盘上的现有配置文件纳管为 JM 的 `config_file` + 基线版本
- 不搬运、不改写任何文件内容
- 让运维可以在此基础上创建分层覆盖

## 3. 非目标

- 不导入 Beacon 的分层版本链（磁盘上没有这个信息）
- 不导入 Beacon 的变更历史与审计
- 不修改任何磁盘文件

## 4. 纳管流程

### 4.1 第一步：发现（复用现有能力）

`GET /api/v1/instances/:id/configs/discover` — **已实现**（`ConfigService.Discover`，FR-071）

递归扫描实例工作目录，返回：
```json
{
  "files": [
    { "path": "server.properties", "format": "properties", "supported": true },
    { "path": "plugins/Foo/config.yml", "format": "yaml", "supported": true },
    { "path": "plugins/Bar/data.db", "format": "", "supported": false }
  ],
  "truncated": false
}
```

`truncated=true` 表示扫描被限制截断（防大目录爆量）。

### 4.2 第二步：纳管（新增）

```http
POST /api/v1/config-files/adopt
```

请求：
```json
{
  "files": [
    { "instanceId": 5, "path": "plugins/Foo/config.yml", "asName": "plugins/Foo/config.yml" },
    { "instanceId": 5, "path": "server.properties" }
  ],
  "scope": { "type": "instance", "id": 5 },
  "createBaseline": true,
  "dryRun": true
}
```

**行为**：

1. 逐文件从目标实例**读取当前内容**（`Worker.ReadFile`，不写）
2. 计算 sha256
3. 创建 `config_file`（若 `name` 已存在则复用）
4. 在指定作用域创建 `config_layer_version`（v1，内容=磁盘实际内容）
5. `dryRun=true` 时只报告不写入

**关键**：读到的内容**必须与磁盘完全一致**（不重新格式化、不排序键）——因为这是"记录现状"，不是"规范化"。

### 4.3 第三步：按需建分层（人工）

纳管后，运维可以在 JM 里：

- 创建分组层版本，覆盖差异值
- 预览有效值，确认与磁盘一致
- 之后由 JM 接管下发

**这一步是人工决策**，因为只有人知道"这个值本来就该全区一致"还是"这个大区特殊"。

## 5. 批量纳管

100+ 实例逐个纳管太慢，需要支持按分组批量：

```json
{
  "target": { "groupIds": [3], "includeDescendants": true },
  "pathPatterns": ["plugins/*/config.yml", "server.properties"],
  "createBaseline": true,
  "dryRun": true
}
```

**行为**：
- 对目标集内每个实例，匹配 `pathPatterns` 的配置文件
- 逐实例纳管（内容可能各不相同，各存各的 v1）
- 返回逐实例结果

**注意**：批量纳管后，同名 `config_file` 会有 N 个实例级 v1 版本（内容可能不同）。这是**正常且正确的**——JM 记录的是真实状态，不是假设一致。

## 6. 冲突处理

| 情况 | 行为 |
|---|---|
| `config_file` 已存在且已有版本 | 跳过，报告 `ALREADY_ADOPTED`（不覆盖） |
| `config_file` 已存在但无版本 | 补建 v1 |
| 文件不存在 | `FILE_NOT_FOUND` |
| 文件无权限读 | `READ_FAILED` |
| 实例离线 | `INSTANCE_OFFLINE` |

**`ALREADY_ADOPTED` 不覆盖**：防止误操作把运维后续做的修改冲掉。要更新内容请走正常的编辑流程。

## 7. 与 Beacon 的关系

| Beacon 的东西 | 处置 |
|---|---|
| 磁盘上的配置文件 | **纳管**（本 FR） |
| Beacon 的分层版本链 | 不导入（磁盘无此信息） |
| Beacon 的变更历史/审计 | 不导入（保留在 Beacon 侧供查询） |
| Beacon 导出接口（FR-282） | 保留，但仅作**审计备份**用途，不再是迁移主路径 |

**Beacon 侧开关**：JM 纳管完成后，把 Beacon 的配置功能关掉（FR-281 的开关置 false）即可。**不需要数据迁移，因为文件本来就在磁盘上。**

## 8. 接口

```http
GET  /api/v1/instances/:id/configs/discover      已有（FR-071）
POST /api/v1/config-files/adopt                  新增：纳管指定文件
POST /api/v1/config-files/adopt/preview          新增：预演（等价 dryRun，但独立端点便于前端）
```

## 9. 验收标准

### 纳管正确性

1. 纳管后 `config_file` 内容与磁盘**逐字节一致**（不重新格式化）
2. `content_hash` 与磁盘文件 sha256 一致
3. `dryRun` 时不产生任何写入
4. 重复纳管同一文件返回 `ALREADY_ADOPTED` 且不覆盖
5. 文件不存在/无权限时返回明确错误码

### 批量

6. 按分组批量纳管 100 实例，逐实例结果正确
7. 同名文件在不同实例内容不同时，各自独立存 v1（不假设一致）
8. 部分实例离线时，其余实例正常纳管（不整体失败）

### 边界

9. **不修改任何磁盘文件**（纳管前后文件 mtime 不变）
10. 纳管后能用 JM 的配置预览正确读出内容
11. 纳管后能在此基础上创建分层覆盖并正确合并

### 规模

12. 单个实例扫描 + 纳管 50 个文件 < 10 秒
13. 100 实例批量纳管不阻塞其他请求

## 10. 实现后补充（2026-10-24）

### 10.1 错误码补全（spec §6 原缺）

| 码 | 场景 |
|---|---|
| `FORMAT_UNSUPPORTED` | `Discover` 会返回 `.toml`/`.txt`/`.conf`，但 `config_file` 只支持 yaml/json/properties。**显式报错而非硬塞成 yaml** |
| `CONFIG_FILE_ARCHIVED` | 同名 `config_file` 在回收站（软删） |
| `FORMAT_CONFLICT` | 同名但 format 已定且不同（format 创建后不可改） |

### 10.2 「已纳管」的判定维度（重要）

**必须按 `(config_file, 实例作用域)` 判定，不能按 `config_file` 是否存在判定。**

否则：第一个实例纳管创建了 `config_file`，第二个实例就永远纳不进去——但 spec §5 明确要求「同名文件在不同实例各自独立存 v1」。

### 10.3 `INSTANCE_OFFLINE` 的口径

定为**节点未连接**，而非实例状态非 RUNNING。

理由：纳管读的是磁盘文件，实例停止时文件还在。**维护窗口恰恰是最该纳管的时候**。

### 10.4 基线入库不走 `SaveLayer`

`SaveLayer` 会做语法 + schema 校验，坏配置会被拒绝。但纳管语义是「记录现状」——**坏掉的配置更需要被记录**，否则运维看不到它。

改用底层 `appendLayer`，解析失败只作 `warning` 带出，不阻断纳管。

### 10.5 ⚠️ 一个必须明示的语义（最容易误解）

**实例层基线的优先级最高，分组层无法覆盖它。**

作用域链是 `[group:root, ..., group:parent, instance:self]`，实例层在**最右侧（最高优先级）**。

后果：纳管后如果只是「建一个分组层去改某个值」，**不会生效**——因为实例层的值压在上面。

要让分组层生效，必须二选一：
- 删除该实例层的版本
- 或者直接改实例层

**这是 FR-529 的既有语义，不是 bug**，但与运维的直觉相反（通常以为"分层是叠加的，上层能覆盖下层"）。

**必须在 UI 与文档里明示这条**，否则运维纳管 100 台后会困惑"为什么改了分组层没反应"。

### 10.6 并发策略

- **读并发**（默认 8）：瓶颈在 gRPC 往返
- **落库串行**：`config_file.name` 与 `(config_file_id, scope_level, scope_ref_id, version_no)` 都是唯一索引，并行写必撞

## 11. 待定项与实现落地

| # | 待定项 | 落地决定 |
|---|---|---|
| 1 | `pathPatterns` 通配语法 | **逐段 glob + `**` 跨层 + 大小写不敏感**（`matchConfigPath`）。段内复用 `path.Match`（`*` `?` `[...]`），单独一段 `**` 表示零到多段。大小写不敏感是因为 MC 插件配置大小写混用极常见（`config.yml` / `Config.YML`）。非法模式（括号不闭合）不匹配任何路径，不退化成「匹配全部」 |
| 2 | 是否自动推断 `format` | **是**，按扩展名（`.properties` / `.yml` `.yaml` / `.json`）。注意与 `Discover` 的口径差异：`Discover` 会返回 `toml`/`txt` 供展示，但 `config_file` 只支持 yaml/json/properties，故 `.toml`/`.txt`/`.conf`/无扩展名报 `FORMAT_UNSUPPORTED`——**不**硬塞成 yaml（否则后续解析全错） |
| 3 | 纳管 v1 的 `remark` | **标注来源**：`纳管自磁盘 <实例名>/<路径> @<RFC3339>`，按字符边界截断到 255 |
| 4 | 是否要「纳管后立即预览合并结果」的一步式接口 | **不做**。纳管与预览是两个语义（记录现状 vs 解释现状），合并成一步会让「纳管失败了但预览成功了」这类部分成功难以表达；前端两次调用即可 |
| 5 | `health` 自检的判定依据 | **只报 JM 侧事实，不做自动判定**。已核实 `beacon_push.go` 只推送实例注册（`/beacon/v1/agent/register`），全文件无配置内容推送逻辑，按「Beacon 同步开着」告警会把正常拓扑同步误报成配置双写风险。返回 `configFileCount` / `layerVersionCount` / `lastWriteAt` + 固定提示文案 |

### 10.1 实现要点（2026-10-25）

**逐字节保真的保证方式**：直接用 `Worker.ReadConfig` 返回的 `Content`（Worker 侧是 `os.ReadFile` 后转 string），**不经过** `ParseConfigContent` / `MergeConfigLayers` / `patchConfig` 任何一条会重新序列化的路径。`Read` 返回的 `Fields` / `Validation` / `SchemaJSON` 是解析产物，纳管一律不用。`content_hash` 由 `appendLayer` 内的 `hashContent(content)` 计算，与磁盘 sha256 天然相同。

**「不修改磁盘」的保证方式**：`ConfigAdoptSource` 接口只声明 `Discover` / `Read` 两个只读方法，生产实现是 `*ConfigService`。这不是约定而是编译期事实——纳管服务根本拿不到写方法。

**基线入库走 `appendLayer` 而非 `SaveLayer`**：`SaveLayer` 会先做语法与 schema 校验，一份语法有问题的 YAML 会被拒绝入库。但纳管的语义是「记录磁盘现状」——坏掉的配置恰恰最需要被记录，否则运维永远无法用 JM 把它修好。因此纳管只校验作用域引用存在，内容原样入库，解析失败仅作为 `warning` 字段带出。

**并发策略（读并发 / 写串行）**：读磁盘（gRPC 往返）是瓶颈，按实例分片并发（默认 8）；落库段用互斥锁串行，因为 `config_file.name` 与 `(config_file_id, scope_level, scope_ref_id, version_no)` 都是唯一索引，并行写必然撞索引。落库是本地毫秒级操作，串行不牺牲吞吐。

**「已纳管」的判定维度是 (config_file, 实例作用域) 而非 config_file 是否存在**：同名 config_file 可被多个实例共享，每个实例各有独立实例层版本链（§5）。若按 config_file 存在判定，第二个实例永远纳管不进去。

**离线口径是「节点未连接」而非「实例状态非 RUNNING」**：纳管读的是磁盘文件，实例停止时文件依然在，恰恰维护窗口才是最需要纳管的时机。

## 12. 与旧设计的关系

原 spec 的以下内容**作废**：

| 原内容 | 状态 |
|---|---|
| §3 三阶段迁移（并行观察→Beacon只读→下线） | ❌ 作废（无数据搬运，不需要阶段） |
| §4 双写禁止机制 | ⚠️ 部分保留（JM 补 `health` 自检仍有价值，但理由变成"防误操作"） |
| §5 数据导入格式（tar.gz） | ❌ 作废 |
| §5.1 映射规则（Beacon 层级→JM 分组） | ❌ 作废（无数据可映射） |
| §5.2 namespace 提升为根分组 | ❌ 作废（无 namespace 数据） |
| §7 回退方案 | ❌ 作废（未改动任何数据，无需回退） |

**保留但简化**：`GET /config-files/health` 自检仍有价值，但**不能自动判定双写**。

### 已核实的约束（2026-10-24）

现有 `beacon_push.go` **只推送实例注册**（`BeaconAPIPathAgentRegister = "/beacon/v1/agent/register"`），
全文件无任何配置推送逻辑。

因此：
- ❌ **不能用 `beaconPushEnabled` 当告警依据**——那会把正常的拓扑同步误报成"配置双写风险"
- ❌ JM **无法从自身探测 Beacon 侧的开关状态**（Beacon 在另一个系统）
- ✅ 正确做法：`health` 只报告 JM 侧事实（配置规模、最近写入时间），附一句人工核对提示，**不做自动判定**

响应示例：
```json
{
  "configFileCount": 42,
  "layerVersionCount": 512,
  "lastWriteAt": "2026-10-23T12:00:00Z",
  "notice": "若 Beacon 侧配置功能仍开启，请确认不会两边同时修改同一份配置。JM 无法自动检测该状态。"
}
```

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-533 Beacon 配置纳管（ref/feat，需 spec，spec

**需求补充**：`docs/specs/config-beacon-migration/spec.md`，依赖 FR-529~532）：**2026-10-24 修订**——原设计「Beacon 导出→JM 导入」的数据搬运作废：配置文件本来就在服务器磁盘上，搬迁副本毫无意义。改为**纳管现有文件**：复用已有 `Discover`（FR-071）扫描实例工作目录，新增 `POST /config-files/adopt` 把磁盘实际内容读入为 `config_file` + 实例级 v1 版本，**内容与磁盘逐字节一致、不改写任何文件**。批量纳管走分组目标，同名文件在不同实例各自独立存 v1（不假设一致）。**分层需人工重建**（磁盘只有合并后的值，无法反推哪层贡献了什么）。Beacon 的分层版本链/变更历史不导入（保留在其侧供查询），FR-282 导出接口降级为审计备份用途。验收：纳管前后磁盘 mtime 不变；重复纳管返回 `ALREADY_ADOPTED` 不覆盖；100 实例批量纳管部分离线不整体失败；纳管后可在其上建分层覆盖并正确合并。

**交付状态**：✅ 已交付@v0.24.0

### FR-533

Beacon 配置禁用与迁移

✅ 代码完成（2026-10-25）：`service/config_adopt.go`（`ConfigAdoptService`：显式 files 与 target+pathPatterns 两种形态共用 `Adopt`；读/写两阶段解耦——并发读磁盘走 `ConfigAdoptSource` 只读接口（仅 Discover/Read，编译期拿不到写盘能力）、落库段 `writeMu` 串行避免同名 config_file 与 version_no 唯一索引竞争；`dryRun` 只走判定不写库）；`router/config_adopt.go`（`POST /config-files/adopt`、`POST /config-files/adopt/preview` 强制 dryRun、`GET /config-files/health`）；路由注册置于 `/config-files/:id` 通配之前。逐字节保真靠「直接用 Worker 原文、不经 ParseConfigContent/MergeConfigLayers/patchConfig 任何重新序列化路径」，`content_hash` 因此天然等于磁盘 sha256。纳管 v1 走 `appendLayer` 而非 `SaveLayer`：坏掉的配置也要能被记录（SaveLayer 会因语法校验拒绝入库）。`GET /config-files/health` 只报 JM 侧事实（configFileCount/layerVersionCount/lastWriteAt）+ 固定提示文案，**不做基于 Beacon 状态的自动判定**——已核实 `beacon_push.go` 只推实例注册（`/beacon/v1/agent/register`），按「同步开着」告警会把正常拓扑同步误报成双写风险。

