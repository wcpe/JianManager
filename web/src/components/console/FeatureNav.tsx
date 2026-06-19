import SidebarNavLink, { type NavEntry } from './SidebarNavLink'

/**
 * 左栏上段：功能导航（与实例运维强相关）。
 * 仪表盘 / 节点 / 实例 / Bot / 告警 / 模板 / 计划任务 / 备份（ADR-009）。
 */
const featureNav: NavEntry[] = [
  { to: '/', labelKey: 'nav.dashboard' },
  { to: '/nodes', labelKey: 'nav.nodes' },
  { to: '/instances', labelKey: 'nav.instances' },
  { to: '/networks', labelKey: 'nav.networks' },
  { to: '/bots', labelKey: 'nav.bots' },
  { to: '/alerts', labelKey: 'nav.alerts' },
  { to: '/templates', labelKey: 'nav.templates' },
  { to: '/schedules', labelKey: 'nav.schedules' },
  { to: '/backups', labelKey: 'nav.backups' },
]

export default function FeatureNav() {
  return (
    <nav className="space-y-0.5 p-2">
      {featureNav.map((item) => (
        <SidebarNavLink key={item.to} {...item} />
      ))}
    </nav>
  )
}
