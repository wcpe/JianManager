# 测试和质量要求

## 测试层级

### 单元测试（必须）

| 模块 | 覆盖要求 |
|---|---|
| service 层 | 核心业务逻辑必须有单元测试 |
| 状态机 | 所有状态转换路径必须覆盖 |
| 二进制帧协议 | 编解码必须有测试 |
| IPC 协议 | 消息序列化/反序列化必须有测试 |
| 工具函数 | 必须有测试 |

### 集成测试（推荐）

| 场景 | 覆盖要求 |
|---|---|
| gRPC 调用 | 至少覆盖主要 RPC 的 happy path |
| 数据库操作 | GORM model 的 CRUD 操作 |
| 守护进程通信 | 二进制帧协议的端到端通信 |

### E2E 测试（V1 可选）

> **本地跑 Playwright 前必须清掉 `DISPLAY`**：SSH / X11 转发环境（`DISPLAY=localhost:N`）会让 Chromium 的 `requestAnimationFrame` 完全不触发，导致所有 `click()` 因「稳定性检查」永不满足而超时（报 `waiting for element to be visible, enabled and stable`），看起来却像登录失败或页面缺陷。命令用 `env -u DISPLAY <playwright 命令>`。CI runner 无 `DISPLAY` 故不受影响——**本地 E2E 红而 CI 绿时，优先按此自查**，不要误判为代码回归。详见 `docs/CONTRIBUTING.md` §1。

| 场景 | 覆盖要求 |
|---|---|
| 实例完整生命周期 | 创建→启动→运行→停止→删除 |

### 状态 / 迁移类变更（必须：迁移 → 首启 → 停 → 二启 → 三启）

> 凡改动**索引 / 账本 / 投影代次 / schema / 回滚脚本**，或做「把某份状态换个地方存」这类迁移，验收脚本必须覆盖**迁移 → 首启 → 停 → 二启 → 三启**，且二启/三启跑在**脏环境**上（上一轮遗留的状态与数据仍在）。**「首启通过」不代表通过。**

**为什么（事故实证）**：2026-10-01 FR-496（采集索引迁 SQLite）**首启正常、二启必死**——投影代次名派生自进程内状态计数器，状态重生（迁移 / 回滚 / 重部署）令计数器回退，选出的代次名与 VL 中**上一轮生命周期**的行重名；VL 只追加（「取代」仅改查询侧白名单）、校验只按代次名过滤，`(同源, 同名, 校验窗口)` 命中旧行即判 `unexpected or duplicate`，启动整窗重发时**必然**卡死（首启落在低号段侥幸过关，二启爬到旧整窗号段即死）。后果：节点离线、**11 台实例起不来**。可转红回归：`internal/worker/logs/ingest/generation_reuse_test.go`（把行为改回去即红，错误串与生产同类）。四轮真机记录（迁移 + 首启 / 停 + 二启 / 回滚 + 三启 / 双失重放）是这条纪律的来源。

**判据**：
- 验收脚本**逐轮**记录启动结果与落库关键字段（代次 / 游标 / 账本 / 索引行数），二启与三启必须**逐字段可解释**——不是只看「进程起来了、端口通了」；
- **脏环境**至少覆盖一次「上一轮遗留物仍在」的真实形态：VL 中仍留有旧代次行、残留 WAL、迁移前归档文件、未回收的段；
- 回滚脚本同样要过「回滚 → 启 → 停 → 再启」，不能只验回滚动作本身；
- 只有单轮首启证据（或只验「没报错」）的验收，**一律不算完成**。
- 部署阶段的配套要求见 [gate-merge.md](gate-merge.md)「部署后复验」。

## 测试规范

### Go

```go
// 文件: xxx_test.go
// 使用 testing + testify/assert
// 表驱动测试

func TestProcessManager_Start(t *testing.T) {
    tests := []struct {
        name    string
        input   InstanceConfig
        wantErr bool
    }{
        {"valid config", validConfig, false},
        {"empty command", emptyCommand, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := manager.Start(tt.input)
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

### TypeScript

```typescript
// 使用 vitest
// 文件: xxx.test.ts

describe('WsClient', () => {
  it('should reconnect on disconnect', async () => {
    // ...
  })
})
```

## 运行测试

```bash
# Go
go test ./...
go test -race ./...          # 竞态检测
go test -cover ./...         # 覆盖率

# TypeScript
cd web && npm test
cd bot-worker && npm test
```

## 质量门禁

- 新增代码的测试覆盖率不低于 60%
- 核心模块（process manager, state machine, protocol）覆盖率不低于 80%
- 不得提交导致现有测试失败的代码
