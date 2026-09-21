import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ExternalLink, FileText, Save } from 'lucide-react'
import { useConfigSurface, useUpdateConfigSurface, type ConfigSourceKind, type ConfigSurfaceItem } from '@/api/configSurface'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { cn } from '@jianmanager/ui'

/**
 * 实例「关键配置」面板（FR-451 / ADR-092）。
 *
 * 把启动参数 + `server.properties` 关键项明面化：每项显式声明来源——
 * **内联值**（平台持有、可编辑）或**文件引用**（外部文件提供、平台不覆写，只读展示生效值预览）。
 * 两态互斥，杜绝「平台改了却被文件覆盖」的静默冲突。`server.properties` 与启动命令均**下次启动生效**。
 */

interface DraftState {
  source: ConfigSourceKind
  inlineValue: string
  filePath: string
  /** 用户是否显式编辑过内联值：用于区分「文件预览不可用时的空占位」与「用户主动清空」。 */
  inlineEdited?: boolean
}

/** 从清单构造草稿（保存前不改真源）。 */
function buildDrafts(items: ConfigSurfaceItem[]): Record<string, DraftState> {
  const out: Record<string, DraftState> = {}
  for (const it of items) {
    out[it.itemKey] = {
      source: it.source,
      inlineValue: it.inlineValue ?? (it.effectiveSource === 'inline' ? it.effectiveValue : ''),
      filePath: it.filePath ?? 'server.properties',
    }
  }
  return out
}

/** 草稿与原始清单的内容指纹（变则重挂编辑器，避免 effect 内 setState）。 */
function draftsKey(items: ConfigSurfaceItem[] | undefined): string {
  if (!items) return 'loading'
  return JSON.stringify(items.map((i) => [i.itemKey, i.source, i.inlineValue, i.effectiveValue, i.filePath, i.fileKey]))
}

export default function InstanceConfigSurfacePanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: items, isLoading } = useConfigSurface(instanceId)
  const key = useMemo(() => draftsKey(items), [items])

  if (isLoading) {
    return <p className="p-4 text-xs text-muted-foreground">{t('common.loading')}</p>
  }
  if (!items || items.length === 0) {
    return <p className="p-4 text-xs text-muted-foreground">{t('configSurface.empty')}</p>
  }
  // 内容变即重挂编辑器，草稿从最新清单重建（与 InstanceEnvSegment 同手法）。
  return <SurfaceEditor key={key} instanceId={instanceId} items={items} />
}

/** 文件引用的生效值是否可用（读取失败/为空时不可用作内联初值）。 */
function filePreviewUsable(item: ConfigSurfaceItem): boolean {
  return item.effectiveSource === 'file' && !item.previewError && item.effectiveValue !== ''
}

