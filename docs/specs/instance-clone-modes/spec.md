# 实例复制双模式（FR-231）

> 状态：开发中 · 增强 FR-036（一键复制）· **改 proto**（CloneWorkDirRequest 加 include）

## 1. 需求

复制实例支持两种模式（原始诉求 #13 复制部分）：

1. **快速复制**：只复制 **核心 jar + plugins/ + 根配置**（`server.properties` 及根 `*.yml`/`*.yaml`/`*.properties`），**不含** world/logs/cache 等。
2. **高级复制**：打开目录选择器，选要复制的文件夹，支持 **include / exclude 筛选**（空 include = 复制全部，等同原「直接复制」）。

二者都仍排除运行态垃圾（session.lock/*.pid/logs/crash-reports/cache/usercache.json）。

## 2. 设计

### 2.1 proto

`CloneWorkDirRequest` 加 `repeated string include = 4`：
- include 为空 → 复制全部（仅受 exclude 约束，**向后兼容**当前行为）。
- include 非空 → 仅复制「顶层段命中 include」的项（目录前缀 / basename glob），仍叠加 exclude。

### 2.2 Worker（clone_ops.go）

`copyDirExcluding` 扩为 `copyDirFiltered(src, dst, include, exclude)`：Walk 时先判 `cloneIncluded(rel, include)`（include 空=全包含；否则 rel 首段匹配某 include 模式），不包含则跳过（目录 SkipDir）；再判既有 `cloneExcluded`。`cloneIncluded` 支持 `plugins`、`*.jar`、`server.properties`、`*.yml` 等顶层段匹配（自动去 `/**`、`/` 后缀）。

### 2.3 CP（clone.go）

- `CloneInstanceRequest` 加 `Mode string`（`quick`/`advanced`，默认 advanced）+ `Include []string` + `Exclude []string`。
- `quickCloneIncludes = ["*.jar","plugins","server.properties","*.yml","*.yaml","*.properties"]`。
- `Clone` 据 mode 派生 (include, exclude) 传 `cloneWorkDirOnWorker`：
  - quick → include=quickCloneIncludes，exclude=defaultCloneExcludes。
  - advanced/默认 → include=req.Include（空=全部），exclude=req.Exclude ∪ defaultCloneExcludes（始终排垃圾）。
- 端点 `POST /instances/:id/clone` 不变（请求体加字段，自动绑定）。

### 2.4 前端（CloneInstanceDialog）

- 模式切换：快速复制（默认，附说明「仅核心 jar + 插件 + 根配置」）/ 高级复制。
- 高级：目录选择器（复用 `DirectoryPicker`，浏览源工作目录）选要复制的顶层项 → 生成 include；可加 exclude（逗号/换行分隔的 glob）。
- 提交带 `mode`/`include`/`exclude`。

## 3. 验收

- [ ] 快速复制：目标只出现核心 jar + plugins/ + 根配置，无 world/logs/cache。
- [ ] 高级复制：按 include/exclude 生效；空 include = 复制全部（减垃圾）。
- [ ] include 为空时行为与原一键复制一致（向后兼容）。
- [ ] 后端单测（`cloneIncluded` 首段匹配 / `copyDirFiltered` include+exclude 组合）+ 前端 tsc/lint/vitest 绿；mock 域不破。
- [ ] **真机验**：真复制一个实例（快速 / 高级各一次），核对目标目录内容符合预期。

## 4. 关联

proto `CloneWorkDirRequest.include`；worker `grpc/clone_ops.go`；CP `service/clone.go`；web `components/CloneInstanceDialog.tsx`、`api/clone.ts`。
