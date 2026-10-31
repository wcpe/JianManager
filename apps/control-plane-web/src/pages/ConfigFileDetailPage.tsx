import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { AlertTriangle, ArrowLeft, GitCompareArrows, History, Layers, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@jianmanager/ui/components/alert'
import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { ListSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Panel } from '@jianmanager/ui/components/panel'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Textarea } from '@jianmanager/ui/components/textarea'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import {
  useConfigFile,
  useConfigFileEffective,
  useConfigFileLayerHistory,
  useConfigFileLayers,
  useDeleteConfigFileLayer,
  useRollbackConfigFileLayer,
  useSaveConfigFileLayer,
} from '@/api/configFiles'
import type { ConfigLayerHead, ConfigScopeLevel } from '@/api/configFiles'
import { useInstanceGroups } from '@/api/instanceGroups'
import { useInstanceSearch } from '@/api/instances'
import {
  buildTracedRows,
  groupLayersByScope,
  scopeLevelLabelKey,
  shouldWarnInstancePrecedence,
} from '@/lib/config-explorer/config-layers'
import { apiErrorMessage } from '@/lib/shared/api-error'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { formatRelativeTime } from '@/lib/shared/relative-time'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'

/** 预览实例候选窗口（服务端搜索，避免千级全量列举）。 */
const INSTANCE_CANDIDATE_LIMIT = 50

/** 当前选中的作用域（含显示名，供标题与删除确认用）。 */
interface SelectedScope {
  level: ConfigScopeLevel
  id: number
  name: string
}

/** Unix 秒 → 本地时间文本（后端 layer 时间戳是秒）。 */
function formatUnixSeconds(sec: number): string {
  if (!sec) return ''
  const ms = sec * 1000
  return formatRelativeTime(ms)
}

/** 把扁平分组节点排成带缩进的下拉选项（先序，父在前）。 */
function groupOptions(nodes: InstanceGroupNode[] | undefined): ComboboxOption[] {
  const list = nodes ?? []
  const known = new Set(list.map((n) => n.id))
  const childrenOf = new Map<number | null, InstanceGroupNode[]>()
  for (const n of list) {
    const parent = n.parentId != null && known.has(n.parentId) ? n.parentId : null
    const bucket = childrenOf.get(parent)
    if (bucket) bucket.push(n)
    else childrenOf.set(parent, [n])
  }
  const sortFn = (a: InstanceGroupNode, b: InstanceGroupNode) =>
    a.sort !== b.sort ? a.sort - b.sort : a.id - b.id
  const out: ComboboxOption[] = []
  const seen = new Set<number>()
  const walk = (parent: number | null, depth: number) => {
    for (const n of [...(childrenOf.get(parent) ?? [])].sort(sortFn)) {
      if (seen.has(n.id)) continue
      seen.add(n.id)
      out.push({ value: String(n.id), label: `${'　'.repeat(depth)}${n.name}` })
      walk(n.id, depth + 1)
    }
  }
  walk(null, 0)
  // 孤儿/环：未走到的节点兜底追加，绝不丢分组。
  for (const n of list) {
    if (!seen.has(n.id)) out.push({ value: String(n.id), label: n.name })
  }
  return out
}

/** 
 * 分层配置·配置文件详情页。
 *
 * 布局：左侧层列表（分组层 / 实例层两组，各自显示当前 head 版本号），
 * 中间选中层的编辑器（原文 + 保存新版本 + 版本历史回退），底部有效值预览
 * （选一个实例 → 合并结果 + traced 来源表）。
 *
 * ## 两条必须明示的语义（真机验收要求，spec §7.4 / §7.5）
 *
 * 1. **预览是规范化结果**：`effective.content` 是「解析 → 合并 → 序列化」的产物，
 *    键按字典序、注释全丢。运维若把预览复制回磁盘就会丢掉注释与原始格式，
 *    故预览区常驻一条醒目提示（{@link NORMALIZED_NOTICE_KEY}）。
 * 2. **实例层优先级最高**：作用域链最右端是实例层，分组层压不住它。
 *    当预览选中的实例自己有实例层、且同时存在分组层时，额外提示要先删实例层。
 *    判断逻辑见 `shouldWarnInstancePrecedence`（纯函数，可单测）。
 */
