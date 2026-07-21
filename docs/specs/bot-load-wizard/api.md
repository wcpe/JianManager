# API：FR-371 压测模板与创建向导前端

> 权威完整定义：`../bot-load-platform/api.md`
> 命令成功边界：`../../adr/075-bot-command-orchestration.md`

本 FR 不新增后端 endpoint，只消费：

- load-nodes
- load-templates CRUD
- template runs/create stress session
- preflight
- start

向导提交的场景是通用命令编排计划；内置预设使用 `command-orchestration-v1`。连接配置、命令内容、命令间隔和顺序均来自运行快照，不要求 room、area、monster、tower 等业务字段，也不要求 ServerProbe。

preflight 只验证目标实例作用域、执行节点容量、命令计划结构、负载曲线和阈值配置是否可调度；连接配置在创建运行快照时校验，不由 preflight 重复验证。缺少平台 `bot:manage` 权限返回 403；目标实例/会话不存在或不可访问返回 404 以隐藏资源存在性。游戏服命令权限、命令执行结果或业务事件不作为 ready 条件。前端不得新增未进入共享 API 的校验或预览 endpoint。
