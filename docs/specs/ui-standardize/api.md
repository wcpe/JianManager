# API Spec — FR-026 前端 shadcn/ui 标准化

> 关联 FR: FR-026 | 优先级: P1

## 概述

前端 12 个页面/组件中，仅 LoginPage 和 SetupPage 使用了 shadcn/ui 组件。其余 10 个文件全部使用手写原生 HTML 标签。本 FR 将所有页面统一迁移到 shadcn/ui 组件库默认样式。

## 需要安装的 shadcn/ui 组件

当前已安装：button, card, input, label

需要新增安装：
- `table` — 用于所有数据表格
- `dialog` — 用于所有弹窗/对话框
- `select` — 用于下拉选择
- `checkbox` — 用于复选框
- `badge` — 用于状态标签
- `tabs` — 用于 InstanceDetailPage 的 Tab 切换

## 迁移范围

| 文件 | 需迁移的元素 |
|---|---|
| NodesPage.tsx | table → Table |
| InstancesPage.tsx | table → Table, button → Button |
| InstanceDetailPage.tsx | button → Button, table → Table, tabs → Tabs, checkbox → Checkbox |
| BotsPage.tsx | table → Table, button → Button, select → Select, modal → Dialog |
| UsersPage.tsx | table → Table, button → Button |
| GroupsPage.tsx | button → Button, 卡片布局 → Card |
| CreateInstanceDialog.tsx | modal → Dialog, input → Input, select → Select, checkbox → Checkbox, button → Button |
| CreateUserDialog.tsx | modal → Dialog, input → Input, select → Select, button → Button |
| CreateGroupDialog.tsx | modal → Dialog, input → Input, textarea → Textarea, button → Button |
| FileBrowser.tsx | button → Button, textarea → Textarea |

**不需迁移**: LoginPage.tsx, SetupPage.tsx（已使用 shadcn）

## 参考样式

LoginPage.tsx 和 SetupPage.tsx 已有的 shadcn 用法作为风格参考：
```tsx
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
```
