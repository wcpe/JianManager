/**
 * 权限节点「大白话」说明（FR-432）。
 * 键 = 权限节点 id；值 = 一句话说清「开了能干什么」。
 */
export const PERM_EXPLAIN_ZH: Record<string, string> = {
  'user.read': '能打开用户列表，查看账号、角色和状态。',
  'user.manage': '能创建、改角色、禁用/删除用户，还能发邀请。',
  'group.read': '能查看用户组列表和组内有哪些人。',
  'group.manage': '能新建/改名/删除用户组，调整组配额。',
  'group.member.write': '能把用户加进组、从组里移除。',
  'group.quota.write': '能改组的实例数、Bot 数、存储等配额上限。',
  'node.read': '能看节点列表、在线状态和资源占用。',
  'node.manage': '能注册/维护/修复节点，改节点配置。',
  'settings.read': '能查看平台设置项。',
  'settings.write': '能修改平台设置（端口、存储路径、邮件等）。',
  'system.update': '能升级/回滚面板（Control Plane）本身。',
  'system.db.browse': '能只读浏览平台数据库表。',
  'license.read': '能查看开源许可信息。',
  'audit.read': '能查看谁在什么时候做了什么操作。',
  'rbac.read': '能查看角色模板和权限配置。',
  'rbac.manage': '能改角色模板节点、给用户绑角色和覆盖权限。',

  'instance.read': '能看服务器实例列表、状态和基础信息。',
  'instance.create': '能新建游戏服实例（含一键搭建）。',
  'instance.write': '能改实例配置（名称、自动重启等）。',
  'instance.operate': '能启动、停止、重启实例。',
  'instance.delete': '能删除实例（会动数据，谨慎给）。',
  'instance.launchspec.write': '能改启动命令、JVM 参数、环境变量——影响进程怎么跑。',
  'instance.business.write': '能改经济余额、背包等业务插件高危数据。',
  'file.read': '能浏览和下载实例里的文件。',
  'file.write': '能上传、编辑、删除实例文件。',
  'terminal.access': '能打开控制台终端，直接敲命令。',
  'bot.read': '能查看 Bot 列表和状态。',
  'bot.manage': '能创建/停止/管理 Bot 和压测会话。',
  'backup.read': '能查看备份列表。',
  'backup.write': '能创建备份、从备份恢复。',
  'schedule.read': '能查看定时任务。',
  'schedule.write': '能新建/修改/删除定时任务。',
  'task.read': '能打开任务中心看进度。',
  'task.manage': '能强停任务等管理操作。',

  'monitor.read': '能看监控图表（CPU/内存/TPS 等）。',
  'log.read': '能查看平台与实例日志。',
  'stats.read': '能查看统计分析页。',
  'alert.read': '能看告警记录。',
  'alert.manage': '能配置告警规则和通知渠道。',
  'notification.read': '能查看通知中心消息。',

  'template.read': '能查看服务端搭建模板。',
  'template.manage': '能新建/改/删模板。',
  'channel.read': '能查看客户端分发频道与版本。',
  'channel.write': '能创建/修改分发频道。',
  'dist.publish': '能向频道发布客户端整合包版本。',
  'dist.ops.read': '能看分发运维页（统计/日志/画像）。',
  'dist.ops.write': '能做分发安全处置（封 IP、处置规则等）。',

  'agent.token.read': '能查看 Agent Token 列表。',
  'agent.token.manage': '能创建/吊销 Agent Token。',
  'agent.mcp.read': '能查看 MCP 会话。',
  'agent.calllog.read': '能查看 Agent 调用流水。',

  'network.read': '能查看群组网络与拓扑。',
  'network.manage': '能创建/修改网络群组与成员关系。',
  'player.read': '能查看在线/历史玩家。',
  'player.manage': '能踢人、封禁等玩家治理。',
  'super.read': '能打开超级工作台（多实例拼屏）。',
  'director.read': '能打开导播台（场景切换）。',
}

export const PERM_DOMAIN_EXPLAIN_ZH: Record<string, string> = {
  platform: '用户、组、节点、系统设置与权限配置等平台级能力。',
  runtime: '游戏服实例、文件、终端、Bot、备份与定时等日常运维。',
  observability: '监控、日志、统计、告警与通知——看运行状况。',
  distribution: '服务端模板与玩家客户端 OTA 分发相关能力。',
  agent: '外部 Agent / MCP 接入与调用审计。',
  workspace: '网络群组、玩家治理与多服工作台。',
}

export function permExplain(id: string, fallback?: string): string {
  return PERM_EXPLAIN_ZH[id] || fallback || '暂无说明'
}

export function permDomainExplain(domain: string, fallback?: string): string {
  return PERM_DOMAIN_EXPLAIN_ZH[domain] || fallback || ''
}
