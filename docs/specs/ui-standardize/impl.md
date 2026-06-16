# 实施计划 — FR-026 前端 shadcn/ui 标准化

> 关联 FR: FR-026 | 优先级: P1 | 状态: 🔨 in-progress

## 背景

前端 12 个页面/组件中，仅 LoginPage 和 SetupPage 使用了 shadcn/ui。其余 10 个文件使用手写原生 HTML，视觉风格不统一。

## 任务拆解

### Phase 1: 安装 shadcn/ui 组件

- [ ] `cd web && npx shadcn@latest add table dialog select checkbox badge tabs textarea`

### Phase 2: 页面迁移（按优先级）

- [ ] NodesPage.tsx — table → Table
- [ ] InstancesPage.tsx — table → Table, button → Button
- [ ] InstanceDetailPage.tsx — button → Button, table → Table, tabs → Tabs, checkbox → Checkbox
- [ ] BotsPage.tsx — table → Table, button → Button, select → Select, modal → Dialog
- [ ] UsersPage.tsx — table → Table, button → Button
- [ ] GroupsPage.tsx — button → Button, 卡片布局 → Card

### Phase 3: 组件迁移

- [ ] CreateInstanceDialog.tsx — modal → Dialog, input → Input, select → Select, checkbox → Checkbox, button → Button
- [ ] CreateUserDialog.tsx — modal → Dialog, input → Input, select → Select, button → Button
- [ ] CreateGroupDialog.tsx — modal → Dialog, input → Input, textarea → Textarea, button → Button
- [ ] FileBrowser.tsx — button → Button, textarea → Textarea

### Phase 4: 验证

- [ ] `cd web && npx tsc --noEmit` 通过
- [ ] 暗色/亮色主题切换正常

## 产出文件范围

| 文件 | 操作 |
|---|---|
| `web/src/components/ui/*.tsx` | 新增（table, dialog, select, checkbox, badge, tabs, textarea） |
| `web/src/pages/NodesPage.tsx` | 修改 |
| `web/src/pages/InstancesPage.tsx` | 修改 |
| `web/src/pages/InstanceDetailPage.tsx` | 修改 |
| `web/src/pages/BotsPage.tsx` | 修改 |
| `web/src/pages/UsersPage.tsx` | 修改 |
| `web/src/pages/GroupsPage.tsx` | 修改 |
| `web/src/components/CreateInstanceDialog.tsx` | 修改 |
| `web/src/components/CreateUserDialog.tsx` | 修改 |
| `web/src/components/CreateGroupDialog.tsx` | 修改 |
| `web/src/components/FileBrowser.tsx` | 修改 |
