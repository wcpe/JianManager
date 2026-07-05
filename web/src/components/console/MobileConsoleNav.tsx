import { useEffect, useRef, useState } from 'react'
import { Link, NavLink, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { cn } from '@jianmanager/ui'
import { navGroupsForRole, type NavGroup, type NavSection } from './nav-config'

/** 收集一个分组下所有可导航路由，用于移动端主域高亮。 */
function groupRoutes(group: NavGroup): string[] {
  if (group.to) return [group.to]
  const fromChildren = group.children?.map((c) => c.to) ?? []
  const fromSections = group.sections?.flatMap((s) => s.children.map((c) => c.to)) ?? []
  return [...fromChildren, ...fromSections]
}

function isRouteActive(pathname: string, to: string): boolean {
  if (to === '/') return pathname === '/'
  if (to === '/networks') return pathname === '/networks'
  return pathname === to || pathname.startsWith(`${to}/`)
}

/** 手机端底部导航：侧栏在小屏隐藏时的等价入口。 */
export default function MobileConsoleNav() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const groups = navGroupsForRole(role)
  const location = useLocation()
  const [openKey, setOpenKey] = useState<string | null>(null)
  const [panelGroup, setPanelGroup] = useState<NavGroup | null>(null)
  const [panelState, setPanelState] = useState<'open' | 'closed'>('closed')
  const closeTimer = useRef<number | null>(null)

  function clearCloseTimer() {
    if (closeTimer.current === null) return
    window.clearTimeout(closeTimer.current)
    closeTimer.current = null
  }

  function closePanel() {
    clearCloseTimer()
    const closingKey = panelGroup?.key
    setOpenKey(null)
    setPanelState('closed')
    if (!closingKey) return

    closeTimer.current = window.setTimeout(() => {
      setPanelGroup((current) => (current?.key === closingKey ? null : current))
      closeTimer.current = null
    }, 180)
  }

  function togglePanel(group: NavGroup) {
    if (openKey === group.key) {
      closePanel()
      return
    }

    clearCloseTimer()
    setPanelGroup(group)
    setOpenKey(group.key)
    setPanelState('open')
  }

  useEffect(
    () => () => {
      if (closeTimer.current !== null) window.clearTimeout(closeTimer.current)
    },
    [],
  )
  const linkClass = ({ isActive }: { isActive: boolean }) =>
    cn(
      'flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.98]',
      isActive
        ? 'bg-accent font-semibold text-primary shadow-[inset_3px_0_0_var(--primary)]'
        : 'text-foreground/85 hover:bg-accent/60 hover:text-foreground',
    )

  return (
    <nav
      data-slot="mobile-console-nav"
      aria-label="移动导航"
      className="fixed inset-x-0 bottom-0 z-40 border-t bg-card/96 px-2 pb-[max(env(safe-area-inset-bottom),0.5rem)] pt-2 shadow-[0_-16px_32px_-28px_rgb(var(--shadow-color)/0.45)] backdrop-blur-xl sm:hidden"
    >
      {panelGroup && (
        <div
          data-slot="mobile-nav-panel"
          data-state={panelState}
          className="jm-mobile-nav-panel absolute inset-x-2 bottom-[calc(100%+0.5rem)] overflow-hidden rounded-lg border bg-card/98 p-2 shadow-lift"
        >
          <div className="mb-1 flex items-center justify-between px-2 py-1 text-xs font-medium text-muted-foreground">
            <span>{t(panelGroup.labelKey)}</span>
            <button
              type="button"
              onClick={closePanel}
              className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
              aria-label="收起移动导航"
            >
              <ChevronDown className="size-4" />
            </button>
          </div>
          <div className="grid gap-1">
            {panelGroup.children?.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === '/' || item.to === '/networks'}
                onClick={() => {
                  closeInstance()
                  closePanel()
                }}
                className={linkClass}
              >
                {item.icon && <item.icon className="size-4 shrink-0" />}
                <span>{t(item.labelKey)}</span>
              </NavLink>
            ))}
            {panelGroup.sections?.map((section) => (
              <MobileSection key={section.labelKey} section={section} onNavigate={closePanel} />
            ))}
          </div>
        </div>
      )}

      <div className="grid grid-cols-5 gap-1">
        {groups.slice(0, 5).map((group) => {
          const Icon = group.icon
          const active = groupRoutes(group).some((to) => isRouteActive(location.pathname, to))
          if (group.to) {
            return (
              <Link
                key={group.key}
                to={group.to}
                onClick={() => {
                  closeInstance()
                  closePanel()
                }}
                className={cn(
                  'relative flex min-w-0 flex-col items-center gap-1 rounded-md px-1.5 py-1.5 text-[11px] transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.97]',
                  active ? 'bg-accent text-primary' : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
                )}
              >
                <Icon className="size-4" />
                <span className="w-full truncate text-center">{t(group.labelKey)}</span>
              </Link>
            )
          }

          return (
            <button
              key={group.key}
              type="button"
              aria-expanded={openKey === group.key}
              onClick={() => togglePanel(group)}
              className={cn(
                'relative flex min-w-0 flex-col items-center gap-1 rounded-md px-1.5 py-1.5 text-[11px] transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.97]',
                active || openKey === group.key
                  ? 'bg-accent text-primary'
                  : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              )}
            >
              <Icon className="size-4" />
              <span className="w-full truncate text-center">{t(group.labelKey)}</span>
            </button>
          )
        })}
      </div>
    </nav>
  )
}

function MobileSection({
  section,
  onNavigate,
}: {
  section: NavSection
  onNavigate: () => void
}) {
  const { t } = useTranslation()
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  return (
    <div className="space-y-1">
      <div className="px-3 pt-2 text-[11px] font-medium text-muted-foreground/70">{t(section.labelKey)}</div>
      {section.children.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          end={item.to === '/' || item.to === '/networks'}
          onClick={() => {
            closeInstance()
            onNavigate()
          }}
          className={({ isActive }) =>
            cn(
              'flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.98]',
              isActive
                ? 'bg-accent font-semibold text-primary shadow-[inset_3px_0_0_var(--primary)]'
                : 'text-foreground/85 hover:bg-accent/60 hover:text-foreground',
            )
          }
        >
          {item.icon && <item.icon className="size-4 shrink-0" />}
          <span>{t(item.labelKey)}</span>
        </NavLink>
      ))}
    </div>
  )
}
