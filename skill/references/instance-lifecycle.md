# 实例生命周期

## 状态机

```
created ──start──▶ running ──stop──▶ stopped ──start──▶ running
                      │                  │
                      └──kill──▶ killed   └──delete──▶ （永久消失）
                                     │
                                     └──start──▶ running（重建容器后）
```

- `running`：进程存活。控制台命令可下发。
- `stopped`：优雅停止，存档已落盘。可安全 start。
- `killed`：被强制终止，可能丢失未落盘数据。可 start，但应先确认存档完整性。

## 工具选择

| 目标 | 工具 | 能力要求 |
|---|---|---|
| 看状态 | `agent_get_instance` | `instance.read` |
| 看指标 | `agent_get_instance_metrics` | `observability.read` |
| 看日志 | `agent_get_instance_logs` | `observability.read` |
| 启动 | `instance_start` | `instance.life` |
| 优雅停止 | `instance_stop` | `instance.life` |
| 重启 | `instance_restart` | `instance.life` |
| 下发控制台命令 | `instance_send_command` | `instance.command` |
| 强制终止 | `instance_kill` | `instance.destructive` |
| 删除 | `instance_delete` | `instance.destructive` |

**停止优先用 `instance_stop`，不要用 `instance_kill`。** 前者走优雅停服并落盘。

## 常用排查顺序

实例异常时按此顺序，避免盲目重启：

1. `agent_get_instance` — 确认实际状态与你以为的是否一致
2. `agent_get_instance_logs` — 读日志找报错，先看尾部
3. `agent_get_instance_metrics` — 确认是资源耗尽还是业务异常
4. `instance_list_crash_snapshots` — 若发生过崩溃，查快照元数据
5. 确需重启时才 `instance_restart`

**不要在没看日志的情况下重启。** 重启会清掉现场，可能让问题无法复现。

## 下发控制台命令

`instance_send_command` 把文本发到游戏服控制台，等价于在服务端控制台输入。

- 只有 `running` 状态可下发，其余状态服务端直接拒绝
- 返回成功仅表示命令已送达，**不代表业务已生效**
- 不要下发 `stop` / `op` 之类改管理状态的命令绕开生命周期工具——
  用 `instance_stop` 才能保证落盘与状态同步

## 批量操作

`instance_batch` 对多个实例执行同生命周期操作（`instance.life` 能力）。

执行前必须：
- 列出每个目标实例的**当前状态**
- 向用户确认批量范围
- 说明部分失败时的表现（各自独立，不会整体回滚）

## 创建与导入

| 目标 | 工具 |
|---|---|
| 在节点上新建 | `instance_create` |
| 导入既有实例（先检视） | `instance_import_inspect` → `instance_import` |
| 复制现有实例 | `instance_clone` |
| 重建 | `instance_rebuild` |

导入必须先 `instance_import_inspect` 看清将导入什么，再执行 `instance_import`。
跳过检视直接导入可能覆盖目标路径下的既有内容。

## 节点维护模式

进入维护模式（`node_maintenance_enter`）会**拒绝该节点上的新实例调度**（`ErrNodeInMaintenance`），
但**不会自动停止既有实例**——既有实例会继续运行。

因此把节点下线前，正确顺序是：

1. 逐个 `instance_stop` 停掉该节点上的实例
2. 确认全部 `stopped`
3. 再 `node_maintenance_enter`

反过来先进维护模式不会让实例停下，只会让它们无人管理地继续跑。
