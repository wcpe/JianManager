import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { FileCog } from 'lucide-react'
import {
  useNodeLogAcq,
  useUpdateNodeLogAcq,
  useClearNodeLogAcq,
  type LogAcqSettingItem,
} from '@/api/nodes'
import { validateSettingDraft } from '@/pages/settings-form'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Badge } from '@jianmanager/ui/components/badge'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'

/**
 * 节点日志采集面板（FR-503）：本 Worker 的采集配置覆盖，对照展示平台默认。
 *
 * 三层语义（高 → 低）：**本节点覆盖 > 平台默认 > worker.yml 基线**。
 * 未覆盖的项直接继承平台默认；「恢复默认」删除本节点覆盖键。
 * 保存后经心跳下发（≤1 周期，约 30s）生效，**无需重启 Worker**；离线节点标注「待下发」。
 *
 * 草稿语义：只提交真正改动的键，保存成功后**只清除仍是本次提交值**的键（在途期间的新输入不受影响），
 * 并在存在未提交改动时显示可见提示。
 *
 * **已知残留（review D-02 缺陷②，本波按「最小改动」取舍未拦截）**：草稿只活在组件生命周期内——
 * 切到别的分段（`NodesPage` 的条件渲染）或换节点（`key={selected.id}` 整体重挂）即静默丢弃。
 * 现在至少提示「有未保存改动」让用户看得见；若要真正拦截，应沿用设置页的
 * `hasUnsavedChanges` + 切分类确认范式（会引入跨组件的确认交互，故不在本波范围）。
 *
 * 可复用独立组件（不绑死容器），便于挂在节点详情右栏分段。
 */
interface NodeLogAcqPanelProps {
  nodeId: number
  /** 是否启用查询（分段打开时为 true，避免后台轮询离屏节点）。 */
  active?: boolean
  /** 节点是否在线（离线时标注「待下发」：下次心跳生效）。 */
  online?: boolean
}

/** 布尔型配置项：用 Select 呈现 true/false（packages/ui 暂无 Switch 组件）。 */
const BOOLEAN_KEYS = new Set([
  'log.acq.rules_enabled',
  'log.acq.realtime_tail',
  'log.acq.gz_import_enabled',
])

/** 未改动时该项的展示值（本节点覆盖 > 平台默认）。 */
function originalValue(it: LogAcqSettingItem): string {
  return it.overridden ? it.override : it.platform
}

export default function NodeLogAcqPanel({ nodeId, active = true, online = true }: NodeLogAcqPanelProps) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useNodeLogAcq(nodeId, { enabled: active })
  const update = useUpdateNodeLogAcq(nodeId)
  const clear = useClearNodeLogAcq(nodeId)

  if (isLoading)
    // 骨架占位：标题行 + 若干行轮廓，替代裸文字避免布局跳动。
    return (
      <div className="space-y-3">
        <Skeleton className="h-5 w-44" />
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-2/3" />
      </div>
    )
  if (isError || !data) return <p className="text-sm text-destructive">{t('nodeLogAcq.loadFailed')}</p>

  return (
    <LogAcqForm
      items={data.items}
      saving={update.isPending}
      clearing={clear.isPending}
      online={online}
      onSave={(values) => update.mutateAsync({ values })}
      onClear={(key) => clear.mutateAsync(key)}
    />
  )
}