function SurfaceEditor({ instanceId, items }: { instanceId: number; items: ConfigSurfaceItem[] }) {
  const { t } = useTranslation()
  const update = useUpdateConfigSurface(instanceId)
  const [drafts, setDrafts] = useState<Record<string, DraftState>>(() => buildDrafts(items))

  const setDraft = (itemKey: string, patch: Partial<DraftState>) =>
    setDrafts((d) => ({ ...d, [itemKey]: { ...d[itemKey], ...patch } }))

  // 已改动项（来源或内联值变化）。
  const changed = items.filter((it) => {
    const d = drafts[it.itemKey]
    if (!d) return false
    if (d.source !== it.source) return true
    if (d.source === 'inline') return d.inlineValue !== (it.inlineValue ?? '')
    return false
  })

  const save = () => {
    update.mutate(
      changed.map((it) => {
        const d = drafts[it.itemKey]
        if (d.source !== 'inline') {
          return { itemKey: it.itemKey, source: 'file' as const, filePath: d.filePath, fileKey: it.fileKey }
        }
        // 文件现值不可用（读取失败/为空）且用户未显式编辑时，不发送 inlineValue，
        // 交后端 file→inline 迁移读取文件现值兜底，避免把文件现值清成空串（破坏性写入）。
        if (it.source === 'file' && !filePreviewUsable(it) && !d.inlineEdited) {
          return { itemKey: it.itemKey, source: 'inline' as const }
        }
        return { itemKey: it.itemKey, source: 'inline' as const, inlineValue: d.inlineValue }
      }),
    )
  }

  // 分组渲染（后端提供中文 group/description，作为元数据直接展示）。
  const groups = useMemo(() => {
    const order: string[] = []
    const map = new Map<string, ConfigSurfaceItem[]>()
    for (const it of items) {
      const g = it.group || t('configSurface.otherGroup')
      if (!map.has(g)) {
        map.set(g, [])
        order.push(g)
      }
      map.get(g)!.push(it)
    }
    return order.map((g) => ({ group: g, items: map.get(g)! }))
  }, [items, t])

  return (
    <div className="space-y-3 p-3" data-testid="config-surface-panel">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">{t('configSurface.hint')}</p>
        <Button size="sm" disabled={changed.length === 0 || update.isPending} onClick={save} data-testid="config-surface-save">
          <Save className="mr-1 size-3.5" />
          {update.isPending ? t('common.saving') : t('common.save')}
          {changed.length > 0 ? ` (${changed.length})` : ''}
        </Button>
      </div>

      {groups.map(({ group, items: groupItems }) => (
        <section key={group} className="rounded-lg border bg-card">
          <h3 className="border-b px-3 py-2 text-xs font-semibold text-muted-foreground">{group}</h3>
          <div className="divide-y">
            {groupItems.map((it) => (
              <SurfaceRow
                key={it.itemKey}
                instanceId={instanceId}
                item={it}
                draft={drafts[it.itemKey]}
                onChange={(patch) => setDraft(it.itemKey, patch)}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  )
}

/** 启动项不支持「引用文件」来源（后端同样拒绝）：启动命令须由平台持有才能对下次启动生效。 */
function isStartupItem(itemKey: string): boolean {
  return itemKey.startsWith('startup.')
}

function SurfaceRow({
  instanceId,
  item,
  draft,
  onChange,
}: {
  instanceId: number
  item: ConfigSurfaceItem
  draft: DraftState
  onChange: (patch: Partial<DraftState>) => void
}) {
  const { t } = useTranslation()
  const startup = isStartupItem(item.itemKey)
  const isInline = startup || draft.source === 'inline'
  const choices = item.choices ?? []

  return (
    <div className="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center" data-testid={`config-surface-row-${item.itemKey}`}>
      <div className="min-w-0 sm:w-64 sm:shrink-0">
        <p className="truncate text-sm font-medium">{item.description || item.itemKey}</p>
        <p className="truncate font-mono text-[11px] text-muted-foreground">{item.itemKey}</p>
      </div>

      {/* 来源二态切换；启动项仅有内联态，不提供文件引用（后端拒绝，见 config_source.go 注）。 */}
      {!startup && (
        <div className="inline-flex shrink-0 rounded-full bg-muted p-0.5" role="group" aria-label={t('configSurface.sourceLabel')}>
          <button
            type="button"
            aria-pressed={isInline}
            onClick={() =>
              onChange({
                source: 'inline',
                // file→inline 迁移：以文件当前生效值预填草稿，避免提交空值把文件现值清掉。
                // 文件现值不可用（previewError/空）时不预填，并以 inlineEdited=false 标记
                // 「非用户编辑」，保存时省略 inlineValue 交后端迁移兜底（见 save）。
                ...(item.effectiveSource === 'file' && filePreviewUsable(item)
                  ? { inlineValue: item.effectiveValue }
                  : {}),
                inlineEdited: false,
              })
            }
            className={cn(
              'rounded-full px-2.5 py-1 text-[11px] transition-colors',
              isInline ? 'bg-card font-semibold text-foreground shadow-soft' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {t('configSurface.sourceInline')}
          </button>
          <button
            type="button"
            aria-pressed={!isInline}
            onClick={() => onChange({ source: 'file' })}
            className={cn(
              'rounded-full px-2.5 py-1 text-[11px] transition-colors',
              !isInline ? 'bg-card font-semibold text-foreground shadow-soft' : 'text-muted-foreground hover:text-foreground',
            )}
          >
            {t('configSurface.sourceFile')}
          </button>
        </div>
      )}

      {/* 来源对应的编辑区 / 只读预览。 */}
      <div className="min-w-0 flex-1">
        {isInline ? (
          choices.length > 0 ? (
            <select
              aria-label={item.description || item.itemKey}
              className="h-8 w-full rounded-md border bg-background px-2 font-mono text-xs"
              value={draft.inlineValue}
              onChange={(e) => onChange({ inlineValue: e.target.value, inlineEdited: true })}
            >
              <option value="">—</option>
              {choices.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          ) : (
            <Input
              aria-label={item.description || item.itemKey}
              className="h-8 font-mono text-xs"
              value={draft.inlineValue}
              onChange={(e) => onChange({ inlineValue: e.target.value, inlineEdited: true })}
            />
          )
        ) : (
          <div className="flex flex-col gap-1 sm:flex-row sm:items-center">
            <Input
              aria-label={t('configSurface.filePath')}
              className="h-8 font-mono text-xs sm:max-w-56"
              value={draft.filePath}
              onChange={(e) => onChange({ filePath: e.target.value })}
              placeholder="server.properties"
            />
            <div className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
              <FileText className="size-3.5 shrink-0" />
              {item.source === 'file' && !item.previewError ? (
                <span className="truncate">
                  {t('configSurface.effectiveValue')}: <span className="font-mono text-foreground">{item.effectiveValue || '—'}</span>
                </span>
              ) : (
                <span>{t('configSurface.previewPending')}</span>
              )}
            </div>
            <Link
              to={`/instances/${instanceId}/files?path=${encodeURIComponent(draft.filePath)}`}
              className="inline-flex shrink-0 items-center gap-1 text-[11px] text-primary underline-offset-2 hover:underline"
            >
              <ExternalLink className="size-3" />
              {t('configSurface.openFile')}
            </Link>
          </div>
        )}
        {item.previewError && (
          <p className="mt-1 text-[11px] text-status-warning">{t('configSurface.previewError', { message: item.previewError })}</p>
        )}
      </div>
    </div>
  )
}
