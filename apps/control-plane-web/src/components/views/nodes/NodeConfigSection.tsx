import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowRight, History, Lock, Pencil, Settings2, SlidersHorizontal } from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { Input } from '@jianmanager/ui/components/input'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { capBytesToGiB, capGiBToBytes } from '@/lib/artifacts/artifact-cache'
import { formatFileSize } from '@/lib/shared/format-file-size'
import { formatRelativeTime } from '@/lib/shared/relative-time'
import type { NodeInfo } from '@/lib/nodes/node-types'
import type { DetailTab } from '@/components/views/nodes/NodeDetailPane'

/**
 * 节点配置总览（「配置」分区）。
 *
 * 【为什么是「总览 + 跳转」而不是又一套编辑表单】节点的可写配置分散在已有分区：
 * 代理模式在「代理」、制品缓存上限在「制品缓存」、包管理器在「运行时」、探针版本在「探针版本」，
 * 后端也没有通用的 `PATCH /nodes/:id` —— 每项都是专用端点（PATCH /proxy、PUT /artifact-cache/cap、
 * PUT /pm-config、PUT /probe-version）。在这里再实现一遍编辑表单，等于把同一份写入逻辑复制四份，
 * 且各自的权限/审计/破坏性确认都要重做一遍。
 *
 * 所以这一屏承担的是**「我有哪些配置、现在是什么值、去哪儿改」**：
 * 把散落四处的配置项汇总成一张表（当前值只读），每行给一个直达入口。
 * 这是在「不给后端加接口」前提下，能做到的最有用的配置视图。
 */
/**
 * 可配置项的**当前生效值**（由容器取数后注入）。
 *
 * 【为什么要它】这页叫「配置总览」，原先四行可配置项一律写「见「XX」分区」——
 * 用户想知道「现在代理是什么、缓存上限多少」还得点进各分区自己找，
 * 总览就没起到总览的作用。值取不到时（加载中/离线/接口失败）回退成「见「XX」分区」。
 */
export interface NodeConfigCurrentValues {
  /**
   * 出口代理：`inherit` 继承全局 / `custom` 自定义，附生效地址（已脱敏）。
   * `pending` = 节点离线，改动要等下次心跳上线才生效（代理是心跳下发的）。
   */
  proxy?: { mode: 'inherit' | 'custom'; effectiveUrl: string; globalDefaultUrl: string; pending?: boolean }
  /** 制品缓存上限（字节）；0 表示不限。 */
  cacheCapBytes?: number
  /** 包管理器：npm/pnpm/yarn + 版本 + registry 数量。 */
  pm?: { pm: string; pmVersion: string; registryCount: number }
  /** 探针版本：继承全局时 version 为空，显示「继承全局（x.y.z）」。 */
  probe?: { inherited: boolean; version: string }
}

/** 该节点最近一次配置类变更（来自审计日志，一次查询覆盖整页）。 */
export interface NodeConfigRecentChange {
  action: string
  /** 动作的可读文案（已本地化）。 */
  actionLabel: string
  username: string
  createdAt: string
}

