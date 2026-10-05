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

#### E2E 失败排查：先做隔离复现，不要按失败信息推断

**失败信息指向的方向，往往不是真正的原因。** 一次全站排查（79 个失败）中实测到的三次误判：

| 失败信息 | 直觉判断 | 实际原因 |
|---|---|---|
| `element(s) not found` | 页面 / 组件没渲染 | 断言超时过短（默认 5s，而页面要 8s+ 才出页头） |
| `locator.click: Test timeout` | 按钮不存在 | 被浮层遮挡（`intercepts pointer events` 藏在 Call log 中段） |
| 放宽断言超时后仍失败 | 冷启动慢 | **test timeout** 预算不足（默认 30s）——断言超时再大也没用 |

那 79 个失败的根因里，**只有 1 个是产品缺陷**，其余是就绪信号选错、超时预算不足、结构 / 文案变更未同步、浮层遮挡。

**排查动作**：把失败用例的**每一步**照搬进一个临时 spec（含 `beforeEach` 的初始化、网络等待、滚动、截图等，一步不漏），单独跑：

- **隔离环境通过** → 问题在**用例自身的配置**（超时预算、就绪信号、选择器），不在被测代码；
- **隔离环境同样失败** → 才去查被测代码。

**只照搬「看起来相关」的步骤会漏掉真因**——曾因少搬了一步 `addInitScript` 与一次网络轮询，两次得出「隔离环境正常」的错误结论。

临时 spec 用完即删，不得留在 `e2e/` 下。

| 场景 | 覆盖要求 |
|---|---|
| 实例完整生命周期 | 创建→启动→运行→停止→删除 |

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
