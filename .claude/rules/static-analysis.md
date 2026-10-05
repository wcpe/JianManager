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

- `tsc -b` — 无类型错误（**不能用 `tsc --noEmit`**，原因见下）
- `eslint .` — 无 error

> **为什么必须是 `tsc -b`**：`apps/control-plane-web/tsconfig.json` 是 `files: []` + `references`
> 形式（真正的配置在 `tsconfig.app.json` / `tsconfig.node.json`）。这种工程里
> `tsc --noEmit` **一个文件都不会检查**，且退出码恒为 0 —— 看上去「通过」，实则什么都没做，
> 于是 `Expected corresponding JSX closing tag` 这类硬错误会被漏掉。`tsc -b` 才会沿
> references 逐个构建检查，本仓 `build` script 用的也正是它。

### 规则

- 不得使用 `any` 类型（除非有注释说明理由）
- 不得有未使用的导入
- React 组件必须有明确的 props 类型定义

## 提交前检查

```bash
# Go
go vet ./...
golangci-lint run

# TypeScript（等价 task lint / task bot:lint）
cd apps/control-plane-web && pnpm exec tsc -b --noEmit && pnpm lint
cd apps/bot-worker && npx tsc --noEmit && npm run lint
```
