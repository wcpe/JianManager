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
