# 功能规格：updater-core 遥测字段补齐与隐私契约

> 状态：开发中　·　关联 PRD：FR-360　·　分支：feature/fr-360-telemetry-privacy  
> 增强：FR-094 / FR-264　·　相关：ADR-023、contract §4.3；若修订隐私边界可写 `docs/adr/XXXX-client-telemetry-privacy-fields.md`（占位号，落地时由主控分配）

## 1. 背景与目标

现网 `Telemetry.java` 仅上报：`channel/result/fromVersion/toVersion/os/javaVersion/launcher/durationMs/bootSuccess`（error 可选）。运营排障缺少 **core 版本、CPU arch、Java vendor、locale/timezone、内存档位** 等诊断维度；面板与导出（FR-361）也缺少统一的 **required vs diagnostic** 与脱敏规则。

本 FR：在 updater-core → CP 落库 → 管理端可读链路补齐字段，并固化隐私契约（notice / opt-out / 面板脱敏）。**允许 schema 扩展**。

## 2. 需求（要什么）

### 2.1 新增/明确字段

| 字段 | 级别 | 来源（客户端） | 落库 | 说明 |
|---|---|---|---|---|
| `coreVersion` | **required** | 嵌入 core 版本字符串（构建/Manifest 已有则复用） | `client_telemetry` 新列 | 运营必需：区分内嵌 core 代际 |
| `arch` | **required** | `os.arch` 归一（如 amd64/arm64） | 新列 | 运营必需 |
| `os` | required（已有） | 归一为 windows/macos/linux/unknown 更佳 | 已有，可加强归一 | |
| `javaVersion` | diagnostic（已有） | `java.version` | 已有 | |
| `javaVendor` | **diagnostic** | `java.vendor` 截断 | 新列 | 可关/可脱敏 |
| `launcher` | diagnostic（已有） | 启发式 | 已有 | |
| `locale` | **diagnostic** | 默认 Locale 语言标签（如 `zh-CN`） | 新列 | 不含精确 GPS |
| `timezone` | **diagnostic** | `TimeZone.getDefault().getID()` | 新列 | 如 `Asia/Shanghai` |
| `memoryTier` | **diagnostic** | 按 `maxMemory` 分档枚举：`le2g` / `le4g` / `le8g` / `gt8g` / `unknown` | 新列 | **不上报精确堆字节** |
| `playerName` | diagnostic（已有，不可信） | hello/反查 | 已有 | 展示截断 |
| `machineId` / `installId` | 标识（header） | 现有 | 明细关联 | 面板截断 |

**兼容**：旧客户端缺新字段 → 服务端空串/默认，**不得 4xx**；202 best-effort 不变。

### 2.2 隐私契约

1. **opt-out**：`jm-updater.json` `telemetry:false` 仍整包不上报（默认 true）。不在本 FR 做字段级开关（避免复杂度）；diagnostic 字段随总开关。
2. **notice**：更新玩家/运营文档与面板说明：收集环境粗粒度与更新结果；机器码不可逆；可关；playerName 不可信。
3. **面板脱敏**（管理端展示统一工具函数，供 FR-358/361 复用）：
   - `playerName`：超长截断（现有 32 入库；展示可中间 `*` 或尾截断，选一种写死）
   - `machineId` / `installId`：展示前 6 + `…` + 后 4（过短则全掩）
   - IP：按现有安全页惯例；若无则保留完整仅管理员可见并审计（不扩大暴露面）
   - diagnostic 字段默认展示；导出列规则与 FR-361 对齐预留
4. **禁止**：日志打印拉取密钥明文；遥测 body 不带 key。

### 2.3 展示接入（最小）

- 管理端至少一处可读新字段：优先 **分发监控日志详情** 或 **安全中心画像/事件详情** 结构化区（有则加厚，无则加「环境」小节）。
- 不在本 FR 做完整 KPI 改造（归 356/357）。

### 2.4 不做

- CSV 导出端点 → FR-361  
- KPI 公式统一 → FR-356  
- 安全一键封禁 → FR-358  
- 字段级 telemetry 开关 UI  
- 精确 RAM/磁盘序列号/MAC/精确地理位置  

