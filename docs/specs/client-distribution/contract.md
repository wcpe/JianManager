# 客户端分发 OTA — 接口契约（manifest + agentArgs）

> **FR-087 核心产出**。服务端线（FR-086/087/088/093/094/095/096/097）与客户端线（FR-089/090/091/092/094/097）的**接口契约**，两线据此并行实现；任何变更须双线同步。
> 关联 ADR-021（纯 JVM 方案）、ADR-022（签名信任 + 防降级 + 密钥轮换）、ADR-023（端点防护）。
> 状态：契约 v1（2026-06-23 定稿，开发中可微调字段、须回写本文件）。

## 1. 总览

```
玩家启动器(HMCL/PCL2) ──JVM参数── -javaagent:wedge.jar=<gameDir>
   │ premain
楔子(wedge) ──读 jm-updater.json(channel/key/endpoint) + 自定位──┐
   │ 动态 classload                                              │
updater-core ──GET /manifest(latest,带 key+machineId)──→ JM 分发后端
   │ 验签 + 防降级 + 文件级 reconcile                             │
   │ GET /client-artifacts/{sha256}(zstd 制品, Range) ──────────┘
   └ POST /client-telemetry(结果回传)
```

- **manifest 只提供 latest**（单一当前版本），不暴露版本历史。
- **信任根 = manifest 的 Ed25519 签名**；拉取密钥仅鉴权路由（半公开、不防篡改）。
- **传输最小化**：文件级增量（只取 hash 变化的文件）+ 每文件 zstd 压缩制品。

## 2. manifest 格式

`GET /client-channels/{channelId}/manifest` 返回的 JSON：

```jsonc
{
  "schemaVersion": 1,                 // 契约结构版本（升级 break 时 +1）
  "channel": "skyblock-s1",           // 频道 id
  "version": 42,                      // 单调递增整数；防降级基准（见 §3）
  "issuedAt": "2026-06-23T10:00:00Z", // 信息性
  "managedDirs": ["mods", "resourcepacks", "config"], // 托管区：仅这些目录可增删（减量）
  "files": [
    {
      "path": "mods/foo.jar",         // 相对 gameDir 的 POSIX 路径
      "sha256": "ab12…",              // 解压后原始内容 hash —— 信任校验（强）
      "md5": "cd34…",                 // 解压后原始内容 md5 —— 本地快筛（弱，不可作信任）
      "size": 123456,                 // 解压后原始大小（字节）
      "sync": "strict",               // strict=强制一致 | once=仅缺失时写 | ignore=不动
      "platform": null,               // null=全平台 | "windows" | "macos" | "linux"
      "artifact": {                   // 下载制品（内容寻址，见 §4 制品端点）
        "sha256": "ef56…",            // 制品自身 hash = 下载寻址 key
        "size": 45678,                // 制品（压缩后）大小
        "codec": "zstd"               // "zstd" | "none"
      }
    }
    // …
  ],
  "agent": {
    "wedge": { "version": 3 },        // 楔子版本（信息性；楔子随基础包、不自更新）
    "core": {                         // updater-core 自更新段（FR-091）
      "version": 5,
      "platforms": {
        "windows": { "artifact": { "sha256": "…", "size": 0, "codec": "zstd" } },
        "macos":   { "artifact": { "sha256": "…", "size": 0, "codec": "zstd" } },
        "linux":   { "artifact": { "sha256": "…", "size": 0, "codec": "zstd" } }
      }
    }
  },
  "sig": {                            // 见 §3
    "alg": "Ed25519",
    "keyId": "k1",                    // 公钥版本（轮换用）
    "value": "base64(signature)"
  }
}
```

