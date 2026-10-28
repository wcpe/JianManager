# FR-529 配置分层覆盖与有效值解析

> 状态：草拟 · 优先级：P0 · 关联：FR-530 落盘校验、FR-531 灰度、FR-532 生效编排

## 1. 背景

JM 现有配置是**单实例粒度**：`InstanceConfigVersion{InstanceID, FilePath, Content, ContentHash}`。同一份配置要覆盖 N 台实例，就得写 N 份，无继承语义。

Beacon 的配置中心有 `namespace → bc_cluster → region → zone → server` 五层覆盖链，迁入 JM 后要基于 JM 自己的分组树（`InstanceGroupNode` 自引用邻接表）重建。

## 2. 目标

- 同一条配置按分组层级承载多份版本，生效值 = 自顶向下合并
- 支持 `null` 删键
- 合并确定性输出 + 内容 hash
- 提供有效配置预览：给定实例，展示最终值及**每个键来自哪一层**

## 3. 非目标

- 不做配置语法转换（yaml/json/properties 各自解析，不互转）
- 不做 schema 之外的语义校验（schema 校验见 §6）
- 不管理插件自身的运行时配置（那是探针 reload 的事）

## 4. 数据模型

### 4.1 `config_file`（配置逻辑文件）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT PK | |
| `name` | VARCHAR(255) | 逻辑名，建议用相对路径形式 `plugins/Foo/config.yml`；**全局唯一** |
| `format` | VARCHAR(16) | `yaml` / `json` / `properties`，创建后不可改 |
| `description` | VARCHAR(512) | |
| `schema_json` | TEXT 可空 | JSON Schema（Draft 2020-12 子集） |
| `sensitive_paths` | TEXT 可空 | JSON 数组，敏感键路径，用于脱敏展示 |
| `deleted_at` | DATETIME 可空 | 软删（回收站） |
| `created_by` / `created_at` / `updated_at` | | |

### 4.2 `config_layer_version`（层版本，不可变追加链）

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT PK | |
| `config_file_id` | BIGINT | |
| `scope_level` | VARCHAR(16) | `group` / `instance` |
| `scope_ref_id` | BIGINT | 分组 ID 或实例 ID |
| `version_no` | INT | 同 (file, scope) 内递增 |
| `content` | TEXT | 该层原文（不做合并） |
| `content_hash` | CHAR(64) | sha256 小写 hex |
| `based_on_version_id` | BIGINT 可空 | 回退时指向来源版本 |
| `remark` | VARCHAR(255) | |
| `created_by` / `created_at` | | |

唯一索引：`(config_file_id, scope_level, scope_ref_id, version_no)`。

**关键**：每个 (文件 × 作用域) 是**独立的不可变追加链**，回退 = 追加新版本，不修改历史。

### 4.3 为什么只有两级而不是五级

Beacon 有 namespace/BC/region/zone/server 五层，是因为它把集群拓扑写死在自己模型里。

JM 的分组树是**任意深度的自引用树**（`InstanceGroupNode.ParentID`），所以：

- `scope_level=group` + `scope_ref_id=<分组ID>` 可以表达**任意层级**（顶级分组=集群、二层=大区、三层=小区……由用户自己组织）
- `scope_level=instance` + `scope_ref_id=<实例ID>` 表达实例级覆盖

**优势**：不需要在代码里硬编码"大区""小区"这些概念，用户可以自由组织层级深度。这是 JM 比 Beacon 更通用的地方。

## 5. 合并算法

### 5.1 作用域链解析

给定目标实例 `instanceId`：

```
1. 从实例向上遍历分组树，得到分组链（从根到叶）：
   [groupRoot, groupRegion, groupZone]
2. 构造作用域链（优先级从低到高）：
   [group:groupRoot, group:groupRegion, group:groupZone, instance:instanceId]
3. 按链顺序合并各层 content
```

### 5.2 合并规则（按 format）

| format | 合并方式 | null 语义 |
|---|---|---|
| `yaml` / `json` | 深合并（对象递归、数组整体替换） | 值显式为 `null` → 删除该键 |
| `properties` | 键级覆盖（后者覆盖前者） | 不支持删键（`null` 是普通字符串） |

### 5.3 确定性输出

合并结果必须**可复现**：

- 对象键按字典序排序
- 数组保持原顺序（整体替换，不做元素合并）
- 数值保持原格式（不重新格式化）
- 输出后计算 sha256 作为 `effectiveHash`