/** 表单主体：本地草稿（只存改动），展示值回退到「覆盖值 > 平台默认」。 */
function LogAcqForm({
  items,
  saving,
  clearing,
  online,
  onSave,
  onClear,
}: {
  items: LogAcqSettingItem[]
  saving: boolean
  clearing: boolean
  online: boolean
  onSave: (values: Record<string, string>) => Promise<unknown>
  onClear: (key?: string) => Promise<unknown>
}) {
  const { t } = useTranslation()
  // 草稿：仅存用户改动；展示值 = draft[key] ?? 原值（覆盖 > 平台默认）。
  const [draft, setDraft] = useState<Record<string, string>>({})
  const shown = (it: LogAcqSettingItem) => draft[it.key] ?? originalValue(it)

  // 只提交真正改动的键（未改的不提交，避免把「继承平台」固化成节点覆盖）。
  const changed: Record<string, string> = {}
  for (const it of items) {
    const v = draft[it.key]
    if (v !== undefined && v !== originalValue(it)) changed[it.key] = v
  }
  const changedCount = Object.keys(changed).length

  const save = async () => {
    if (changedCount === 0) {
      toast.info(t('nodeLogAcq.noChanges'))
      return
    }
    // 保存前预校验（2026-10-20 浏览器验收发现）：本面板原先**没有**前端校验，只能等
    // PUT 之后由后端拒绝再弹 toast。设置页（settings-form.validateSettingDraft）早已有
    // 同口径校验，两处不一致会让「同一个非法值」在一个入口当场报错、在另一个入口要先
    // 提交一次才失败。这里复用同一个校验函数，保证口径不会各自漂移。
    for (const [key, value] of Object.entries(changed)) {
      const errKey = validateSettingDraft(key, value)
      if (errKey) {
        toast.error(t(errKey))
        return
      }
    }
    // 本次提交的键值快照：清草稿时以它为基准，而不是整表清空（见下方 setDraft）。
    const submitted = { ...changed }
    try {
      await onSave(submitted)
      /**
       * 保存成功后**只清除仍是本次提交值**的键。
       *
       * 为什么不 `setDraft({})`（旧实现）：PUT 在途期间输入控件并不禁用，用户在别处的新输入
       * 会被整表清空一起抹掉，界面当场回退成服务端旧值且没有任何提示——用户以为改动已生效，
       * 实际从未提交。于是这里逐键比对：仍是提交值的键（= 已落库）清掉；在途期间被再次改动的
       * 键保留为未保存草稿（下面「有未保存改动」提示会继续显示）。
       *
       * 为什么不选「saving 期间禁用全部输入」（另一种候选修法）：本面板的 PUT 往返可达秒级，
       * 禁用会把整张表锁成只读，且它并不能消除根因——真正的问题不是「能不能输入」，
       * 而是「清草稿时清多了」。逐键清除既保留可操作性，又对任何时机的输入都安全。
       */
      setDraft((d) => {
        const next = { ...d }
        for (const [key, value] of Object.entries(submitted)) {
          if (next[key] === value) delete next[key]
        }
        return next
      })
      toast.success(t('nodeLogAcq.saved'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodeLogAcq.saveFailed'))
    }
  }

  const restore = async (key: string) => {
    try {
      await onClear(key)
      setDraft((d) => {
        const next = { ...d }
        delete next[key]
        return next
      })
      toast.success(t('nodeLogAcq.restored'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodeLogAcq.restoreFailed'))
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2">
        <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
          <FileCog className="size-4" />
        </span>
        <div>
          <h3 className="text-sm font-semibold">{t('nodeLogAcq.title')}</h3>
          <p className="text-xs text-muted-foreground">{t('nodeLogAcq.desc')}</p>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-xs">
        <span className="text-muted-foreground">{t('nodeLogAcq.scopeHint')}</span>
        {!online && <span className="text-status-warning">{t('nodeLogAcq.pendingPush')}</span>}
      </div>

      <div className="divide-y rounded-md border">
        {items.map((it) => (
          <div key={it.key} className="flex flex-col gap-2 p-3 sm:flex-row sm:items-center sm:gap-4">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium">{t(`settings.keys.${it.key}`, it.key)}</span>
                {it.overridden ? (
                  <Badge variant="outline">{t('nodeLogAcq.overridden')}</Badge>
                ) : (
                  <span className="text-xs text-muted-foreground">{t('nodeLogAcq.inherited')}</span>
                )}
              </div>
              <p className="text-xs text-muted-foreground font-mono">{it.key}</p>
              <p className="text-[11px] text-muted-foreground">
                {t('nodeLogAcq.platformDefault')}:{' '}
                <code className="font-mono">{it.platform || t('nodeLogAcq.emptyValue')}</code>
              </p>
            </div>

            <div className="flex items-center gap-2">
              {BOOLEAN_KEYS.has(it.key) ? (
                <Select value={shown(it)} onValueChange={(v) => setDraft((d) => ({ ...d, [it.key]: v }))}>
                  <SelectTrigger size="sm" className="w-32" data-testid={`node-log-acq-input-${it.key}`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="true">{t('nodeLogAcq.on')}</SelectItem>
                    <SelectItem value="false">{t('nodeLogAcq.off')}</SelectItem>
                  </SelectContent>
                </Select>
              ) : (
                <div className="w-56">
                  <Input
                    data-testid={`node-log-acq-input-${it.key}`}
                    value={shown(it)}
                    onChange={(e) => setDraft((d) => ({ ...d, [it.key]: e.target.value }))}
                    placeholder={
                      it.key === 'log.acq.path_overrides'
                        ? t('nodeLogAcq.pathOverridesPlaceholder')
                        : t('nodeLogAcq.scanIntervalPlaceholder')
                    }
                    className="h-8 font-mono text-xs"
                  />
                </div>
              )}
              <Button
                variant="ghost"
                size="sm"
                disabled={!it.overridden || clearing}
                onClick={() => restore(it.key)}
                title={it.overridden ? t('nodeLogAcq.restoreHint') : t('nodeLogAcq.notOverriddenHint')}
              >
                {t('nodeLogAcq.restore')}
              </Button>
            </div>
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between gap-2">
        <p className="text-[11px] text-muted-foreground">{t('nodeLogAcq.heartbeatHint')}</p>
        <div className="flex items-center gap-2">
          {/* 「有未保存改动」的可见提示：本面板的草稿是暂存式（改完还要点保存才落库），
              没有提示时用户无法区分「已提交」与「只是敲进来了」。判据与提交集同源
              （changed，改回原值即不算改动），避免出现「提示说有改动、保存却报没有改动」。 */}
          {changedCount > 0 && (
            <span
              data-testid="node-log-acq-unsaved-hint"
              className="text-[11px] text-status-warning"
            >
              {t('nodeLogAcq.unsavedHint', { count: changedCount })}
            </span>
          )}
          <Button size="sm" onClick={save} disabled={saving}>
            {saving ? t('common.saving', '保存中…') : t('common.save')}
          </Button>
        </div>
      </div>
    </div>
  )
}
