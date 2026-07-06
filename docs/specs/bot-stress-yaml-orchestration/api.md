# API 契约：Bot 压测会话 YAML 动作编排

> 状态：草拟　·　关联 PRD：FR-042 增强

## POST /api/v1/bots/stress-sessions

- 描述：创建持久化 Bot 压测会话，支持 YAML 动作编排。
- 权限：`bot:manage`，按目标实例隔离。
- 兼容别名：`POST /api/v1/bots/stress-test`。
- 请求：

```json
{
  "instanceId": 1,
  "count": 50,
  "behavior": "idle",
  "namePrefix": "load",
  "config": { "server": "127.0.0.1", "port": 25565, "auth": "offline" },
  "orchestrationYaml": "loop: true\nstaggerMs: 500\nphases:\n  - durationSec: 60\n    behavior: idle\n"
}
```

字段规则：
- `instanceId` 必填。
- `count` 范围保持 `1..5000`，本期真实验收固定使用 50。
- `namePrefix` 必填。
- `config` 保持现有 Bot 连接配置 JSON。
- `behavior` 在 `orchestrationYaml` 为空时必填；在 `orchestrationYaml` 非空时可省略，响应中的 `behavior` 取首个阶段行为。
- `orchestrationYaml` 可选；非空时必须通过 YAML 编排校验。

- 响应 `201`：

```json
{
  "id": 1,
  "uuid": "uuid",
  "instanceId": 1,
  "count": 50,
  "behavior": "idle",
  "namePrefix": "load",
  "config": { "server": "127.0.0.1", "port": 25565, "auth": "offline" },
  "orchestrationYaml": "loop: true\nstaggerMs: 500\nphases:\n  - durationSec: 60\n    behavior: idle\n",
  "orchestrationSummary": {
    "enabled": true,
    "loop": true,
    "staggerMs": 500,
    "phaseCount": 1,
    "durationSec": 60,
    "behaviors": ["idle"]
  },
  "status": "pending",
  "startedAt": null,
  "stoppedAt": null,
  "createdAt": "datetime",
  "updatedAt": "datetime",
  "counts": { "total": 0, "byStatus": {} }
}
```

- 错误：
  - `400 INVALID_REQUEST`：参数缺失、数量越界、旧模式缺 `behavior`、YAML 语法错误或编排语义非法。
  - `403 FORBIDDEN`：无权管理目标实例。

## GET /api/v1/bots/stress-sessions

- 描述：分页列出压测会话，返回会话状态、关联 Bot 聚合计数和编排摘要。
- 权限：`bot:read`，按可访问实例集合收敛。
- Query：`?page=1&pageSize=20`
- 响应 `200`：

```json
{
  "items": [
    {
      "id": 1,
      "instanceId": 1,
      "count": 50,
      "behavior": "idle",
      "namePrefix": "load",
      "status": "running",
      "orchestrationSummary": {
        "enabled": true,
        "loop": true,
        "staggerMs": 500,
        "phaseCount": 4,
        "durationSec": 330,
        "behaviors": ["idle", "patrol", "guard", "custom"]
      },
      "counts": { "total": 50, "byStatus": { "connected": 50 } }
    }
  ],
  "total": 1,
  "page": 1,
  "pageSize": 20
}
```

## GET /api/v1/bots/stress-sessions/:id

- 描述：查询单个压测会话详情，返回持久化 YAML。
- 权限：`bot:read`，按会话目标实例隔离。
- 响应 `200`：同创建响应。
- 错误：
  - `403 FORBIDDEN`：无读取权限。
  - `404 NOT_FOUND`：会话不存在或无权访问。

## POST /api/v1/bots/stress-sessions/:id/start

- 描述：启动压测会话，按会话配置批量创建并上线 Bot；含 YAML 编排时下发 `orchestrated` 行为和 `behavior_config`。
- 权限：`bot:manage`，按会话目标实例隔离。
- 响应 `200`：会话视图，含 `counts` 和 `orchestrationSummary`。
- 错误：
  - `400 INVALID_REQUEST`：会话状态不允许启动或持久化编排无法解析。
  - `404 NOT_FOUND`：会话不存在或无权访问。

## POST /api/v1/bots/stress-sessions/:id/stop

- 描述：停止压测会话，将会话关联 Bot 批量置为 `stopped`。
- 权限：`bot:manage`，按会话目标实例隔离。
- 响应 `200`：会话视图，含 `counts` 和 `orchestrationSummary`。
- 错误：
  - `404 NOT_FOUND`：会话不存在或无权访问。
