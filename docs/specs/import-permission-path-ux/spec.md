# 功能规格：导入现有服务器 — 手输路径 + 权限引导 + 写预检

> 状态：开发中（DOM 测绿）　·　关联 PRD：FR-374　·　分支：feature/fr-374-import-permission-path-ux  
> 依赖：FR-373（`check-access` / `chmod` / 中文权限诊断）  
> 增强：FR-302 / ADR-069　·　计划：`.tmp/brainstorm-file-explorer-perm-ux-2026-07-23.md`

## 1. 背景与目标

### 问题（用户现场）
- `读取目录失败: open /home/wxys233: permission denied`：只能点选浏览，失败文案裸 errno，无引导。
- 无法自己输入绝对路径做探测。
- 导入后写 `server.properties` 才报 `permission denied`，导入半成功。

### 目标
在 **导入向导** 内：
1. **浏览 + 手输绝对路径**（回车/按钮触发探测）；
2. 读目录 / inspect 失败时展示 **权限诊断** + 动作：**尝试修复(chmod)** / 换路径 / 改 **migrate**；
3. **提交前**（及可选在 inspect 后）对导入根与 `server.properties` 做 **可写预检**；就地不可写则阻断提交并引导。

属阶段：P0（与 FR-375 可并行开发，合入需 373 在 main）。

## 2. 需求（要什么）

### 范围内
1. **路径输入**（向导 step `dir`）  
   - 保留 `DirectoryPicker` 点选。  
   - 增加绝对路径输入框（monospace）：粘贴/键入后「探测」或 Enter → 调既有 `POST /instances/import/inspect`。  
   - 点选成功时同步回填输入框路径。

2. **权限失败体验**  
   - inspect / BrowseDir 失败：toast **或** 内联 Alert 展示 CP/Worker 中文 `message`（FR-373 已映射的文案优先）。  
   - 若错误语义为权限：展示动作条：  
     - **尝试修复权限**：`POST /nodes/:id/fs/chmod`（path=当前导入根；mode 空=FR-373 默认 u+rwx 目录语义）；二次确认 `DangerConfirm`。成功后自动重试 inspect。  
     - **换路径**：焦点回输入框。  
     - **改用搬进托管区**：预选 mode=migrate 并提示「仍需能读源目录才能搬迁」。  
   - 不可读目录：不强行 chmod 成功则继续阻断就地导入。

3. **写前预检**  
   - inspect 成功后（进入 step `inspect` 或提交前）：  
     - `POST /nodes/:id/fs/check-access` path=导入根 → 目录应 readable；就地模式还须 writable（migrate 模式提交时源只需 readable，目标由托管区创建）。  
     - 若存在 `server.properties`（`propsFound`）：再 check 该文件绝对路径 `join(root, "server.properties")`；就地模式要求 writable。  
   - 就地 + 不可写：禁止提交，展示诊断 + 尝试修复（对不可写 path）/ 改 migrate。  
   - migrate：源目录须 readable；不要求源 writable。

4. **i18n**  
   - `importServer.*` 增补：pathPlaceholder、pathProbe、permDeniedTitle、tryFixPerm、fixAfterFix、precheckWritable、precheckBlocked、switchMigrate 等（zh/en 对称）。

5. **测试**  
   - DOM：手输路径触发 inspect mock；权限错误展示动作条；就地不可写阻断提交。  
   - 不改后端 import RPC 语义（仅消费 FR-373 节点 fs API）。

### 不做
- 批量导入、自动改端口、chown/root/递归 chmod。  
- 资源管理器权限列/历史（FR-375）。  
- 改 ADR-069 就地/删除语义。

## 3. 设计（怎么做）

### 3.1 前端改动点
| 文件 | 改动 |
|---|---|
| `ImportServerWizard.tsx` | 路径 state 与输入框；`runInspect` 错误分流；precheck 钩子；chmod 确认流 |
| `DirectoryPicker.tsx`（可选） | 权限错误透传改进（已有 errMsg）；可不改 |
| `api/nodeRuntime.ts` | 复用 FR-373 `checkNodePathAccess` / `chmodNodePath` |
| `api/importServer.ts` | 不变 inspect/import 契约 |
| i18n zh/en | 新键 |
| `ImportServerWizard.dom.test.tsx` | 扩测 |

### 3.2 预检伪代码
```
onInspectSuccess(path, result):
  rootAccess = checkNodePathAccess(nodeId, path)
  if !rootAccess.readable → 阻断进入后续，展示诊断+修复
  if mode will be in_place:
    if !rootAccess.writable → 标记 precheckFail
    if result.propsFound:
      p = join(path, "server.properties")
      fa = checkNodePathAccess(nodeId, p)
      if fa.exists && !fa.writable → precheckFail
  允许进入 inspect/mode/config；提交时若 in_place && precheckFail → 禁止

onTryFix(path):
  DangerConfirm → chmodNodePath(nodeId, path) → re-check → re-inspect
```

### 3.3 路径拼接
- 前端 join 绝对路径：识别 Windows `\` 与 Unix `/`（与向导已有 `dirBaseName` 一致），用简单规则：`path.endsWith(/[\\/]/) ? path+rel : path+sep+rel`。

## 4. 任务拆分

- [x] 向导：绝对路径输入 + 与 DirectoryPicker 双向同步  
- [x] inspect 错误：权限诊断 UI + 尝试修复 / 换路径 / 建议 migrate  
- [x] 写预检：根 + server.properties；就地阻断提交  
- [x] i18n zh/en  
- [x] 路径工具单测 + DOM 测试（手输探测 / 权限失败区）  
- [x] 文档：PRD 开发中；CHANGELOG  
- [ ] 真机：无权限 `/home/...` 手输 → 诊断 → 修复或 migrate 可导入（待用户节点）

## 5. 验收标准

1. 手输绝对路径可触发 inspect，成功进入探测结果步。  
2. 权限类失败：可见中文诊断，不只 `permission denied` 裸串。  
3. 「尝试修复」经确认调用节点 chmod，成功后可重试探测。  
4. 就地 + 根或 server.properties 不可写：提交按钮禁用或提交被拒，并引导 migrate/修复。  
5. migrate 模式：源可读即可提交（不要求源可写）。  
6. DOM 测绿；i18n 中英键对称。  
7. 真机（用户确认）：复现 wxys233 类目录场景至少一条闭环。

## 6. 风险 / 待定

- chmod 常因非属主失败：必须保留 migrate/换路径，不假装总能修。  
- Windows 路径与 Worker Linux 节点混用：输入框提示「节点本机绝对路径」。  
- 与 FR-375 无代码冲突面小；并行时注意 i18n 键不撞。
