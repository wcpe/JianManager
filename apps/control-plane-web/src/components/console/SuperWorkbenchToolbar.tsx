import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { Clapperboard, LayoutGrid, Save, SquareTerminal, Trash2 } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@jianmanager/ui/components/dropdown-menu'
import { Button } from '@jianmanager/ui/components/button'
import PromptDialog from '@/components/explorer/PromptDialog'
import type { WorkspacePreset } from '@/lib/workspace-preset'

/**
 * 超级工作台工具栏（FR-167）：标题 + 跨实例预设选择/另存。
 * 与单实例工具栏不同：无单实例生命周期操作（跨实例画布无「当前实例」），
 * 卡片来自实例库拖拽；预设携 instanceId（个人级 localStorage）。
 */
interface SuperWorkbenchToolbarProps {
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
}

export default function SuperWorkbenchToolbar({
  presetId,
  userPresets,
  onApplyPreset,
  onSavePreset,
  onDeletePreset,
  onOpenFocusTerminals,
  focusTerminalsDisabledReason,
}: SuperWorkbenchToolbarProps) {
  const { t } = useTranslation()
  const [saveOpen, setSaveOpen] = useState(false)
  const currentName = userPresets.find((p) => p.id === presetId)?.name ?? t('superWorkbench.customLayout')

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
          <Link to="/director">
            <Clapperboard className="size-4" />
            <span className="hidden sm:inline">{t('director.enter')}</span>
          </Link>
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
