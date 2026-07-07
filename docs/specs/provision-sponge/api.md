# API Spec — FR-046 Sponge 子服支持

> 关联 FR: FR-046 | 优先级: P2 | 状态: 开发中

## 概述

FR-046 在既有向导式建服 API 上支持 SpongeVanilla 与 SpongeForge 后端子服。对外实例类型保持 `minecraft_java`，通过 `coreType` 区分服务端核心变体。

## REST API

### GET /api/v1/cores

- **描述**：查询可下载核心类型与版本，或解析指定版本/构建的下载信息。
- **Query**：
  - `type=paper|velocity|waterfall|bungeecord|spongevanilla|spongeforge`
  - `mcVersion`：可选；未传时返回版本列表，传入时解析构建。
  - `build`：可选；`0` 或缺省表示最新构建。

#### 版本列表响应

```json
{
  "type": "spongevanilla",
  "versions": ["1.21.1", "1.20.6"]
}
```

#### SpongeVanilla 构建解析响应

```json
{
  "type": "spongevanilla",
  "mcVersion": "1.21.1",
  "build": 12,
  "filename": "spongevanilla-1.21.1-12-universal.jar",
  "downloadUrl": "https://repo.spongepowered.org/repository/maven-releases/org/spongepowered/spongevanilla/.../spongevanilla-...-universal.jar",
  "sha256": ""
}
```

#### SpongeForge 构建解析响应

```json
{
  "type": "spongeforge",
  "mcVersion": "1.20.1",
  "build": 7,
  "filename": "spongeforge-1.20.1-...-universal.jar",
  "downloadUrl": "https://repo.spongepowered.org/repository/maven-releases/org/spongepowered/spongeforge/.../spongeforge-...-universal.jar",
  "sha256": "",
  "runtime": {
    "distribution": "spongeforge",
    "modFilename": "SpongeForge.jar",
    "forgeInstallerUrl": "https://maven.minecraftforge.net/net/minecraftforge/forge/.../forge-...-installer.jar",
    "forgeVersion": "...",
    "launchJar": "forge-server.jar"
  }
}
```

> `runtime` 为向后兼容新增字段；旧客户端忽略该字段仍可展示基础下载信息，新客户端用于展示 SpongeForge 需要 Forge 初始化。

### POST /api/v1/instances/provision/bukkit

- **描述**：向导创建后端子服。历史路径名保留为 `bukkit` 以兼容现有前端与调用方，但 `coreType` 可选择 Sponge 变体。
- **请求**：

```json
{
  "nodeId": 1,
  "name": "sponge-lobby",
  "coreType": "spongevanilla",
  "mcVersion": "1.21.1",
  "build": 0,
  "jdkId": 1,
  "memoryMb": 4096,
  "jvmArgs": ["-XX:+UseG1GC"],
  "groupId": 2,
  "onlineMode": false
}
```

SpongeForge 示例：

```json
{
  "nodeId": 1,
  "name": "sponge-forge-survival",
  "coreType": "spongeforge",
  "mcVersion": "1.20.1",
  "build": 0,
  "jdkId": 1,
  "memoryMb": 4096,
  "jvmArgs": ["-XX:+UseG1GC"],
  "onlineMode": false
}
```

- **成功响应**：`201 Created`，返回创建后的 Instance。
- **关键断言**：
  - `type` 必须为 `minecraft_java`。
  - `role` 必须为 `backend`。
  - SpongeVanilla 的启动核心为工作目录下 `server.jar`。
  - SpongeForge 的 Sponge jar 位于 `mods/SpongeForge.jar`，Forge 服务端入口由结构化启动配置引用。

#### 部分失败响应

实例已创建但下载、Forge 初始化或配置写入失败时：

```json
{
  "error": "PROVISION_FAILED",
  "message": "子服搭建失败: ...",
  "instance": { "id": 123, "type": "minecraft_java", "role": "backend" }
}
```

客户端应提示“实例已创建但搭建未完成”，并引导用户重试或删除。

## Worker gRPC / 内部部署能力

| 能力 | 类型 | 说明 |
|---|---|---|
| `DownloadCore` | 既有 Unary | 下载 SpongeVanilla jar 或其他单文件核心到实例工作目录 |
| SpongeForge 安装能力 | 新增或内部封装 | 在实例工作目录运行 Forge installer，写入 `mods/SpongeForge.jar`，并返回启动入口 |
| `WriteConfig` | 既有 Unary | 写入 `eula.txt`、`server.properties` 等基础配置 |
| `DeployServerProbe` | 既有 Unary | 可用时下发 ServerProbe 到 `plugins/` |

## 自动配置

- `eula.txt`：`eula=true`
- `server.properties`：写入分配的 `server-port`、`query.port`、`enable-query=true`、`online-mode=<用户选择>`
- SpongeVanilla：下载核心到 `server.jar`
- SpongeForge：安装 Forge 服务端并写入 `mods/SpongeForge.jar`
- ServerProbe：沿用现有 `plugins/ServerProbe.jar` 与 `plugins/ServerProbe/config.yml` 部署策略

## 错误码

| HTTP 状态 | error | 场景 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 请求体格式或必填字段错误 |
| 502 | `CORE_REPO_ERROR` | Sponge/Forge/Paper 源不可达、元数据结构异常、版本/构建不存在 |
| 502 | `PROVISION_FAILED` | 实例已创建但下载、安装或配置写入失败 |
| 422 | `PROVISION_FAILED` | 创建实例前校验失败或无法分配资源 |

## 验收接口契约

- `/cores?type=spongevanilla` 与 `/cores?type=spongeforge` 必须返回新→旧版本列表。
- `/cores` 构建解析必须能返回可展示的 `filename`、`downloadUrl`、`build`。
- `POST /instances/provision/bukkit` 对 `coreType=spongevanilla|spongeforge` 创建出的实例仍保持 `minecraft_java/backend`。
- 前端提交 payload 不得出现 `type=sponge`。
