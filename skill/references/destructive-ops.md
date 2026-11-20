# 破坏性操作

`destructiveHint: true` 的工具会永久删除数据或强制中断服务，**没有回收站，不可恢复**。

## 当前破坏性工具清单

| 工具 | 后果 | 需确认？ |
|---|---|---|
| `instance_delete` | 删除实例记录与其工作目录 | 是（`confirmInstanceName`） |
| `instance_kill` | 强制终止进程，不保证优雅退出与存档落盘 | 是（`confirmInstanceName`） |
| `node_purge_archived` | 永久清除已归档节点 | 是（`confirmNodeName`） |
| `bot_delete` | 删除 Bot 及其配置 | 否 |
| `file_delete` | 删除实例内文件 | 否 |
| `plugin_delete` | 删除实例插件 | 否 |
| `loadtest_template_delete` | 删除压测模板 | 否 |

工具自身携带 `annotations.destructiveHint`，可直接判定，无需查阅本表。
本表用于让用户理解后果。

## 精确确认参数

带 `confirmField` 标注的工具要求传入目标的**当前名称**，服务端逐字比对：

- **区分大小写**
- **不 trim 前后空格**
- 与目标真实名称不一致 → 拒绝，返回 "确认名称与目标不符"

正确流程：

```
1. 先查真实名称：agent_get_instance / agent_list_instances / node 相关工具
2. 把「目标名称 + 要执行的操作 + 后果」展示给用户
3. 取得用户明确同意
4. 用第 1 步查到的原始名称填入 confirmInstanceName / confirmNodeName
```

**绝不** 在不知道真实名称时猜测、用 `id` 冒充名称、或反复试错。
确认比对在授权之后执行，所以错误的名称不会泄露其他 scope 的信息，但会让你拿不到结果。

## 执行前自检

- 用户是否明确说了「删除 / 强杀 / 清除」？含糊的"清理一下""处理掉"不算同意。
- 目标是否在运行中？**运行中实例优先用 `instance_stop`，不要直接 kill。**
  kill 会跳过优雅停服，可能丢存档。
- 是否已确认无正在进行的数据写入或压测？压测期间删除实例会导致该运行悬空。
- 是否有备份或快照可回退？`instance_list_crash_snapshots` 可查崩溃快照。

## 不可恢复性说明

- `instance_delete` 删除工作目录，服务端不回滚。
- `instance_kill` 中断进程，未落盘的数据丢失。
- `node_purge_archived` 清除的是已归档节点，清除后无法再从归档恢复。

向用户复述结果时，必须说明**不可恢复**，不能只说"操作成功"。

## 误操作后的动作

没有撤销接口。按顺序：

1. `instance_list_crash_snapshots` 查是否有可用快照
2. 节点级操作查 `agent_list_nodes` 确认节点状态
3. 无法恢复时，如实告知用户，不要尝试用其他写工具"修复"——那会扩大损害面
