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
import { Button } from '@jianmanager/ui/components/button'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { SettingsFormView } from '@/components/views/settings/SettingsFormView'
import {
  clearSubmitted,
  defaultSerializeKv,
  emptyDraft,
  type SettingItem as DraftSettingItem,
  type SettingsDraft,
} from '@/lib/settings/settings-draft'

/**
 * 节点日志采集面板：本 Worker 的采集配置覆盖，对照展示平台默认。
 *
 * 三层语义（高 → 低）：**本节点覆盖 > 平台默认 > worker.yml 基线**。
 * 未覆盖的项直接继承平台默认；「恢复默认」删除本节点覆盖键。
 * 保存后经心跳下发（≤1 周期，约 30s）生效，**无需重启 Worker**；离线节点标注「待下发」。
 *
 * 【为什么改用通用表单组件 SettingsFormView】
 * 本面板原先自己写了一套渲染（布尔用 Select、其余用 Input，另加独立保存按钮），
 * 与设置页的 EditableRow 是两份实现。两份的后果已经发生过一次：本面板长期没有
 * 前端校验，同一个非法值在设置页当场报错、在这里却要提交一次才失败。
 * 现在字段类型由 `lib/settings/field-types` 的声明表分派、校验只走
 * `validateSettingDraft`，两个入口天然同口径。
 *
 * 顺带的收益：`log.acq.path_overrides` 从「手写 JSON 文本框」变成成对行编辑器
 * （实例 + 路径，可增删、逐行即时校验）——那正是「不要给输入框让我输 JSON」那条诉求。
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

/**
 * 采集项 → 通用表单的草稿模型条目。
 *
 * `platform` 放平台默认值（而非空串）：草稿模型用「草稿 vs 原值」判脏，
 * 原值必须是用户眼里那一列的当前值，否则「没动过」会被算成「改动」。
 */
function toDraftItem(it: LogAcqSettingItem): DraftSettingItem {
  return {
    key: it.key,
    platform: it.platform,
    override: it.override,
    overridden: it.overridden,
  }
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

/** 表单主体：草稿由通用组件持有并上报，本层负责提交与「恢复默认」。 */
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
  const [draft, setDraft] = useState<SettingsDraft>(emptyDraft())
  const draftItems = items.map(toDraftItem)

  const restore = async (key: string) => {
    try {
      await onClear(key)
      setDraft((d) => {
        const next: SettingsDraft = { text: { ...d.text }, kv: { ...d.kv } }
        delete next.text[key]
        delete next.kv[key]
        return next
      })
      toast.success(t('nodeLogAcq.restored'))
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodeLogAcq.restoreFailed'))
    }
  }

  /*
    保存成功后**只清除仍是本次提交值**的键（`clearSubmitted` 的职责）。
    不能整表清空：PUT 在途期间输入控件并不禁用，用户在别处的新输入会被一起抹掉，
    界面当场回退成服务端旧值且没有任何提示——用户以为改动已生效，实际从未提交。
  */
  const submit = async (values: Record<string, string>) => {
    try {
      await onSave(values)
      setDraft((d) => clearSubmitted(d, values, draftItems, defaultSerializeKv))
      toast.success(t('nodeLogAcq.saved'))
      return true
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodeLogAcq.saveFailed'))
      return false
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

      <SettingsFormView
        items={draftItems}
        draft={draft}
        onDraftChange={setDraft}
        saving={saving}
        onSave={submit}
        labelOf={(key) => t(`settings.keys.${key}`, key)}
        actionOf={(key) => {
          const it = items.find((x) => x.key === key)
          if (!it) return null
          return (
            <Button
              variant="ghost"
              size="sm"
              disabled={!it.overridden || clearing}
              onClick={() => void restore(key)}
              title={it.overridden ? t('nodeLogAcq.restoreHint') : t('nodeLogAcq.notOverriddenHint')}
            >
              {t('nodeLogAcq.restore')}
            </Button>
          )
        }}
      />

      <p className="text-[11px] text-muted-foreground">{t('nodeLogAcq.heartbeatHint')}</p>
    </div>
  )
}
