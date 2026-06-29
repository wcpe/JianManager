# FR-222 实现任务清单

> 开发中持续打勾；完成后状态标 ✅ done。

## ADR / 文档

- [x] 写 ADR-051（worker 免配置自启 setup，accepted，改写 ADR-020 单脚本编排立场，supersedes 引用 ADR-020）
- [x] 写 spec.md（目的 / 设计 / TTY 与无 TTY 双路径 / worker.yml 字段 / 注册编排 / 未配置判定 / 验收）
- [x] ADR-020 状态行追加「§2 安装编排立场 superseded-by ADR-051」
- [x] 同步 ARCHITECTURE.md §5.1（节点接入：补 worker 免配置自启 setup）
- [x] 同步 PRD §4 FR-222 状态 → 🔨 开发中（交付由用户确认后改 ✅）

## 实现

- [x] `config.FindConfigFile` 导出（自检复用，原 unexported `findConfigFile` 改导出 + 内部调用更新）
- [x] `config` 新增 `IsConfigured`/未配置判定辅助（探测 worker.yml + node-identity.json，只解析 data-dir 路径不建目录）
- [x] 新增 `internal/worker/setup` 包：入参采集（TTY 交互 + 无 TTY 参数/env 双形态）+ 写 worker.yml（原子）+ 编排（注册 + 持久化身份）
- [x] worker 入口 `runWorker` 前置自检：未配置 → setup → 以 setup 产物转 run；已配置 → 现状不变
- [x] run 主体识别「身份已由 setup 持久化」跳过重复注册（复用首注册结果）

## 测试

- [x] 未配置判定单测（无 yml + 无身份 = 未配置；任一存在 = 已配置；显式配置文件路径 = 已配置）
- [x] 无 TTY 参数解析单测（flag 优先于 env，缺必填报错）
- [x] worker.yml 写出字段单测（字段正确 + token 不在文件中 + data_dir 缺省不写）
- [x] 注册编排单测（mock register，验证身份持久化落 0600）

## 质量门

- [x] `go build ./...` 绿
- [x] `go vet ./...` 绿
- [x] `go test ./...` 绿