字段规则：
- **path**：统一 POSIX `/`、相对 gameDir、不得逃逸（`..` 拒绝）；不得落入玩家区（见 §6.4）。
- **platform**：updater 只取 `platform==本机` 或 `platform==null` 的文件；JRE/natives 等用平台专属项。
- **sync**：`strict` 文件被 reconcile 强制与 manifest 一致；`once` 仅当本地缺失才写（玩家可改的整合包配置）；`ignore` 列出但不增不删（仅供展示/审计）。
- **减量**：仅在 `managedDirs` 内、对 `sync!=once&&!=ignore` 的文件，删除"本地有但 manifest 未列"的。

## 3. 签名与防降级

- **签名范围**：对**去掉 `sig` 字段后**的 manifest 做 **canonical JSON**（键按 UTF-8 码点升序递归排序、无多余空白、数字最简形式）序列化，再 Ed25519 签名。`sig.value` = base64(签名)。
- **覆盖 version**：`version` 在签名范围内 → 攻击者无法改版本号而保持签名有效。
- **验签**：updater 用 `sig.keyId` 选内置公钥验签；缺失/不符 → **拒绝整份 manifest**，fail-static 带本地版本进游戏。
- **密钥轮换（ADR-022）**：updater 内置 `keyId → 公钥` 映射（主 `k1` + 备 `k2`…）；私钥泄露切备用 keyId、经一次基础包更新淘汰旧公钥。私钥服务端持有、env 注入不入库。
- **防降级/重放（ADR-022）**：updater 本地持久化 `lastSeenVersion`；拉到的 manifest 若 `version < lastSeenVersion` → **拒绝**（疑似重放旧版投毒）。运营回滚不是下发更低号，而是**以更高 version 重发旧内容**（FR-088）。

## 4. 端点

> 均为**面向玩家公网**端点，拉取密钥鉴权，与运营者浏览器 JWT 入口隔离（ADR-022/023）。L7 防护见 FR-096。

### 4.1 `GET /client-channels/{channelId}/manifest`
- Headers：`X-Client-Key`（必，拉取密钥）、`X-Machine-Id`（必，机器码，见 §5）、`X-Client-Core-Version`（可，当前 core 版本，便于统计）
- 200：§2 的签名 manifest（`Cache-Control` + `ETag=version:keyId`，CDN 友好）
- 304：Not Modified（If-None-Match 命中）
- 401/403：无效/吊销 key；429：限流（FR-096）；404：channel 不存在

### 4.2 `GET /client-artifacts/{sha256}`
- Headers：`X-Client-Key`（必）、`X-Machine-Id`（可）；支持 `Range`（断点续传）
- 200/206：二进制制品（按 codec 为 zstd 压缩流或原文）；强缓存（内容寻址不可变）
- 403/404/416/429
- 制品 = FR-045 制品库 `type=client-file`，`sha256` 即 `artifact.sha256`

### 4.3 `POST /client-telemetry`（FR-094）
- Headers：`X-Client-Key`、`X-Machine-Id`
- Body：`{ result: "success"|"fail-static"|"rolled-back"|"error", fromVersion, toVersion, os, javaVersion, launcher, durationMs, bootSuccess: bool, error?: string }`
- 202 Accepted（不阻塞客户端；隐私可关，见 FR-094）

## 5. 鉴权与身份

