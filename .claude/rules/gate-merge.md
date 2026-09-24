# Gate 4: 合并/发版门禁

> 代码合并到 master 前必须满足以下条件。

## Checklist

### 一致性
- [ ] 实现和 `docs/specs/<feature>/api.md` 一致
- [ ] 实现和 `docs/ARCHITECTURE.md` 架构一致（未引入未记录的模式）
- [ ] 数据库 migration 和 ARCHITECTURE.md 中的表结构一致
- [ ] **FR 验收标准逐条通过，且由用户确认（Agent 不得自行标记 done）**

### 文档同步
- [ ] `docs/ARCHITECTURE.md` 已更新（如有架构变更）
- [ ] `docs/API.md` 已更新（如有 API 变更）
- [ ] `docs/adr/` 已追加（如有架构决策变更）
- [ ] `docs/PRD.md` 中对应 FR 状态已更新；已交付必须写成 `✅ 已交付@vX.Y.Z`，不得留下裸 `✅ 已交付`

### 代码质量
- [ ] 无编译错误
- [ ] 无 lint 错误
- [ ] 核心逻辑有测试覆盖

### 发版时额外检查
- [ ] `CHANGELOG.md` 已更新
- [ ] 版本号已确定
- [ ] 合并经 PR 完成，未直推主干；发版提交同样是独立 PR（chore(release)）
- [ ] **基线提交的远程 CI 已全绿**（见下节红线，未绿则禁止发版）

## 发版红线：CI 未绿禁止创建发版 commit 与 tag

> 发版 commit（`chore(release): 发布 X.Y.Z`）与 tag `vX.Y.Z` 是对「该提交已通过全部质量门禁」的承诺。CI 未绿就创建它们，等于把未经门禁验证的提交标记为可发布。

**硬性要求**：创建任何发版 commit 与 tag **之前**，必须先确认**基线提交**（发版 commit 的父提交，即当前主干 tip）在远程 CI 上**全绿**。

- **唯一判定依据是远程 CI 结论，不得用本地测试结果替代。** 本地 `go test` / `vitest` 全绿**不能**代替 CI：开发机负载与环境差异会让两者不等价（实例：本机 `load average ≈ 30` 时前端 vitest 因 10s `testTimeout` 批量假失败，而**同一提交**在 CI 上 2349 用例全绿）。
- **CI 红即禁止发版，flake 变红同样禁止。** flake 不是豁免理由——它使门禁本身不可信，必须先使其变绿（修 spec 稳定性，或重跑并确认结论为 success）再发版。
- 核对命令（要求 `conclusion == "success"` 且 `headSha` 等于待发版的基线提交）：
  ```sh
  gh run list --workflow=ci.yml --limit 1 --json conclusion,headSha,displayTitle
  ```
- **禁止「先打好 commit/tag，等 CI 绿了再推」。** commit 与 tag 一旦创建，本地就已形成「已发布」事实，极易随 `push --follow-tags` 意外外泄。CI 未绿时，`version.go`、CHANGELOG 归档段、tag 一律不得改动。
- CI 红时的正确顺序：定位原因 → 修复（经 PR 合入主干）→ 等主干 CI 全绿 → 再执行发版流程。
- 已在 CI 未绿状态下误建的发版提交与 tag，应**立即撤销**（删除本地 tag + 回退发版提交），待 CI 转绿后重新执行。

## 检查方式

按此 checklist 逐项核对。