**为什么要求确定性**：同一组合并两次必须得到同一 hash，否则灰度判断、变更检测都会误报。

### 5.4 边界情况

| 情况 | 行为 |
|---|---|
| 某层 content 为空 | 视为空对象，不影响合并 |
| 语法解析失败 | 该层标记失效，预览时报错并指出层与行号 |
| 中间层类型冲突（对象 vs 标量） | 高层覆盖低层，预览时告警 |
| 实例不属于任何分组 | 只应用 instance 层 |

## 6. Schema 校验

### 6.1 校验时机

| 时机 | 校验对象 |
|---|---|
| 保存某层版本时 | **该层原文**（不做合并校验） |
| 预览/下发时 | **合并后的有效值** |

### 6.2 Schema 来源

`schema_json` 是 `config_file` 的属性，不支持版本链。修改 schema 入审计。

支持的 JSON Schema 子集：`type` / `properties` / `required` / `enum` / `minimum` / `maximum` / `pattern` / `items` / `additionalProperties`。

`properties` 格式按扁平键名匹配。

## 7. 接口

### 7.1 配置文件

```http
GET    /api/v1/config-files                 列表（分页）
POST   /api/v1/config-files                 创建
GET    /api/v1/config-files/:id             详情
PUT    /api/v1/config-files/:id             改元数据（name/format 不可改）
DELETE /api/v1/config-files/:id             软删
```

### 7.2 层版本

```http
GET  /api/v1/config-files/:id/layers                    列出各作用域的当前 head
GET  /api/v1/config-files/:id/layers/:scopeType/:scopeId/history   版本历史
POST /api/v1/config-files/:id/layers                    保存新版本
POST /api/v1/config-files/:id/layers/:scopeType/:scopeId/rollback   回退到指定版本
DELETE /api/v1/config-files/:id/layers/:scopeType/:scopeId          删除该作用域全部版本
```

保存新版本请求：

```json
{
  "scopeType": "group",
  "scopeId": 12,
  "content": "port: 25566\nmotd: xxx",
  "remark": "调整端口"
}
```

### 7.3 有效值预览

```http
GET /api/v1/config-files/:id/effective?instanceId=5
```

响应：

```json
{
  "instanceId": 5,
  "content": "合并后的完整文本",
  "effectiveHash": "sha256...",
  "traced": {
    "port":  { "value": 25566, "source": { "level": "group", "id": 12, "name": "region1", "versionNo": 3 } },
    "motd":  { "value": "xxx",   "source": { "level": "instance", "id": 5, "versionNo": 1 } },
    "debug": { "value": null,    "source": { "level": "group", "id": 12, "name": "region1", "versionNo": 3 }, "deleted": true }
  },
  "warnings": []
}
```

`traced` 是**关键能力**——运维能直接看到"这个值从哪来的"，这是 Beacon 也有的能力，必须保留。

### 7.4 预览结果是「规范化」的，必须明示（真机验收发现）

`content` 是**合并后再序列化**的产物，与磁盘原文有本质差异：

| 维度 | 磁盘原文 | `effective.content` |
|---|---|---|
| 缩进 | 原文缩进（可能是 4 空格） | 统一为解析器的输出格式（通常 2 空格） |
| 键顺序 | 原文顺序 | **对象键按字典序重排** |
| 注释 | 保留 | **全部丢失** |
| 块标量 / 引号风格 | 保留 | 按序列化器规则重写 |

真机验证：纳入一份含 4 空格缩进、块标量、行尾注释的 YAML，
`contentHash` 与磁盘 sha256 一致（纳管正确），但 `effective.content` 已重排且注释消失。

**这是合并语义的必然结果，不是缺陷**——合并必须先解析再序列化。

**但必须在 UI 上明示**，否则用户会把预览内容复制回磁盘，导致注释与格式丢失。

UI 文案要求（前端实现时照抄）：

> 预览为**合并后的规范化结果**，用于确认取值与来源。
> 它**不保留原始注释、缩进与键顺序**，请勿直接复制回磁盘。
> 需要原文请查看「层版本历史」。

### 7.5 附带：实例层优先级高于分组层（真机验收发现）

作用域链 `[group:root, ..., group:parent, instance:self]` 中，**实例层在最右、优先级最高**。

