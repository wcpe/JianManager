import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, Clapperboard, LayoutGrid, Plus, Save, SquareTerminal, Trash2 } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import PromptDialog from '@/components/views/explorer/PromptDialog'
import { breadcrumbTrail } from '@jianmanager/ui/lib/breadcrumb'
import type { WorkspacePreset } from '@/lib/workspace-preset'

/** 路由链接渲染插槽（应用侧注入 react-router Link；缺省渲染原生 `<a>`）。 */
export interface RouterLinkArgs {
  to: string
  className?: string
  children: ReactNode
}

export type RenderRouterLink = (args: RouterLinkArgs) => ReactNode

// ── 「添加场景」菜单（FR-168）────────────────────────────────────────

export interface DirectorAddSceneMenuProps {
  /** 可选的用户预设（跨实例 + 单实例共享一份，FR-167）。 */
  userPresets: WorkspacePreset[]
  /** 触发按钮样式：default=轮廓小按钮；primary=空态主操作。 */
  variant?: 'default' | 'primary'
  /** 由预设导入为场景（容器注入 store 动作）。 */
  onAddScene: (preset: WorkspacePreset) => void
}

/**
 * 「添加场景」菜单（FR-168）：从已保存的用户预设（FR-167 超级工作台另存的跨实例布局）导入为导播台场景。
 * 导入即克隆该预设卡片为新场景，追加到缩略图条末尾，并加入状态机（cold，切到才保活）。
 */
