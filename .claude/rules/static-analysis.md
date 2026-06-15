# 静态分析规范

## Go

### 必须通过

- `go vet ./...` — 无警告
- `golangci-lint run` — 无 error 级别问题

### 推荐的 golangci-lint 配置

```yaml
# .golangci.yml
linters:
  enable:
    - errcheck      # 检查未处理的 error
    - govet         # go vet
    - staticcheck   # staticcheck
    - unused        # 未使用的变量/函数
    - gosimple      # 简化建议
    - ineffassign   # 无效赋值
    - misspell      # 拼写检查
    - gocritic      # 代码风格检查

linters-settings:
  errcheck:
    check-blank: true

issues:
  exclude-rules:
    - path: _test\.go
      linters: [errcheck]
```

### 规则

- 不得有未处理的 error（`errcheck`）
- 不得有未使用的变量或导入（`unused`）
- 不得有竞态条件（`go vet -race`）

## TypeScript (前端 + Bot Worker)

### 必须通过

- `tsc --noEmit` — 无类型错误
- `eslint .` — 无 error

### 规则

- 不得使用 `any` 类型（除非有注释说明理由）
- 不得有未使用的导入
- React 组件必须有明确的 props 类型定义

## 提交前检查

```bash
# Go
go vet ./...
golangci-lint run

# TypeScript
cd web && tsc --noEmit && npm run lint
cd bot-worker && tsc --noEmit && npm run lint
```