后果：纳管后在分组层设 `port: 25999`，`effective` 仍返回实例层的 `25566`；`traced` 全部指向 `instance:*`。

**这也是既有设计（与 Beacon 一致），但与运维直觉相反**（通常以为"分层是叠加的，上层能覆盖下层"）。

UI 文案要求：

> **实例层优先级最高，分组层覆盖不了它。**
> 要让分组值生效，需先删除该实例的层版本，或直接改实例层。

## 8. 敏感值处理

- `sensitive_paths` 声明的键在预览与列表接口中脱敏为 `***`
- 内容 hash **基于明文**计算（保证与下发一致）
- 日志与审计**不记录任何 content**
- 审计只记"哪些键变了"，敏感键只记"值已变更"

## 9. 与现有代码的关系

| 现有 | 处置 |
|---|---|
| `InstanceConfigVersion` | **保留**，作为 `scope_level=instance` 的特例，或迁移后废弃（见 §12） |
| `ConfigBaseline` / `DetectDrift` | 保留，`Converge` 改为调用新的合并+下发链路 |
| `config_patch.go` 按字段补丁 | 保留，作用于某一层的 content |

## 10. 迁移路径

存量 `InstanceConfigVersion` 数据迁移：

1. 为每个 `(FilePath)` 创建一条 `config_file`（name = filePath）
2. 每个实例的历史版本转为 `scope_level=instance` 链
3. 迁移后保留原表只读一个版本周期，确认无误后删除

## 11. 验收标准

### 合并正确性

1. 只在 region1 覆盖某键、region2 不覆盖 → 两实例各取正确值
2. `null` 删键在 yaml/json 生效、properties 不生效
3. 数组整体替换而非元素合并
4. 同一组合并两次 hash 相同

### 追溯

5. 预览能指出每个键的来源层级与版本号
6. 被 `null` 删除的键在 `traced` 中标记 `deleted: true`

### 边界

7. 实例不属于任何分组时只应用 instance 层
8. 语法错误能被定位到层与行号
9. 中间层类型冲突有告警而非静默

### 规模

10. 1000 个实例、50 个配置文件时，预览单实例 < 50ms

## 12. 待定

1. **是否保留 `InstanceConfigVersion`**：作为 layer 的特例（改动小）还是迁移后废弃（更干净）
2. 分组树深度是否限制（当前无限制，但深树会让合并链变长）
3. 是否需要「跨文件引用」（如 `server.properties` 引用 `plugin.yml` 的值）——当前不做

## 附：PRD 表内原始记录（2026-10-26 迁入）

以下内容原先写在 PRD 的 FR「需求」列里。按「**PRD 只保留一行 FR + 状态，详情归 spec**」的约定迁入本文件，PRD 表内只留简短介绍。

### FR-529 配置分层覆盖与有效值解析（feat，需 spec，spec

**需求补充**：`docs/specs/config-layered-override/spec.md`，依赖现有配置版本链）：把当前**单实例粒度**的配置升级为基于分组树的**继承覆盖链**（集群 → 大区 → 小区 → 实例），同一条配置按层级承载多份版本，生效值 = 自顶向下合并；支持 `null` 删键语义、确定性输出 + 内容 hash（保证幂等）；提供**有效配置预览**（给定目标实例，展示最终生效值及每个键来自哪一层）。**现状缺口**：现有 `InstanceConfigVersion` 只有 `instanceId + filePath` 两个维度，无层级概念；`ConfigBaseline.Converge` 是循环逐台，无继承语义。验收：region1 覆盖某键、region2 不覆盖，两实例各取正确值；预览能指出每个键的来源层级；合并结果 hash 稳定可复现。

**交付状态**：✅ 已交付@v0.24.0

### FR-529

配置分层覆盖

✅ 代码完成+真机验收（2026-10-24）：`model/config_file.go` + `service/{config_layer,config_merge,config_schema}.go` + `router/config_file.go`。两级（group/instance）经分组树表达任意深度；yaml/json/properties 三种合并语义；`traced` 精确指出每个键的来源层与版本号。**真机**：纳管后 `effective` 能正确合并并追溯来源；同时证实两条必须明示的语义（预览是规范化结果、实例层优先级最高），已写入 spec §7.4/7.5 与 API.md。**前端**：`ConfigFilesPage` + `ConfigFileDetailPage`（含两条约束的常驻提示）。

