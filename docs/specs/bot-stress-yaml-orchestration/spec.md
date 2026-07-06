# 功能规格：Bot 压测会话 YAML 动作编排

> 状态：已交付　·　关联 PRD：FR-042 增强　·　分支：当前工作树

## 1. 背景与目标

FR-042 已提供持久化 Bot 压测会话、批量上线/下线和聚合展示。本次增强补齐“压测会话不是只创建一批 Bot，而是能让一批 Bot 按可读脚本持续执行动作”的能力。

目标是：运营者在创建压测会话时，用 YAML 定义阶段化动作；系统持久化该 YAML；启动会话后 50 个 Bot 能持续在线，并按 YAML 编排循环播放动作；真实环境验收中，30 分钟内必须始终保持 50/50 在线，任意 Bot 掉线即验收失败。

## 2. 需求

- 创建压测会话时支持 `orchestrationYaml` 字段，字段内容是 YAML 字符串；HTTP 请求体仍保持现有 JSON 外壳。
- 后端持久化原始 YAML，并在创建时解析校验；非法 YAML 或非法动作在创建阶段返回 400。
- 保持现有无编排会话兼容：不传 `orchestrationYaml` 时，继续使用 `behavior` 作为初始行为。
- 新增单个压测会话详情 API，详情返回原始 `orchestrationYaml`、摘要、状态和 Bot 聚合计数。
- 前端创建压测会话入口改为 YAML 编排编辑体验：提供默认模板、可编辑文本、提交失败时展示后端校验错误。
- Bot Worker 执行编排时支持阶段、循环、错峰启动和自定义动作。
- 行为覆盖 `idle`、`follow`、`patrol`、`guard`、`custom`；`custom` 复用现有 `move/chat/wait/attack/interact/use_item` 步骤。
- 真实环境验收必须自启动服务端、Worker、bot-worker 和 Minecraft 测试服务器，创建 50 Bot 会话，运行 30 分钟并持续断言 50/50 在线。

范围内：
- 扩展 FR-042 的会话模型、API、Worker 下发链路、bot-worker 行为引擎和 Bot 管理页。
- 复用现有 gRPC `behavior_config` 字段，不新增 proto 字段。
- 复用 Go 侧已有 `gopkg.in/yaml.v3`；前端复用已有 CodeMirror YAML 语法能力，不新增前端 YAML 解析依赖，校验以服务端为准。

不做：
- 不把整个 REST API 请求体改成 YAML。
- 不新增长期后台稳定性监控表；30 分钟严格在线由真实环境验收脚本持续轮询证明。
- 不扩大单 Worker 容量上限，本期目标固定验证 50 Bot。
- 不改 FR-098 的块级二进制 diff 设计；FR-098 只在最终验收中继续回归确认。

## 3. 设计

### 3.1 YAML 契约

最小可用示例：

```yaml
loop: true
staggerMs: 500
phases:
  - durationSec: 60
    behavior: idle
  - durationSec: 120
    behavior: patrol
    target: "0,64,0;8,64,8"
  - durationSec: 60
    behavior: guard
  - durationSec: 90
    behavior: custom
    steps:
      - type: chat
        message: hello
      - type: wait
        durationMs: 3000
      - type: move
        pos:
          x: 0
          y: 64
          z: 0
```

规则：
- `phases` 必须非空。
- `durationSec` 必须大于 0。
- `behavior` 只能是 `idle/follow/patrol/guard/custom`。
- `staggerMs` 可选，默认 0；含义是第 N 个 Bot 的编排启动偏移为 `(N-1) * staggerMs`。
- `loop` 为 `true` 时，最后一个阶段结束后回到第一个阶段；为 `false` 时，最后停留在最后一个阶段。
- `custom.steps` 的步骤类型与现有 `CustomBehavior` 保持一致；`durationMs` 在下发到 bot-worker 时映射为现有 `duration`。

### 3.2 Control Plane

`BotStressSession` 新增字段：
- `OrchestrationYAML`：原始 YAML 文本。
- `OrchestrationSummary`：后端解析出的摘要 JSON 字符串，用于列表和详情展示。

创建流程：
1. Gin 继续按 JSON 绑定请求。
2. 若 `orchestrationYaml` 为空，走现有 `behavior` 校验与创建逻辑。
3. 若 `orchestrationYaml` 非空，用 YAML 解析器解析成内部结构并校验。
4. 后端保存原始 YAML 和摘要；响应仍是 JSON，其中增加 `orchestrationYaml` 与 `orchestrationSummary`。
5. 启动会话时，把解析后的编排结构序列化为 `behavior_config`，随每个 Bot 下发；Bot 的行为类型使用 `orchestrated`。

### 3.3 Worker 与 Bot Worker

Worker 不解析 YAML，只透传 CP 生成的 `behavior_config`。这样 YAML 解析和校验只在 Control Plane 一处发生，避免 Go/TypeScript 双实现漂移。

bot-worker 新增 `orchestrated` 行为：
- 启动后按 `startDelayMs` 等待。
- 按阶段切换内部行为。
- 阶段到期后进入下一阶段。
- `loop=true` 时循环；否则停留在最后阶段。
- 内部行为复用已有行为类，`custom` 阶段复用 `CustomBehavior`。