- **`X-Client-Key`**：频道拉取密钥。服务端只存 SHA-256 哈希、比对校验（FR-086）；吊销即 401。**半公开**（随整包分发会泄露）→ 仅鉴权路由 + 吊销，**不作内容可信依据**（内容可信靠 §3 签名）。
- **`X-Machine-Id`**：客户端生成的机器码（FR-092，已实装）。updater 多硬件/环境特征组合 SHA-256（不可逆）+ userHome 持久化（稳定容错），随 manifest/制品请求携带；服务端 manifest 拉取时 best-effort 登记入 `client_machines`。**不可信**（客户端可伪造）→ 仅用于审计/统计/**辅助**限流；**限流主键为 IP**（FR-096）。

## 6. agentArgs 协议（楔子 ↔ updater-core）

### 6.1 楔子注入
- 启动器 JVM 参数：`-javaagent:<path>/wedge.jar=<gameDir>`
- `premain(String agentArgs, Instrumentation inst)`：`agentArgs` = gameDir（优先）；为空则解析 `System.getProperty("sun.java.command")` 的 `--gameDir` 兜底；再不行用楔子自定位目录推断。
- 楔子 fail-open（ADR-021）：premain 全程 try/catch，任何异常都 `return`（放行游戏）。

### 6.2 楔子自定位与配置
- 自定位：`Wedge.class.getProtectionDomain().getCodeSource().getLocation()` → 得 wedge.jar 绝对路径 → 同目录。
- 配置文件：楔子同目录 `jm-updater.json` = `{ "channel": "...", "key": "...", "endpoint": "https://...", "coreJar": "updater-core.jar", "timeoutSec": 120 }`。

### 6.3 楔子 → core 入口约定
- core 以独立 `URLClassLoader` **内存加载**（读 jar 字节 → defineClass，避免文件锁，便于 FR-091 自更新替换）。
- 入口（反射）：`int top.jm.updater.Core.run(java.util.Map<String,String> ctx)`
  - `ctx` = `{ gameDir, channel, key, endpoint, wedgeDir, coreVersion }`
  - `coreVersion`（FR-091）：楔子据自更新选择状态机选定的 core 版本（内置 bundled 默认 0），core 据此与 manifest `agent.core.version` 比对决定是否自更新；machineId 见 §5（FR-092）。
  - 返回值：`0` = 更新成功，放行；`非 0` = fail-static（带本地版本放行 + 提示）；core **不得抛异常逃逸到楔子**（自己兜底）。
- 超时：楔子等待 `timeoutSec`（默认 120s），超时中断并 fail-static 放行。
- **core 自更新（FR-091）**：core 消费 manifest `agent.core`（§2）暂存更高版本 core 为 pending（下载+sha256+selftest）；楔子下次 premain 经 `<gameDir>/.jm-updater/core/state.properties` + `pending.confirmed`/`rollback.flag` 标志做 promote / 首次 trial / 启动失败回退 N-1。`wedge.version` 仅信息性（楔子不自更新）。

### 6.4 托管区 / 玩家区（reconcile 边界，FR-090）
- **托管区** = manifest `managedDirs`：updater 可增删，与 manifest 严格一致。
- **玩家区**（永不碰）：`saves/`、`screenshots/`、`logs/`、`options.txt`、`crash-reports/`、及任何不在 `managedDirs` 下的路径。
- `config/` 走 `sync` 策略区分整合包配置（`strict`）与玩家偏好（`once`）。

## 7. 关联 FR

| 契约要素 | 服务端实现 | 客户端实现 |
|---|---|---|
| manifest 生成 + 签名 + latest 指针 | FR-087 / FR-088 | — |
| 制品分发（zstd, Range, CAS） | FR-087（复用 FR-045） | FR-090 下载 + 解压 |
| 拉取密钥鉴权 | FR-086 | 携带（§5） |
| 机器码 | FR-092（登记/统计） | FR-092（生成/携带） |
| 防降级/重放 | FR-088（版本单调） | FR-090（lastSeenVersion） |
| 自更新段 | FR-087（manifest.agent.core） | FR-091 |
| 遥测 | FR-094（接收） | FR-094（上报） |
| agentArgs / 楔子↔core | — | FR-089 / FR-090 |
| `.jmpack` 容器（zstd+签名） | FR-097（打包） | FR-097（解包） |
| L7 防护 | FR-096 | — |

> **版本历史 / 运营回滚为「管理面」，不在本契约（客户端面）内**：FR-088 的 `GET .../versions`、`GET .../versions/:version`、`POST .../rollback` 走 **JWT 平台管理员**，玩家 updater 永不访问；客户端仅认 §2 的 latest manifest。回滚=以更高 version 重发旧内容（§3），对客户端等价于一次正常的版本前进。