## 3. 设计（怎么做）

### 3.1 客户端（updater-core）

- 扩展 `Telemetry.build(...)`：写入上表 required + diagnostic。
- `memoryTier` 仅分档；`arch`/`os` 做简单归一函数（单测锁定）。
- 保持 `Core.run` opt-out 路径；`Transport.postTelemetry` JSON 契约扩展。
- 改 core 后必须走 **FR-351 内嵌门禁**：重建/归档 core 并使 `scripts/verify-client-updater-embed` 通过（agent 在 worktree 内按项目既有脚本执行，不跳过）。

### 3.2 服务端（Control Plane）

- `model.ClientTelemetry` 增列：`CoreVersion`、`Arch`、`JavaVendor`、`Locale`、`Timezone`、`MemoryTier`（varchar 长度保守，如 32~64）。
- `ClientTelemetryInput` + `Record` 截断写入；AutoMigrate。
- `POST /client-telemetry` body 解析新字段（未知字段忽略）；响应仍 202。
- 管理端读路径：事件/遥测详情 DTO 带出新字段（若现有 list 过重，可仅 detail）。
- 日聚合表 **不** 按新维度打爆基数（保持按 result 日聚合）；新字段仅明细层。

### 3.3 前端

- 类型与 mock 同步。
- 脱敏工具：`lib/privacy-mask.ts`（或等价）+ 单测。
- 详情 UI 展示新字段；不可信 playerName 角标若安全页已有则复用文案键。

### 3.4 契约与文档

- 更新 `docs/specs/client-distribution/contract.md` §4.3 字段表。
- `docs/API.md` 遥测请求体；ARCHITECTURE 一句数据模型。
- 若隐私边界相对 ADR-023 有实质扩展说明，新增占位 ADR `XXXX-client-telemetry-privacy-fields.md`。

### 3.5 与 FR-356 边界

- **禁止**修改 KPI 共享公式模块与两页成功率标签口径。
- 可增加展示字段，不改 `successRate` 定义。

## 4. 任务拆分

- [ ] Java：`Telemetry` 字段 + 归一/分档 + 单测；opt-out 回归
- [ ] Go：model/migrate + service Record + router 解析 + 单测/路由测
- [ ] 管理端 DTO/详情展示 + 脱敏工具单测
- [ ] contract / API / ARCHITECTURE / CHANGELOG / PRD 状态
- [ ] 可选：占位 ADR 隐私字段说明
- [ ] re-embed / `verify-client-updater-embed` 通过
- [ ] 相关 `go test`、`gradlew :updater-core:test`、前端 vitest/tsc

## 5. 验收标准

- [ ] 新客户端上报含 `coreVersion`、`arch`；diagnostic 字段在开启 telemetry 时出现在 body 并落库。
- [ ] 旧 body 无新字段仍 202，行内新列为空默认。
- [ ] `telemetry:false` 不上报（既有行为保持）。
- [ ] 管理端详情可见新字段；`playerName`/`machineId` 经脱敏函数展示（单测锁定格式）。
- [ ] contract §4.3 与 API 文档已更新 required vs diagnostic。
- [ ] 自动化：Go 相关包测试、updater-core 测试、前端类型检查与相关单测 **红→绿**。
- [ ] embed 校验脚本通过（或文档说明本地生成步骤且 CI 同款命令绿）。
- [ ] 真机/半真：至少用 mock 或集成测证明新字段进入详情；完整 MC OTA 非硬闸（与 brainstorm 一致）。

## 6. 风险 / 待定

- 嵌入 core 发版链路：仅改 Java 未 re-embed 会导致线上仍跑旧 core——**完成判据含 embed 门禁**。
- `locale`/`timezone` 被部分地区视为敏感：已标 diagnostic + 总开关；若合规加严可后续字段级开关（本 FR 不做）。
- 并行 FR-356：禁止改 KPI 字典文件归属 356 的公式；若需同文件展示，360 只加类型可选字段。
