import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LayoutGrid, Play, Plus, RotateCw, Save, Square, Trash2, X, Zap } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/ui/status-badge'
import { toneChipClass } from '@/lib/tone'
import { cn } from '@/lib/utils'
import PromptDialog from '@/components/explorer/PromptDialog'
import { ALL_CARD_TYPES, builtinPresets, type WorkspacePreset } from '@/lib/workspace-preset'
import { cardTypeDef, type CardType } from '@/lib/workspace-card'
import {
  headerIconTone,
  headerStatusLevel,
  isRunning,
  isTransitioning,
  metaLine,
  roleMeta,
} from './workspace-header'

/**
 * 实例工作区页眉（FR-180，重设计 FR-166 工具栏，增强 FR-069）。
 *
 * 左 = **身份块**：角色色块图标 + 实例名 + 状态徽标（过渡态脉冲）+ 角色徽标 + `类型·节点:端口` 元信息行，
 * 把「这是谁、什么角色、现在什么状态、在哪台机器」一眼讲清，与全局顶栏（FR-162/179）同色系同圆角。
 * 中 = **关键操作**：生命周期按钮分组明示（运行→停止/重启/强制终止，否则→启动），图标+文案高可发现。
 * 右 = 画布控件：快捷预设（原固定 Tab 降级项 + 用户预设）/ 添加卡片 / 另存预设 / 关闭工作区。
 */
interface WorkspaceToolbarProps {
  instanceId: number
  instanceName: string
  status: string
  /** 群组服角色（FR-032）：proxy / backend / universal，用于身份图标与角色徽标。 */
  role: string
  /** 实例类型（如 PaperMC / Velocity），元信息行展示。 */
  type: string
  /** 所属节点名（由画布解析后传入，避免页眉自查节点表）。 */
  nodeName: string
  /** 系统分配的监听端口（FR-032），>0 时拼到节点后。 */
  serverPort: number
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
  role,
  type,
  nodeName,
  serverPort,
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
  const running = isRunning(status)
  const statusLabel = t(`instances.${status.toLowerCase()}`, status)
  const meta = roleMeta(role)
  const RoleIcon = meta.icon
  const builtins = builtinPresets()
  const isBuiltinPreset = builtins.some((p) => p.id === presetId)
  const currentName = isBuiltinPreset
    ? builtinPresetName(presetId, t)
    : (userPresets.find((p) => p.id === presetId)?.name ?? t('workspace.customLayout'))

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b bg-card/40 px-3 py-2 sm:px-4">
      {/* 身份块：角色色块图标 + 名称 + 状态/角色徽标 + 类型·节点:端口 */}
      <div className="flex min-w-0 items-center gap-2.5">
        <span
          className={cn(
            'flex size-9 shrink-0 items-center justify-center rounded-xl',
            toneChipClass(headerIconTone(status)),
          )}
          aria-hidden
        >
          <RoleIcon className="size-[18px]" />
        </span>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-1.5">
            <span className="truncate text-sm font-semibold" title={instanceName}>
              {instanceName}
            </span>
            <StatusBadge
              level={headerStatusLevel(status)}
              label={statusLabel}
              pulse={isTransitioning(status)}
            />
            {meta.kind !== 'universal' && (
              <Badge variant="outline" className={cn('hidden sm:inline-flex', meta.badgeClass)}>
                {t(meta.labelKey)}
              </Badge>
            )}
          </div>
          <p
            className="mt-0.5 truncate text-xs text-muted-foreground"
            title={metaLine(type, nodeName, serverPort)}
          >
            {metaLine(type, nodeName, serverPort)}
          </p>
        </div>
      </div>

      {/* 关键操作：生命周期分组明示（图标 + 文案，高可发现） */}
      <div className="flex items-center gap-1.5">
        {running ? (
          <>
            <Button size="sm" variant="outline" disabled={stopPending} onClick={onStop}>
              <Square className="size-3.5 text-status-warning" />
              {t('instances.stop')}
            </Button>
            <Button size="sm" variant="outline" disabled={restartPending} onClick={onRestart}>
              <RotateCw className="size-3.5 text-status-info" />
              {t('instances.restart')}
            </Button>
            <Button size="sm" variant="outline" disabled={killPending} onClick={onKill}>
              <Zap className="size-3.5 text-status-danger" />
              {t('instances.kill')}
            </Button>
          </>
        ) : (
          <Button size="sm" disabled={!hasInstance || startPending} onClick={onStart}>
            <Play className="size-3.5" />
            {t('instances.start')}
          </Button>
        )}
      </div>

      {/* 画布控件：预设 / 添加卡片 / 另存 / 关闭 */}
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
        <Button
          size="sm"
          variant="outline"
          onClick={() => setSaveOpen(true)}
          aria-label={t('workspace.savePreset')}
          title={t('workspace.savePreset')}
        >
          <Save className="size-4" />
        </Button>

        <Button
          size="sm"
          variant="ghost"
          aria-label={t('common.close')}
          title={t('common.close')}
          onClick={onClose}
        >
          <X className="size-4" />
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
