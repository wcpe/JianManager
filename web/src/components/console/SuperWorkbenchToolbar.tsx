import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { Clapperboard, LayoutGrid, Save, Trash2 } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
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
}

export default function SuperWorkbenchToolbar({
  presetId,
  userPresets,
  onApplyPreset,
  onSavePreset,
  onDeletePreset,
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