export function NodeConfigSection({
  node,
  onTab,
  currentValues,
  recentChange,
  onSaveCacheCap,
  onSaveProbeVersion,
  onSaveProxy,
  probeOptions,
  probeDefaultVersion,
  savingInline = false,
}: {
  node: NodeInfo
  onTab: (tab: DetailTab) => void
  /** 可配置项的当前生效值（容器注入；缺失则回退为「见「XX」分区」）。 */
  currentValues?: NodeConfigCurrentValues
  /** 最近一次配置类变更（容器注入；无记录时省略）。 */
  recentChange?: NodeConfigRecentChange
  /**
   * 内联快改的三个写入入口（容器注入；复用各分区既有的 mutation，不另写写入逻辑）。
   * 返回 true 表示已保存成功（视图据此收起编辑区）。
   */
  onSaveCacheCap?: (capBytes: number) => Promise<boolean>
  onSaveProbeVersion?: (versionId: number) => Promise<boolean>
  onSaveProxy?: (body: { mode: 'inherit' | 'custom'; url?: string; noProxy?: string }) => Promise<boolean>
  /** 探针候选版本（内联下拉用）；缺省则该项不可内联编辑。 */
  probeOptions?: { id: number; version: string }[]
  /** 全局默认探针版本（展示「继承全局（x.y.z）」）。 */
  probeDefaultVersion?: string
  /** 写入在途（统一禁用保存按钮）。 */
  savingInline?: boolean
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  /*
    内联快改：一次只展开一行（多行同时展开会让这一页变成四张表单，回到当初跳转前的复杂度）。
    draft 存正在编辑的值，取消即丢弃。
  */
  const [editing, setEditing] = useState<string | null>(null)
  const [capDraft, setCapDraft] = useState('')
  const [probeDraft, setProbeDraft] = useState(0)
  const [proxyMode, setProxyMode] = useState<'inherit' | 'custom'>('inherit')
  const [proxyUrl, setProxyUrl] = useState('')
  const [proxyNoProxy, setProxyNoProxy] = useState('')

  /** 当前固定版本号 → 版本 id（编辑器初值；找不到按「继承全局」处理）。 */
  const probeIdOf = (version: string | undefined): number =>
    probeOptions?.find((v) => v.version === version)?.id ?? 0

  const openEditor = (key: string) => {
    setEditing(key)
    if (key === 'cacheCap') setCapDraft(capBytesToGiB(currentValues?.cacheCapBytes ?? 0))
    if (key === 'probe') setProbeDraft(currentValues?.probe?.inherited ? 0 : probeIdOf(currentValues?.probe?.version))
    if (key === 'proxy') {
      setProxyMode(currentValues?.proxy?.mode ?? 'inherit')
      setProxyUrl(currentValues?.proxy?.mode === 'custom' ? currentValues.proxy.effectiveUrl : '')
      setProxyNoProxy('')
    }
  }
  const closeEditor = () => setEditing(null)

  /**
   * 配置项清单：value 为当前生效值的只读快照，action 指向真正可修改它的分区。
   * 「只读」标记用于表达「后端无写接口」的项（如端口），避免用户找编辑入口找不到。
   */
  const items: Array<{
    key: string
    label: string
    value: React.ReactNode
    hint: string
    target?: DetailTab
    editable: boolean
    /** 有值表示「改动尚未生效」，显示为徽标。 */
    pending?: string
  }> = [
    {
      key: 'proxy',
      label: t('nodes.config.proxy', '出口代理'),
      value: currentValues?.proxy
        ? currentValues.proxy.mode === 'inherit'
          ? t('nodes.config.proxyInherit', {
              url: currentValues.proxy.globalDefaultUrl || t('nodeProxy.effectiveDirect'),
            })
          : t('nodes.config.proxyCustom', { url: currentValues.proxy.effectiveUrl || '--' })
        : t('nodes.config.seeProxyTab', '见「代理」分区'),
      /*
        代理是**心跳下发**的：节点离线时改了也不会立即生效。
        总览页必须标出来，否则用户以为已经改好了（要等节点上线才发现没生效）。
      */
      pending: currentValues?.proxy?.pending ? t('nodes.config.pendingPush') : undefined,
      hint: t('nodes.config.proxyHint', '节点出站请求走全局继承还是自定义代理'),
      target: 'proxy',
      editable: true,
    },
    {
      key: 'cacheCap',
      label: t('nodes.config.cacheCap', '制品缓存上限'),
      value:
        currentValues?.cacheCapBytes != null
          ? currentValues.cacheCapBytes > 0
            ? formatFileSize(currentValues.cacheCapBytes)
            : t('nodes.config.cacheUnlimited')
          : t('nodes.config.seeCacheTab', '见「制品缓存」分区'),
      hint: t('nodes.config.cacheCapHint', '超出上限会触发 LRU 淘汰（破坏性操作，需确认）'),
      target: 'cache',
      editable: true,
    },
    {
      key: 'pm',
      label: t('nodes.config.pm', '包管理器'),
      value: currentValues?.pm
        ? t('nodes.config.pmValue', {
            pm: currentValues.pm.pm,
            version: currentValues.pm.pmVersion || '--',
            count: currentValues.pm.registryCount,
          })
        : t('nodes.config.seeRuntimeTab', '见「运行时」分区'),
      hint: t('nodes.config.pmHint', 'corepack 激活与 registry 配置'),
      target: 'runtime',
      editable: true,
    },
    {
      key: 'probe',
      label: t('nodes.config.probe', '探针版本'),
      value: currentValues?.probe
        ? currentValues.probe.inherited
          ? t('nodes.config.probeInherit', { version: currentValues.probe.version || '--' })
          : t('nodes.config.probePinned', { version: currentValues.probe.version })
        : t('nodes.config.seeProbeTab', '见「探针版本」分区'),
      hint: t('nodes.config.probeHint', '只影响之后创建的新实例，不会主动升级存量'),
      target: 'probe',
      editable: true,
    },
    {
      key: 'grpcPort',
      label: t('nodes.grpcPort'),
      value: node.grpcPort > 0 ? node.grpcPort : '--',
      hint: t('nodes.config.grpcPortHint', '参与节点接入，修改需重新 enroll（无在线编辑接口）'),
      editable: false,
    },
    {
      key: 'wsPort',
      label: t('nodes.wsPort'),
      value: node.wsPort > 0 ? node.wsPort : '--',
      hint: t('nodes.config.wsPortHint', '参与节点接入，修改需重新 enroll（无在线编辑接口）'),
      editable: false,
    },
    {
      key: 'autoStart',
      label: t('nodes.config.schedulable', '接受新实例调度'),
      value: node.maintenance ? t('nodes.unschedulable', '不可调度') : t('nodes.schedulable', '可调度'),
      hint: t('nodes.config.schedulableHint', '通过「进入/退出维护」切换（顶部「⋯」菜单）'),
      editable: false,
    },
    {
      key: 'health',
      label: t('nodes.config.heartbeat', '心跳上报'),
      value: online
        ? t('nodes.config.heartbeatOn', '正常')
        : t('nodes.config.heartbeatOff', '离线'),
      hint: t('nodes.config.heartbeatHint', '由节点上的 Worker 主动上报，平台侧无法强制开启'),
      editable: false,
    },
  ]

  /** 哪几项支持内联快改（包管理器字段最多，仍走分区页）。 */
  const inlineEditable = (key: string): boolean =>
    (key === 'cacheCap' && !!onSaveCacheCap) ||
    (key === 'probe' && !!onSaveProbeVersion && !!probeOptions?.length) ||
    (key === 'proxy' && !!onSaveProxy)

  /**
   * 内联编辑区：一次只展开一行。
   *
   * 【为什么只做「快改」】缓存上限与探针版本是单字段，就地改完即走；
   * 代理是三字段但语义简单（模式 + 地址 + 免代理），折叠展开也放得下。
   * 包管理器涉及 PM 选择与多条 registry，字段太多，仍保留「去配置」跳转——
   * 把它也塞进这一页，总览就退化成了四张表单。
   */
  const renderEditor = (key: string) => {
    const cancel = (
      <Button type="button" variant="ghost" size="sm" onClick={closeEditor}>
        {t('common.cancel')}
      </Button>
    )
    if (key === 'cacheCap') {
      return (
        <div className="mt-2 flex flex-wrap items-end gap-2 rounded-md border bg-muted/30 px-2.5 py-2" data-testid="node-config-editor-cacheCap">
          <label className="flex flex-col gap-1 text-[11px] text-muted-foreground">
            {t('artifactCache.capInputLabel')}
            <Input
              value={capDraft}
              onChange={(e) => setCapDraft(e.target.value)}
              inputMode="decimal"
              placeholder={t('artifactCache.capUnlimited')}
              className="h-7 w-28 text-sm"
            />
          </label>
          <Button
            size="sm"
            disabled={savingInline}
            onClick={async () => {
              const ok = await onSaveCacheCap!(capGiBToBytes(capDraft))
              if (ok) closeEditor()
            }}
          >
            {t('common.save')}
          </Button>
          {cancel}
        </div>
      )
    }
    if (key === 'probe') {
      return (
        <div className="mt-2 flex flex-wrap items-end gap-2 rounded-md border bg-muted/30 px-2.5 py-2" data-testid="node-config-editor-probe">
          <label className="flex flex-col gap-1 text-[11px] text-muted-foreground">
            {t('probe.nodeVersionTitle')}
            <NativeSelect
              value={String(probeDraft)}
              onChange={(e) => setProbeDraft(Number(e.target.value))}
              className="h-7 w-56 text-sm"
            >
              <option value="0">{t('probe.nodeInherit', { version: probeDefaultVersion || '—' })}</option>
              {(probeOptions ?? []).map((v) => (
                <option key={v.id} value={v.id}>
                  {v.version}
                </option>
              ))}
            </NativeSelect>
          </label>
          <Button
            size="sm"
            disabled={savingInline}
            onClick={async () => {
              const ok = await onSaveProbeVersion!(probeDraft)
              if (ok) closeEditor()
            }}
          >
            {t('common.save')}
          </Button>
          {cancel}
        </div>
      )
    }
    if (key === 'proxy') {
      return (
        <div className="mt-2 space-y-2 rounded-md border bg-muted/30 px-2.5 py-2" data-testid="node-config-editor-proxy">
          <Segments aria-label={t('nodeProxy.mode')}>
            {(['inherit', 'custom'] as const).map((m) => (
              <Segment key={m} active={proxyMode === m} onClick={() => setProxyMode(m)}>
                {t(`nodeProxy.${m}`)}
              </Segment>
            ))}
          </Segments>
          {proxyMode === 'custom' && (
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1 text-[11px] text-muted-foreground">
                {t('nodeProxy.url')}
                <Input
                  value={proxyUrl}
                  onChange={(e) => setProxyUrl(e.target.value)}
                  placeholder="http://host:port"
                  className="h-7 w-64 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1 text-[11px] text-muted-foreground">
                {t('nodeProxy.noProxy')}
                <Input
                  value={proxyNoProxy}
                  onChange={(e) => setProxyNoProxy(e.target.value)}
                  placeholder="localhost,127.0.0.1"
                  className="h-7 w-56 font-mono text-xs"
                />
              </label>
            </div>
          )}
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              disabled={savingInline}
              onClick={async () => {
                const ok = await onSaveProxy!(
                  proxyMode === 'custom'
                    ? { mode: 'custom', url: proxyUrl, noProxy: proxyNoProxy }
                    : { mode: 'inherit' },
                )
                if (ok) closeEditor()
              }}
            >
              {t('common.save')}
            </Button>
            {cancel}
          </div>
        </div>
      )
    }
    return null
  }

  return (
    <div className="space-y-3">
      <Panel
        title={
          <span className="flex items-center gap-1.5">
            <Settings2 className="size-3.5" aria-hidden />
            {t('nodes.config.title', '节点配置')}
          </span>
        }
      >
        <p className="mb-3 text-xs text-muted-foreground">
          {t(
            'nodes.config.intro',
            '可写配置各自有专用入口与权限/审计要求，这里汇总当前状态并提供直达链接；标「只读」的项平台侧没有在线修改接口。',
          )}
        </p>
        {/*
          按「能不能改」分成两组：原先 8 行拍平，可写项与只读项交错，
          用户得逐行看徽标才知道哪几项点了能改。分组后先看到「有入口的 4 项」。
        */}
        {(['editable', 'readonly'] as const).map((group) => {
          const rows = items.filter((i) => i.editable === (group === 'editable'))
          if (rows.length === 0) return null
          return (
            <section key={group} className="mb-1 last:mb-0">
              <div className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground">
                {group === 'editable' ? (
                  <SlidersHorizontal className="size-3.5 text-primary/70" aria-hidden />
                ) : (
                  <Lock className="size-3.5 text-muted-foreground/60" aria-hidden />
                )}
                {group === 'editable'
                  ? t('nodes.config.groupEditable')
                  : t('nodes.config.groupReadonly')}
                <span className="tabular-nums font-normal text-muted-foreground/70">{rows.length}</span>
              </div>
              <ul className="divide-y rounded-md border">
                {rows.map((item) => (
                  <li
                    key={item.key}
                    className="px-2.5 py-2 transition-colors hover:bg-muted/40"
                    data-testid={`node-config-${item.key}`}
                  >
                    <div className="flex items-start gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="text-xs font-semibold text-foreground">{item.label}</span>
                        {!item.editable && (
                          <Badge variant="outline" className="text-[10px] text-muted-foreground">
                            {t('nodes.config.readOnly', '只读')}
                          </Badge>
                        )}
                      </div>
                      <p className="mt-0.5 text-[11px] text-muted-foreground">{item.hint}</p>
                    </div>
                    <div className="flex shrink-0 items-center gap-2">
                      {item.pending && (
                        <Badge
                          variant="outline"
                          className="border-status-warning/50 text-[10px] text-status-warning"
                          title={t('nodes.config.pendingPushHint')}
                        >
                          {item.pending}
                        </Badge>
                      )}
                      <span className="text-xs font-medium tabular-nums text-foreground/80">{item.value}</span>
                      {inlineEditable(item.key) && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={t('common.edit')}
                          title={t('nodes.config.inlineEditHint')}
                          onClick={() => (editing === item.key ? closeEditor() : openEditor(item.key))}
                          data-testid={`node-config-inline-${item.key}`}
                        >
                          <Pencil className="size-3.5" aria-hidden />
                        </Button>
                      )}
                      {item.target && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          className="h-6 gap-1 px-2 text-[11px]"
                          onClick={() => onTab(item.target!)}
                        >
                          {t('nodes.config.goEdit', '去配置')}
                          <ArrowRight className="size-3" aria-hidden />
                        </Button>
                      )}
                    </div>
                    </div>
                    {editing === item.key && renderEditor(item.key)}
                  </li>
                ))}
              </ul>
            </section>
          )
        })}

        {/*
          最近一次配置类变更（来自审计日志）：一次查询覆盖整页。
          「谁在何时改的」是排障第一问——尤其出问题时要确认「是不是刚有人动过」。
        */}
        {recentChange && (
          <div className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 border-t pt-2 text-[11px] text-muted-foreground">
            <History className="size-3.5 shrink-0 text-muted-foreground/70" aria-hidden />
            <span>{t('nodes.config.recentChange')}</span>
            <span className="font-medium text-foreground/80">{recentChange.actionLabel || recentChange.action}</span>
            <span aria-hidden>·</span>
            <span className="font-mono">{recentChange.username || '--'}</span>
            <span aria-hidden>·</span>
            <span>{formatRelativeTime(recentChange.createdAt) || '--'}</span>
          </div>
        )}
      </Panel>
    </div>
  )
}