export default function ConfigFileDetailPage() {
  const { t } = useTranslation()
  const params = useParams<{ id: string }>()
  const fileId = Number(params.id)

  const fileQuery = useConfigFile(Number.isFinite(fileId) ? fileId : null)
  const layersQuery = useConfigFileLayers(Number.isFinite(fileId) ? fileId : null)
  const groupsQuery = useInstanceGroups()
  const saveLayer = useSaveConfigFileLayer(fileId)
  const rollbackLayer = useRollbackConfigFileLayer(fileId)
  const deleteLayer = useDeleteConfigFileLayer(fileId)

  // 当前选中的层（null = 新建某作用域的 v1）。
  const [selected, setSelected] = useState<SelectedScope | null>(null)
  const [draft, setDraft] = useState('')
  const [remark, setRemark] = useState('')
  // 新建层的目标作用域（仅「新增层」时展开）。
  const [addingOpen, setAddingOpen] = useState(false)
  const [addLevel, setAddLevel] = useState<ConfigScopeLevel>('group')
  const [addScopeId, setAddScopeId] = useState<string>('')

  // 预览：选一个实例，看合并结果与逐键溯源。
  const [instanceKeyword, setInstanceKeyword] = useState('')
  const [previewInstance, setPreviewInstance] = useState<number | null>(null)
  const debouncedInstanceKeyword = useDebounced(instanceKeyword, 300).trim()
  const instanceSearch = useInstanceSearch(
    {
      ...(debouncedInstanceKeyword ? { q: debouncedInstanceKeyword } : {}),
      page: 1,
      pageSize: INSTANCE_CANDIDATE_LIMIT,
      sort: 'name',
      order: 'asc',
    },
    true,
  )
  const effectiveQuery = useConfigFileEffective(
    Number.isFinite(fileId) ? fileId : null,
    previewInstance,
  )

  const layers = layersQuery.data
  const grouped = useMemo(() => groupLayersByScope(layers), [layers])
  const file = fileQuery.data
  const format = file?.format ?? 'yaml'

  // 选中层的 head 版本与历史。
  const selectedHead: ConfigLayerHead | null =
    (layers ?? []).find((l) => l.scopeLevel === selected?.level && l.scopeRefId === selected?.id) ?? null
  const historyQuery = useConfigFileLayerHistory(
    Number.isFinite(fileId) ? fileId : null,
    selected?.level ?? null,
    selected?.id ?? null,
  )
  const history = historyQuery.data
  /** 该作用域当前的 head 版本（历史新→旧，取第一条）。 */
  const headVersion = history?.[0]

  /** 
   * 切换层时把草稿同步成该层 head 的原文。
   *
   * 用 effect 而不是在点击处直接 setDraft：`layers` 是异步查询出来的，首次进入页面时
   * 还没有任何层可选，草稿只能等数据到达后再对齐；同时保存新版本后 head 会变，
   * 编辑器也要跟着刷新到刚保存的那一版。
   *
   * 依赖取 `headVersion` 而不是整个历史数组：后者每次渲染都是新引用，会让编辑器
   * 在用户输入过程中被反复重置。
   */
  useEffect(() => {
    if (!selected) return
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 外部身份（选中层/其最新版本）变化时对齐草稿，属该规则的合法例外
    setDraft(headVersion?.content ?? '')
  }, [selected, headVersion])

  const instanceOptions: ComboboxOption[] = (instanceSearch.data?.items ?? []).map((i) => ({
    value: String(i.id),
    label: i.name,
  }))
  const previewInstanceName =
    instanceSearch.data?.items.find((i) => i.id === previewInstance)?.name ?? ''

  /** 约束 2 的判据（纯函数）：同时有分组层与实例层，且选中实例自己有实例层。 */
  const warnInstancePrecedence = shouldWarnInstancePrecedence(layers, previewInstance)
  const tracedRows = useMemo(() => buildTracedRows(effectiveQuery.data?.traced), [effectiveQuery.data])

  const selectLayer = (level: ConfigScopeLevel, id: number, name: string) => {
    setSelected({ level, id, name })
    setAddingOpen(false)
    setRemark('')
  }

  const startAddLayer = () => {
    setSelected(null)
    setDraft('')
    setRemark('')
    setAddLevel('group')
    setAddScopeId('')
    setAddingOpen(true)
  }

  const saveNewVersion = async () => {
    if (!selected) return
    try {
      await saveLayer.mutateAsync({
        scopeType: selected.level,
        scopeId: selected.id,
        content: draft,
        remark: remark.trim() || undefined,
      })
      toast.success(t('configFiles.layerSaved'))
      setRemark('')
    } catch (err) {
      toast.error(apiErrorMessage(err, t('configFiles.layerSaveFailed')))
    }
  }

  const saveFirstVersion = async () => {
    const scopeId = Number(addScopeId)
    if (!Number.isFinite(scopeId) || scopeId <= 0) return
    try {
      await saveLayer.mutateAsync({ scopeType: addLevel, scopeId, content: draft, remark: remark.trim() || undefined })
      toast.success(t('configFiles.layerSaved'))
      setAddingOpen(false)
      setDraft('')
      setRemark('')
    } catch (err) {
      toast.error(apiErrorMessage(err, t('configFiles.layerSaveFailed')))
    }
  }

  const rollbackTo = async (versionId: number) => {
    if (!selected) return
    try {
      await rollbackLayer.mutateAsync({
        scopeType: selected.level,
        scopeId: selected.id,
        versionId,
        remark: t('configFiles.rollbackRemark', { versionId }),
      })
      toast.success(t('configFiles.layerRolledBack'))
    } catch (err) {
      toast.error(apiErrorMessage(err, t('configFiles.layerRollbackFailed')))
    }
  }

  const removeLayer = async () => {
    if (!selected) return
    try {
      await deleteLayer.mutateAsync({ scopeType: selected.level, scopeId: selected.id })
      toast.success(t('configFiles.layerDeleted'))
      setSelected(null)
      setDraft('')
    } catch (err) {
      toast.error(apiErrorMessage(err, t('configFiles.layerDeleteFailed')))
    }
  }

  const [deleteOpen, setDeleteOpen] = useState(false)

  return (
    <PageShell>
      <PageHeader
        title={file?.name ?? t('configFiles.detailFallbackTitle')}
        description={t('configFiles.detailDesc')}
        breadcrumbs={[
          { label: t('configFiles.title'), to: '/config-files' },
          { label: file?.name ?? '…' },
        ]}
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link to="/config-files">
              <ArrowLeft aria-hidden />
              {t('configFiles.backToList')}
            </Link>
          </Button>
        }
      />

      {fileQuery.isLoading ? (
        <ListSkeleton rows={4} />
      ) : fileQuery.isError ? (
        <p className="text-sm text-destructive" data-testid="config-file-detail-error">
          {apiErrorMessage(fileQuery.error, t('configFiles.loadFailed'))}
        </p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-[minmax(200px,260px)_minmax(0,1fr)]">
          {/* 左：层列表，按作用域分组 */}
          <Panel
            title={t('configFiles.layersTitle')}
            bodyClassName="p-0"
            actions={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('configFiles.addLayer')}
                onClick={startAddLayer}
                data-testid="config-layer-add"
              >
                <Plus aria-hidden />
              </Button>
            }
          >
            {layersQuery.isLoading ? (
              <div className="space-y-2 p-3">
                <Skeleton className="h-4 w-32" />
                <Skeleton className="h-4 w-40" />
              </div>
            ) : layersQuery.isError ? (
              // 取数失败必须与「真的没有层」区分：否则运维会去给一个已有版本的作用域重建层。
              <p className="px-3 py-4 text-xs text-destructive" data-testid="config-layers-error">
                {apiErrorMessage(layersQuery.error, t('configFiles.loadFailed'))}
              </p>
            ) : (layers ?? []).length === 0 ? (
              <EmptyState title={t('configFiles.noLayers')} description={t('configFiles.noLayersHint')} />
            ) : (
              <div className="py-1">
                <LayerSection
                  title={t('configFiles.scope.group')}
                  layers={grouped.groups}
                  selected={selected}
                  onSelect={selectLayer}
                />
                <LayerSection
                  title={t('configFiles.scope.instance')}
                  layers={grouped.instances}
                  selected={selected}
                  onSelect={selectLayer}
                />
              </div>
            )}
          </Panel>

          {/* 右：编辑器 / 新增层 / 版本历史 */}
          <div className="flex min-w-0 flex-col gap-4">
            {addingOpen ? (
              <Panel title={t('configFiles.addLayerTitle')}>
                <div className="space-y-3">
                  <div className="flex flex-wrap items-end gap-3">
                    <div className="space-y-1">
                      <Label htmlFor="config-layer-level">{t('configFiles.fieldScopeLevel')}</Label>
                      <NativeSelect
                        id="config-layer-level"
                        value={addLevel}
                        onChange={(e) => {
                          setAddLevel(e.target.value as ConfigScopeLevel)
                          setAddScopeId('')
                        }}
                      >
                        <option value="group">{t('configFiles.scope.group')}</option>
                        <option value="instance">{t('configFiles.scope.instance')}</option>
                      </NativeSelect>
                    </div>
                    <div className="space-y-1">
                      <Label>{t('configFiles.fieldScopeRef')}</Label>
                      <div className="w-56">
                        {addLevel === 'group' ? (
                          <NativeSelect
                            aria-label={t('configFiles.fieldScopeRef')}
                            value={addScopeId}
                            onChange={(e) => setAddScopeId(e.target.value)}
                            data-testid="config-layer-scope-select"
                          >
                            <option value="">{t('common.selectPlaceholder')}</option>
                            {groupOptions(groupsQuery.data).map((opt) => (
                              <option key={opt.value} value={opt.value}>
                                {opt.label}
                              </option>
                            ))}
                          </NativeSelect>
                        ) : (
                          <Combobox
                            ariaLabel={t('configFiles.fieldScopeRef')}
                            options={instanceOptions}
                            value={addScopeId}
                            onChange={setAddScopeId}
                            onQueryChange={setInstanceKeyword}
                            placeholder={t('common.selectPlaceholder')}
                          />
                        )}
                      </div>
                    </div>
                  </div>
                  <Textarea
                    aria-label={t('configFiles.fieldContent')}
                    value={draft}
                    rows={10}
                    className="font-mono text-xs"
                    placeholder={t('configFiles.contentPlaceholder', { format })}
                    onChange={(e) => setDraft(e.target.value)}
                  />
                  <div className="flex items-center justify-between gap-2">
                    <Button variant="outline" size="sm" onClick={() => setAddingOpen(false)}>
                      {t('common.cancel')}
                    </Button>
                    <Button
                      size="sm"
                      onClick={() => { void saveFirstVersion() }}
                      disabled={!addScopeId || saveLayer.isPending}
                      data-testid="config-layer-create-submit"
                    >
                      {saveLayer.isPending ? t('common.saving') : t('common.save')}
                    </Button>
                  </div>
                </div>
              </Panel>
            ) : selected ? (
              <Panel
                title={t('configFiles.editorTitle', {
                  scope: t(scopeLevelLabelKey(selected.level)),
                  name: selected.name || `#${selected.id}`,
                })}
                actions={
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setDeleteOpen(true)}
                      data-testid="config-layer-delete"
                    >
                      <Trash2 aria-hidden />
                      {t('configFiles.deleteLayer')}
                    </Button>
                  </div>
                }
              >
                <div className="space-y-3">
                  <p className="text-xs text-muted-foreground">
                    {selectedHead
                      ? t('configFiles.headHint', {
                          versionNo: selectedHead.versionNo,
                          time: formatUnixSeconds(selectedHead.createdAt) || t('common.na'),
                        })
                      : t('configFiles.headMissing')}
                  </p>
                  <Textarea
                    aria-label={t('configFiles.fieldContent')}
                    value={draft}
                    rows={12}
                    className="font-mono text-xs"
                    onChange={(e) => setDraft(e.target.value)}
                    data-testid="config-layer-editor"
                  />
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      value={remark}
                      onChange={(e) => setRemark(e.target.value)}
                      placeholder={t('configFiles.remarkPlaceholder')}
                      aria-label={t('configFiles.remarkPlaceholder')}
                      className="h-8 max-w-xs"
                    />
                    <Button
                      size="sm"
                      onClick={() => { void saveNewVersion() }}
                      disabled={saveLayer.isPending}
                      data-testid="config-layer-save"
                    >
                      {saveLayer.isPending ? t('common.saving') : t('configFiles.saveVersion')}
                    </Button>
                  </div>

                  <div className="border-t pt-3">
                    <div className="mb-2 flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                      <History className="size-3.5" aria-hidden />
                      {t('configFiles.historyTitle')}
                    </div>
                    {historyQuery.isLoading ? (
                      <Skeleton className="h-4 w-40" />
                    ) : (history ?? []).length === 0 ? (
                      <p className="text-xs text-muted-foreground">{t('configFiles.historyEmpty')}</p>
                    ) : (
                      <ul className="space-y-1" data-testid="config-layer-history">
                        {(history ?? []).map((ver) => (
                          <li
                            key={ver.id}
                            className="flex items-center justify-between gap-2 rounded px-1 py-1 text-xs hover:bg-muted/50"
                          >
                            <span className="tabular-nums">
                              v{ver.versionNo}
                              <span className="ml-2 text-muted-foreground">
                                {formatUnixSeconds(ver.createdAt) || t('common.na')}
                              </span>
                              {ver.remark ? (
                                <span className="ml-2 text-muted-foreground">{ver.remark}</span>
                              ) : null}
                            </span>
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => { void rollbackTo(ver.id) }}
                              disabled={rollbackLayer.isPending}
                              data-testid={`config-layer-rollback-${ver.id}`}
                            >
                              {t('configFiles.rollbackTo')}
                            </Button>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                </div>
              </Panel>
            ) : (
              <Panel title={t('configFiles.editorPlaceholderTitle')}>
                <EmptyState
                  icon={<Layers aria-hidden />}
                  title={t('configFiles.editorPlaceholder')}
                  description={t('configFiles.editorPlaceholderHint')}
                />
              </Panel>
            )}

            {/* 底部：有效值预览 + 溯源 */}
            <Panel
              title={t('configFiles.previewTitle')}
              actions={
                <div className="flex items-center gap-2">
                  <div className="w-56">
                    <Combobox
                      ariaLabel={t('configFiles.previewInstance')}
                      options={instanceOptions}
                      value={previewInstance == null ? '' : String(previewInstance)}
                      onChange={(v) => setPreviewInstance(v ? Number(v) : null)}
                      onQueryChange={setInstanceKeyword}
                      placeholder={t('configFiles.previewInstancePlaceholder')}
                    />
                  </div>
                  <Button variant="outline" size="sm" asChild>
                    <Link to="/config-baselines">
                      <GitCompareArrows aria-hidden />
                      {t('nav.configBaselines')}
                    </Link>
                  </Button>
                </div>
              }
            >
              <div className="space-y-3">
                {/*
                  约束 1（spec §7.4）：无论是否加载出结果都常驻，因为用户点下预览之前
                  就该知道「这块内容不是磁盘原文」。文案与 spec 逐字对齐。
                */}
                <Alert variant="warning" data-testid="config-preview-normalized-notice">
                  <AlertTriangle aria-hidden />
                  <AlertTitle>{t('configFiles.normalizedNoticeTitle')}</AlertTitle>
                  <AlertDescription>{t('configFiles.normalizedNotice')}</AlertDescription>
                </Alert>

                {/*
                  约束 2（spec §7.5）：仅在「分组层与实例层同时存在，且所选实例自己有实例层」时出现。
                  判据见 shouldWarnInstancePrecedence（纯函数，由 dom 测试逐条覆盖）。
                */}
                {warnInstancePrecedence && (
                  <Alert variant="destructive" data-testid="config-preview-instance-precedence">
                    <AlertTriangle aria-hidden />
                    <AlertTitle>{t('configFiles.instancePrecedenceTitle')}</AlertTitle>
                    <AlertDescription>
                      {t('configFiles.instancePrecedence', { name: previewInstanceName || `#${previewInstance}` })}
                    </AlertDescription>
                  </Alert>
                )}

                {previewInstance == null ? (
                  <EmptyState
                    icon={<Layers aria-hidden />}
                    title={t('configFiles.previewEmpty')}
                    description={t('configFiles.previewEmptyHint')}
                  />
                ) : effectiveQuery.isLoading ? (
                  <Skeleton className="h-24 w-full" />
                ) : effectiveQuery.isError ? (
                  <p className="text-sm text-destructive" data-testid="config-preview-error">
                    {apiErrorMessage(effectiveQuery.error, t('configFiles.previewFailed'))}
                  </p>
                ) : (
                  <>
                    {(effectiveQuery.data?.warnings ?? []).length > 0 && (
                      <div className="space-y-1" data-testid="config-preview-warnings">
                        {(effectiveQuery.data?.warnings ?? []).map((w, i) => (
                          <p key={`${w.code}-${i}`} className="text-xs text-status-warning">
                            {w.message}
                          </p>
                        ))}
                      </div>
                    )}
                    <div>
                      <div className="mb-1 text-xs font-medium text-muted-foreground">
                        {t('configFiles.previewContent')}
                        <span className="ml-2 font-mono">{effectiveQuery.data?.effectiveHash?.slice(0, 12)}</span>
                      </div>
                      <pre
                        className="max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs whitespace-pre"
                        data-testid="config-preview-content"
                      >
                        {effectiveQuery.data?.content ?? ''}
                      </pre>
                    </div>
                    <div>
                      <div className="mb-1 text-xs font-medium text-muted-foreground">
                        {t('configFiles.tracedTitle')}
                      </div>
                      {tracedRows.length === 0 ? (
                        <p className="text-xs text-muted-foreground">{t('configFiles.tracedEmpty')}</p>
                      ) : (
                        <Table containerClassName="max-h-72 overflow-y-auto" data-testid="config-traced-table">
                          <TableHeader>
                            <TableRow>
                              <TableHead>{t('configFiles.tracedKey')}</TableHead>
                              <TableHead>{t('configFiles.tracedValue')}</TableHead>
                              <TableHead>{t('configFiles.tracedSource')}</TableHead>
                              <TableHead className="text-right">{t('configFiles.tracedVersion')}</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {tracedRows.map((row) => (
                              <TableRow key={row.key} data-testid={`config-traced-row-${row.key}`}>
                                <TableCell className="font-mono text-xs">{row.key}</TableCell>
                                <TableCell className="font-mono text-xs">
                                  {row.deleted ? (
                                    <span className="text-muted-foreground">{t('configFiles.tracedDeleted')}</span>
                                  ) : (
                                    row.valueText
                                  )}
                                </TableCell>
                                <TableCell className="text-xs">
                                  {t(scopeLevelLabelKey(row.level))}
                                  {row.sourceName ? ` · ${row.sourceName}` : ` · #${row.sourceId}`}
                                </TableCell>
                                <TableCell className="text-right font-mono text-xs tabular-nums">
                                  v{row.versionNo}
                                </TableCell>
                              </TableRow>
                            ))}
                          </TableBody>
                        </Table>
                      )}
                    </div>
                  </>
                )}
              </div>
            </Panel>
          </div>
        </div>
      )}

      <DangerConfirm
        open={deleteOpen}
        title={t('configFiles.deleteLayerTitle')}
        description={t('configFiles.deleteLayerBody', {
          scope: selected ? t(scopeLevelLabelKey(selected.level)) : '',
          name: selected?.name ?? '',
        })}
        onConfirm={() => {
          setDeleteOpen(false)
          void removeLayer()
        }}
        onCancel={() => setDeleteOpen(false)}
        pending={deleteLayer.isPending}
      />
    </PageShell>
  )
}

/** 一组层（分组层 / 实例层）：不可变追加链的「链头」列表。 */
function LayerSection({
  title,
  layers,
  selected,
  onSelect,
}: {
  title: string
  layers: ConfigLayerHead[]
  selected: SelectedScope | null
  onSelect: (level: ConfigScopeLevel, id: number, name: string) => void
}) {
  // 整组没有层就不渲染标题，避免左栏出现一列空小节。
  if (layers.length === 0) return null
  return (
    <div className="border-b border-border/60 px-2 py-2 last:border-b-0">
      <div className="mb-1 px-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
        {title}
      </div>
      <ul>
        {layers.map((layer) => {
          const active = selected?.level === layer.scopeLevel && selected.id === layer.scopeRefId
          return (
            <li key={`${layer.scopeLevel}-${layer.scopeRefId}`}>
              <button
                type="button"
                onClick={() => onSelect(layer.scopeLevel, layer.scopeRefId, layer.scopeName)}
                aria-current={active ? 'true' : undefined}
                data-testid={`config-layer-item-${layer.scopeLevel}-${layer.scopeRefId}`}
                className={`flex w-full items-center justify-between gap-2 rounded px-1.5 py-1 text-left text-sm transition-colors hover:bg-muted/60 ${
                  active ? 'bg-muted font-medium text-foreground' : 'text-muted-foreground'
                }`}
              >
                <span className="truncate">{layer.scopeName || `#${layer.scopeRefId}`}</span>
                <span className="shrink-0 font-mono text-[11px] tabular-nums">v{layer.versionNo}</span>
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