export function DirectorAddSceneMenu({ userPresets, variant = 'default', onAddScene }: DirectorAddSceneMenuProps) {
  const { t } = useTranslation()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="sm" variant={variant === 'primary' ? 'default' : 'outline'}>
          <Plus className="size-4" />
          {t('director.addScene')}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[14rem]">
        {userPresets.length === 0 ? (
          <DropdownMenuItem disabled>{t('director.noPresets')}</DropdownMenuItem>
        ) : (
          userPresets.map((p) => (
            <DropdownMenuItem key={p.id} onClick={() => onAddScene(p)}>
              <span className="truncate">{p.name}</span>
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// ── 统一页头/面包屑（FR-134 + FR-162）────────────────────────────────

export interface PageBreadcrumbProps {
  /** 当前路径（容器注入 `useLocation().pathname`）。 */
  pathname: string
  /** 具体末级名称（如打开实例时的实例名）。 */
  leaf?: string
  /** 路由链接渲染插槽。 */
  renderLink?: RenderRouterLink
}

/**
 * 统一页头/面包屑：据当前路由渲染「域 › 页面 [› 末级]」轨迹，
 * 套靛蓝圆角范式（弱化父级、加粗末级、可点节点 hover 高亮）。与 FR-131 五域 IA 对齐。
 *
 * `leaf` 用于补具体末级名称（如打开实例时的实例名）；此时轨迹中的页面节点变为可点回列表。
 * 未知路由（trail 为空）时回退到通用「控制台」标题。
 */
export function PageBreadcrumb({ pathname, leaf, renderLink }: PageBreadcrumbProps) {
  const { t } = useTranslation()
  const trail = breadcrumbTrail(pathname)

  if (trail.length === 0 && !leaf) {
    return <h1 className="min-w-0 truncate text-sm font-semibold">{t('header.console')}</h1>
  }

  // 末级是否为 leaf：有 leaf 则 leaf 是末级、页面节点可点回列表。
  // 路径态详情（如 /instances/:id）breadcrumbTrail 已自带 to；query 态详情（如 /client-channels?channelId=）
  // pathname 无更深段，需按首段合成列表路径，否则「域 › 页面 › 名」里页面节点点不回去。
  const firstSeg = pathname.split('/').filter(Boolean)[0]
  const listPath = firstSeg ? `/${firstSeg}` : '/'
  const items: Array<{ key: string; text: string; to?: string }> = trail.map((c, i) => {
    const isPageNode = i === trail.length - 1
    return {
      key: `${c.labelKey}-${i}`,
      text: t(c.labelKey),
      to: c.to ?? (leaf && isPageNode ? listPath : undefined),
    }
  })
  if (leaf) items.push({ key: 'leaf', text: leaf })

  const lastIdx = items.length - 1

  return (
    <nav aria-label="breadcrumb" className="flex min-w-0 items-center gap-1 text-sm">
      {items.map((it, i) => {
        const isLast = i === lastIdx
        const sep = i > 0 && <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/50" />
        const linkClass = 'shrink-0 truncate text-muted-foreground transition-colors hover:text-foreground'
        const node =
          it.to && !isLast ? (
            renderLink ? (
              renderLink({ to: it.to, className: linkClass, children: it.text })
            ) : (
              <a key={it.key} href={it.to} className={linkClass}>
                {it.text}
              </a>
            )
          ) : (
            <span
              key={it.key}
              className={cn('truncate', isLast ? 'font-semibold text-foreground' : 'shrink-0 text-muted-foreground')}
            >
              {it.text}
            </span>
          )
        return (
          <span key={it.key} className="flex min-w-0 items-center gap-1">
            {sep}
            {node}
          </span>
        )
      })}
    </nav>
  )
}

// ── 超级工作台工具栏（FR-167）────────────────────────────────────────

export interface SuperWorkbenchToolbarProps {
  /** 当前应用的预设 id。 */
  presetId: string
  /** 用户保存的预设（跨实例 + 单实例共享一份）。 */
  userPresets: WorkspacePreset[]
  onApplyPreset: (presetId: string) => void
  onSavePreset: (name: string) => void
  onDeletePreset: (presetId: string) => void
  /** 打开「专注终端」（沉浸终端模式，ADR-087）：取画布上首个终端卡实例。未提供=不渲染按钮。 */
  onOpenFocusTerminals?: () => void
  /** 禁用原因（画布无终端卡等）；给出时按钮禁用并以 title 提示。 */
  focusTerminalsDisabledReason?: string
  /** 路由链接渲染插槽（「进导播台」按钮）。 */
  renderLink?: RenderRouterLink
}

/**
 * 超级工作台工具栏：标题 + 跨实例预设选择/另存。
 * 与单实例工具栏不同：无单实例生命周期操作（跨实例画布无「当前实例」），
 * 卡片来自实例库拖拽；预设携 instanceId（个人级 localStorage）。
 */
export function SuperWorkbenchToolbar({
  presetId,
  userPresets,
  onApplyPreset,
  onSavePreset,
  onDeletePreset,
  onOpenFocusTerminals,
  focusTerminalsDisabledReason,
  renderLink,
}: SuperWorkbenchToolbarProps) {
  const { t } = useTranslation()
  const [saveOpen, setSaveOpen] = useState(false)
  const currentName = userPresets.find((p) => p.id === presetId)?.name ?? t('superWorkbench.customLayout')

  const directorIcon = (
    <>
      <Clapperboard className="size-4" />
      <span className="hidden sm:inline">{t('director.enter')}</span>
    </>
  )

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
      <div className="flex items-center gap-1.5 text-sm">
        <span className="text-muted-foreground">{t('nav.cluster')}</span>
        <span className="text-muted-foreground">/</span>
        <span className="font-medium">{t('superWorkbench.title')}</span>
      </div>

      <div className="ml-auto flex items-center gap-1.5">
        {/* 专注终端（融合入口，职责分层决策）：监看混排留在画布，纯终端重度操作进沉浸台。
            取画布首个终端卡的实例为起点；画布无终端卡时禁用并提示。 */}
        {onOpenFocusTerminals && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={Boolean(focusTerminalsDisabledReason)}
            title={focusTerminalsDisabledReason ?? t('superWorkbench.focusTerminals')}
            onClick={onOpenFocusTerminals}
          >
            <SquareTerminal className="size-4" />
            <span className="hidden sm:inline">{t('superWorkbench.focusTerminals')}</span>
          </Button>
        )}

        {/* 进导播台（FR-168）：把已存的跨实例预设当场景预热瞬切。 */}
        <Button asChild size="sm" variant="outline" title={t('director.enter')}>
          {renderLink ? (
            renderLink({ to: '/director', children: directorIcon })
          ) : (
            <a href="/director">{directorIcon}</a>
          )}
        </Button>

        {/* 跨实例预设选择 */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline">
              <LayoutGrid className="size-4" />
              <span className="max-w-[10rem] truncate">{currentName}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[12rem]">
            {userPresets.length === 0 ? (
              <DropdownMenuItem disabled>{t('superWorkbench.noPresets')}</DropdownMenuItem>
            ) : (
              userPresets.map((p) => (
                <DropdownMenuItem
                  key={p.id}
                  onClick={() => onApplyPreset(p.id)}
                  className="flex items-center justify-between gap-2"
                >
                  <span className="truncate">{p.name}</span>
                  <Trash2
                    className="size-3.5 shrink-0 text-muted-foreground hover:text-destructive"
                    role="button"
                    aria-label={t('common.delete')}
                    onClick={(e) => {
                      e.preventDefault()
                      e.stopPropagation()
                      onDeletePreset(p.id)
                    }}
                  />
                </DropdownMenuItem>
              ))
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setSaveOpen(true)}>
              <Save className="size-4" />
              {t('superWorkbench.savePreset')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 另存为预设 */}
        <Button size="sm" variant="outline" onClick={() => setSaveOpen(true)} title={t('superWorkbench.savePreset')}>
          <Save className="size-4" />
        </Button>
      </div>

      <PromptDialog
        open={saveOpen}
        title={t('superWorkbench.savePresetTitle')}
        validate={(v) => (v.trim() ? '' : t('workspace.presetNameRequired'))}
        onSubmit={(v) => {
          onSavePreset(v.trim())
          setSaveOpen(false)
        }}
        onCancel={() => setSaveOpen(false)}
      />
    </div>
  )
}