### 3.4 前端

Bot 压测会话创建对话框保留现有实例、数量、连接配置和名称前缀字段，新增 YAML 编排编辑区：
- 默认填入可运行模板。
- 提供“恢复模板”按钮。
- 列表展示编排摘要：阶段数、是否循环、总时长、行为集合。
- 详情入口展示原始 YAML 和聚合计数。

## 4. 任务拆分

- [x] 后端测试先行：YAML 解析、非法输入、兼容旧请求、详情 API、持久化字段、启动时 behavior_config 下发。
- [x] 后端实现：模型字段、解析器、摘要、详情路由、服务层启动下发。
- [x] Worker/bot-worker 测试先行：IPC behavior_config 透传、orchestrated 行为阶段推进、loop、stagger、custom 阶段。
- [x] Worker/bot-worker 实现：行为配置透传与 `orchestrated` 行为。
- [x] 前端测试先行：创建对话框 YAML 模板、提交字段、列表摘要、详情展示。
- [x] 前端实现：API 类型、Bot 页面 YAML 编辑、模板、错误展示和 i18n。
- [x] 文档同步：`docs/PRD.md`、`docs/ARCHITECTURE.md`、`docs/API.md`、`CHANGELOG.md`。
- [x] 多智能体验收：单测、集测、本机断言+截图、真实浏览器断言+截图、50 Bot 30 分钟真实环境验收。

验收摘要：
- 真实环境 30 分钟验收已通过：脚本自启动 Control Plane、Worker、bot-worker 和 Minecraft 测试服务器。
- 验收会话使用 `count=50` 和 YAML 编排；持续时间 `1800000ms`，共 360 次轮询。
- 每次轮询均满足 `total=50` 且 `connected=50`，未出现掉线或状态回退。
- 真实浏览器验收已覆盖会话列表、会话详情、YAML 原文展示和 `orchestrated` 行为状态。
- 长时验收过程中 access token 过期后完成刷新，刷新后继续保持 50/50 在线。

入库截图证据：
- [FR-041 本机单 Bot 详情](evidence/fr-041-bot-detail-local.png)
- [FR-041 真实环境单 Bot 详情](evidence/fr-041-real-bot-detail.png)
- [FR-042 真实环境压测会话](evidence/fr-042-real-stress-session.png)
- [FR-042 本机浏览器压测会话](evidence/fr-042-stress-session-browser.png)
- [FR-042 YAML 真实环境会话列表](evidence/fr-042-yaml-stress-session-real.png)
- [FR-042 YAML 真实环境会话详情](evidence/fr-042-yaml-stress-session-detail-real.png)
- [FR-098 真实环境增量补丁清单](evidence/fr-098-real-manifest-patch.png)

## 5. 验收标准

- 单元测试：
  - Go 侧 YAML 解析覆盖正常路径、空编排、非法 YAML、非法行为、非法时长。
  - Go 服务层覆盖新字段持久化、旧请求兼容、详情 API 响应、启动会话下发 `orchestrated` 配置。
  - bot-worker 覆盖阶段推进、循环、错峰、custom 阶段执行。
  - 前端覆盖 YAML 字段提交和模板展示。
- 集成测试：
  - `POST /api/v1/bots/stress-sessions` 能创建含 YAML 编排的会话。
  - `GET /api/v1/bots/stress-sessions/:id` 能返回持久化 YAML 和摘要。
  - `POST /api/v1/bots/stress-sessions/:id/start` 能创建关联 Bot，并把编排配置传到 Worker。
- 本机截图验收：
  - Mock 或本机服务中，Bot 页面可见 YAML 编排创建入口、会话摘要和详情 YAML。
- 真实浏览器验收：
  - 真实 Control Plane + Worker + bot-worker 环境中，用 Playwright 创建 YAML 编排会话、启动并截图确认页面状态。
- 真实环境 30 分钟验收：
  - 脚本自启动 Minecraft 测试服务器、Control Plane、Worker、bot-worker。
  - 创建 `count=50` 的 YAML 编排压测会话。
  - 30 分钟内持续轮询会话聚合计数，必须始终为 `connected=50/50`。
  - 同期至少确认一次 Bot 行为事件或状态中出现编排行为切换。
  - 任意轮询发现 connected 小于 50，验收失败并记录截图与日志。
- FR-041 和 FR-098 回归：
  - FR-041 单 Bot 详情 SSE 在真实环境仍可见状态/事件。
  - FR-098 zstd patch-from 发布与客户端回退链路继续通过既有真实环境断言。

## 6. 风险

- 50 Bot 稳定在线依赖测试机 CPU、内存、Node.js 和 Minecraft 测试服务器容量；验收失败必须先按日志区分产品缺陷与机器资源不足，但不允许把阈值降级。
- `patrol/guard` 的目标参数需要与现有行为引擎补齐映射，否则会出现“阶段切换了但动作不明显”的假通过风险。
- 编排计时在 bot-worker 进程内存中执行；Worker 或 bot-worker 重启后，本期不自动恢复阶段进度，只允许重新启动会话重跑验收。
