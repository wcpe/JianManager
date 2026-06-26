import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LayoutGrid, Plus, Save, Trash2 } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/ui/status-badge'
import { instanceStatusLevel } from '@/lib/threshold'
import PromptDialog from '@/components/explorer/PromptDialog'
import { ALL_CARD_TYPES, builtinPresets, type WorkspacePreset } from '@/lib/workspace-preset'
import { cardTypeDef, type CardType } from '@/lib/workspace-card'

/**
 * 可组合工作区工具栏（FR-166）：实例面包屑 + 生命周期操作 + 预设选择 / 另存 + 添加卡片。
 * 原固定 Tab（终端/文件/配置…）在此降级为「快捷预设」下拉的内置项。
 */
interface WorkspaceToolbarProps {
  instanceId: number
  instanceName: string
  status: string
  /** 当前应用的预设 id。 */
  presetId: string
  /** 用户保存的预设。 */
  userPresets: WorkspacePreset[]
  hasInstance: boolean
  startPending: boolean
  stopPending: boolean
  restartPending: boolean
  killPending: boolean
  onStart: () => void
  onStop: () => void
  onRestart: () => void
  onKill: () => void
  onApplyPreset: (presetId: string) => void
  onAddCard: (type: CardType) => void
  onSavePreset: (name: string) => void
  onDeletePreset: (presetId: string) => void
  onClose: () => void
}

/** 内置预设 id → i18n 名称键。 */
function builtinPresetName(id: string, t: (k: string) => string): string {
  return t(`workspace.preset.${id}`)
}

export default function WorkspaceToolbar({
  instanceName,
  status,
  presetId,
  userPresets,
  hasInstance,
  startPending,
  stopPending,
  restartPending,
  killPending,
  onStart,
  onStop,
  onRestart,
  onKill,
  onApplyPreset,
  onAddCard,
  onSavePreset,
  onDeletePreset,
  onClose,
}: WorkspaceToolbarProps) {
  const { t } = useTranslation()
  const [saveOpen, setSaveOpen] = useState(false)
  const isRunning = status === 'RUNNING'
  const builtins = builtinPresets()
  const isBuiltinPreset = builtins.some((p) => p.id === presetId)
  const currentName = isBuiltinPreset
    ? builtinPresetName(presetId, t)
    : (userPresets.find((p) => p.id === presetId)?.name ?? t('workspace.customLayout'))

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
      <div className="flex items-center gap-1.5 text-sm">
        <span className="text-muted-foreground">{t('console.title')}</span>
        <span className="text-muted-foreground">/</span>
        <span className="font-medium">{instanceName}</span>
        {status && <StatusBadge level={instanceStatusLevel(status)} label={status} className="ml-1" />}
      </div>

      {/* 实例生命周期操作 */}
      <div className="flex items-center gap-1.5">
        {isRunning ? (
          <>
            <Button size="sm" variant="outline" disabled={stopPending} onClick={onStop}>
              {t('instances.stop')}
            </Button>
            <Button size="sm" variant="outline" disabled={restartPending} onClick={onRestart}>
              {t('instances.restart')}
            </Button>
            <Button size="sm" variant="outline" disabled={killPending} onClick={onKill}>
              {t('instances.kill')}
            </Button>
          </>
        ) : (
          <Button size="sm" disabled={!hasInstance || startPending} onClick={onStart}>
            {t('instances.start')}
          </Button>
        )}
      </div>

      <div className="ml-auto flex items-center gap-1.5">
        {/* 快捷预设（含原 Tab 降级项 + 用户预设） */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline">
              <LayoutGrid className="size-4" />
              <span className="max-w-[10rem] truncate">{currentName}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[12rem]">
            {builtins.map((p) => (
              <DropdownMenuItem key={p.id} onClick={() => onApplyPreset(p.id)}>
                {builtinPresetName(p.id, t)}
              </DropdownMenuItem>
            ))}
            {userPresets.length > 0 && <DropdownMenuSeparator />}
            {userPresets.map((p) => (
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
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 添加卡片 */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline">
              <Plus className="size-4" />
              {t('workspace.addCard')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[12rem]">
            {ALL_CARD_TYPES.map((def) => (
              <DropdownMenuItem key={def.type} onClick={() => onAddCard(def.type)}>
                <div className="flex flex-col">
                  <span>{t(cardTypeDef(def.type)!.titleKey)}</span>
                  <span className="text-[11px] text-muted-foreground">{t(def.descKey)}</span>
                </div>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        {/* 另存为预设 */}
        <Button size="sm" variant="outline" onClick={() => setSaveOpen(true)} title={t('workspace.savePreset')}>
          <Save className="size-4" />
        </Button>

        <Button size="sm" variant="ghost" title={t('common.close')} onClick={onClose}>
          ✕
        </Button>
      </div>

      <PromptDialog
        open={saveOpen}
        title={t('workspace.savePresetTitle')}
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
