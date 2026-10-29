import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileSearch, ShieldAlert } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Textarea } from '@jianmanager/ui/components/textarea'
import { useConfigDiscover } from '@/api/configs'
import { useInstanceGroups, useInstanceGroupSubtree } from '@/api/instanceGroups'
import { useInstanceSearch } from '@/api/instances'
import { useAdoptConfigs, useAdoptPreview, useConfigHealth } from '@/api/configAdopt'
import type { AdoptFileResult, AdoptRequest, AdoptResponse, AdoptSummary } from '@/api/configAdopt'
import { CANDIDATE_LIMIT } from '@/components/views/instances/InstancePicker'
import { useDebounced } from '@/lib/hooks/use-debounced'
import type { StatusLevel } from '@jianmanager/ui'

/** 
 * 配置纳管页。
 *
 * 【语义核心：这是「纳管现有文件」，不是数据导入】
 * 磁盘上的配置文件就是唯一真源。本页做的事是「把磁盘现状登记成 JM 的基线版本」：
 * - **只读磁盘、绝不改写**（后端纳管服务只持有 `Discover` / `Read` 两个只读方法，
 *   这不是约定而是编译期事实）；
 * - 内容与磁盘**逐字节一致**，`contentHash` 即磁盘 sha256；
 * - 已纳管的文件报 `ALREADY_ADOPTED` 并**跳过**，不覆盖——防止冲掉运维后续做的修改；
 * - **坏掉的配置也照收**：记录现状优先于校验通过，解析失败只作 `warning`。
 *
 * 【实例层优先级】纳管建的是**实例层** v1，而实例层在作用域链最右侧、优先级最高，
 * 分组层覆盖不了它。这条必须在本页明示，否则运维纳管 100 台后会困惑
 * 「为什么改了分组层没反应」。
 *
 * 流程：选目标 → 扫描配置 → 勾选 → **预演** → 执行 → 逐文件结果。
 */

/** 单个待纳管文件的选择项（实例 + 路径的唯一键）。 */
interface Candidate {
  instanceId: number
  instanceName: string
  path: string
  format: string
  /** Discover 判定命中内置 schema（可走表单模式）；与「能否纳管」无关。 */
  supported: boolean
}

/** 可被纳管的格式（后端 `config_file` 只支持 yaml / json / properties）。 */
const ADOPTABLE_FORMATS = new Set(['yaml', 'yml', 'json', 'properties'])

/** 
 * 是否可纳管。
 *
 * `Discover` 会返回 `.toml` / `.txt` / `.conf` 供展示，但 `config_file` 只支持
 * yaml / json / properties——后端对这些报 `FORMAT_UNSUPPORTED` 而非硬塞成 yaml
 * （硬塞会让后续解析全错）。前端据此直接禁用勾选框，省掉一次注定失败的往返。
 */
function isAdoptable(path: string, format: string): boolean {
  const fmt = format.toLowerCase()
  if (ADOPTABLE_FORMATS.has(fmt)) return true
  // format 缺省时按扩展名兜底：Discover 对无扩展名文件返回空 format。
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  return ADOPTABLE_FORMATS.has(ext)
}

/** 结果分类：界面按此把「成功 / 跳过 / 失败」分开呈现。 */
type ResultKind = 'success' | 'skipped' | 'failed' | 'warned'

/** 把一条逐文件结果归入展示分类。 */
function resultKindOf(row: AdoptFileResult): ResultKind {
  if (row.status === 'failed') return 'failed'
  if (row.status === 'already_adopted') return 'skipped'
  if (row.warning) return 'warned'
  return 'success'
}

const RESULT_LEVEL: Record<ResultKind, StatusLevel> = {
  success: 'success',
  warned: 'warning',
  skipped: 'neutral',
  failed: 'danger',
}

/** 汇总计数（界面展示与测试共用；不依赖后端 summary 的字段命名）。 */
interface ResultCounts {
  success: number
  failed: number
  skipped: number
  warned: number
  total: number
}

/** 从响应里摊平全部逐文件结果（批量形态的明细嵌在 instances[].results 里）。 */
function flattenResults(res: AdoptResponse | undefined): AdoptFileResult[] {
  if (!res) return []
  if (res.results.length > 0) return res.results
  return (res.instances ?? []).flatMap((inst) => inst.results ?? [])
}

/** 统计各分类条数。 */
function countResults(rows: AdoptFileResult[]): ResultCounts {
  const counts: ResultCounts = { success: 0, failed: 0, skipped: 0, warned: 0, total: rows.length }
  for (const row of rows) {
    switch (resultKindOf(row)) {
      case 'failed':
        counts.failed += 1
        break
      case 'skipped':
        counts.skipped += 1
        break
      case 'warned':
        counts.warned += 1
        break
      default:
        counts.success += 1
    }
  }
  return counts
}

/** 汇总行的可读文案（后端 summary 与逐文件结果可能不一致，界面上以逐文件为准并标注差异）。 */
function summaryLine(summary: AdoptSummary): string {
  return `共 ${summary.total} 项：新建 ${summary.created}，追加 ${summary.appended}，已纳管 ${summary.alreadyAdopted}，失败 ${summary.failed}`
}

export default function ConfigAdoptPage() {
  const { t } = useTranslation()

  // ── 目标选择：实例 or 分组 ───────────────────────────────────────────────
  /** `instance` 单实例扫描；`group` 按分组批量纳管。 */
  const [mode, setMode] = useState<'instance' | 'group'>('instance')
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const [instKeyword, setInstKeyword] = useState('')
  const instQ = useDebounced(instKeyword, 300).trim()
  const { data: instPage } = useInstanceSearch(
    { ...(instQ ? { q: instQ } : {}), page: 1, pageSize: CANDIDATE_LIMIT, sort: 'name', order: 'asc' },
    mode === 'instance',
  )

  const { data: groups } = useInstanceGroups({ enabled: mode === 'group' })
  /** 参与批量的分组 id（含子分组开关见下）。 */
  const [groupIds, setGroupIds] = useState<number[]>([])
  const [includeDescendants, setIncludeDescendants] = useState(true)
  /** 
   * 批量模式的路径通配。后端**强制**要求它非空（不允许「纳管目录下全部文件」），
   * 默认给出最常见的一档，避免用户面对空输入框不知所措。
   */
  const [pathPatterns, setPathPatterns] = useState('server.properties\nplugins/*/config.yml')

  // ── 单实例扫描 ──────────────────────────────────────────────────────────
  const discover = useConfigDiscover(instanceId ?? 0)
  const candidates = useMemo<Candidate[]>(() => {
    if (mode !== 'instance' || !instanceId) return []
    const name =
      instPage?.items.find((i) => i.id === instanceId)?.name ??
      `#${instanceId}`
    return (discover.data?.files ?? []).map((f) => ({
      instanceId,
      instanceName: name,
      path: f.path,
      format: f.format,
      supported: f.supported,
    }))
  }, [mode, instanceId, discover.data, instPage])

  /** 已勾选的路径集合（单实例形态下路径即唯一键）。 */
  const [picked, setPicked] = useState<Set<string>>(new Set())
  /** 是否建立实例级 v1 基线（默认建，否则纳管只剩空壳 config_file）。 */
  const [createBaseline, setCreateBaseline] = useState(true)

  // ── 预演 / 执行 ─────────────────────────────────────────────────────────
  const preview = useAdoptPreview()
  const adopt = useAdoptConfigs()
  /** 当前展示的报告：预演结果优先，执行后换成执行结果。 */
  const [report, setReport] = useState<{ stage: 'preview' | 'executed'; res: AdoptResponse } | null>(null)

  // 分组批量模式的命中预览（只用来数「这一批会覆盖多少台」，不做纳管）。
  const subtree = useInstanceGroupSubtree(groupIds.length === 1 ? (groupIds[0] ?? null) : null)

  const pickedFiles = useMemo(
    () => [...picked].map((path) => ({ instanceId: instanceId ?? 0, path })),
    [picked, instanceId],
  )

  /** 通配模式逐行拆分后的结果（空行丢弃）。后端要求非空，因此这里也是「能否预演」的判据之一。 */
  const patternList = useMemo(
    () =>
      pathPatterns
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean),
    [pathPatterns],
  )

  /** 
   * 纳管请求体（两种形态共用入口）。
   *
   * 用判别式联合而不是「同时带上 files 与 target」：后端按 `target` 是否存在决定走批量还是
   * 逐文件路径，两者同时出现会让语义变成「批量 + 忽略 files」，是本页最容易误传的形状。
   */
  const request = useMemo<AdoptRequest>(
    () =>
      mode === 'group'
        ? { target: { groupIds, includeDescendants }, pathPatterns: patternList, createBaseline }
        : { files: pickedFiles, createBaseline },
    [mode, groupIds, includeDescendants, patternList, createBaseline, pickedFiles],
  )

  const canPreview = mode === 'group' ? groupIds.length > 0 && patternList.length > 0 : pickedFiles.length > 0

  const handlePreview = async () => {
    const res = await preview.mutateAsync({ ...request, dryRun: true }).catch(() => null)
    if (res) setReport({ stage: 'preview', res })
  }

  const handleExecute = async () => {
    const res = await adopt.mutateAsync(request).catch(() => null)
    if (res) setReport({ stage: 'executed', res })
  }

  const health = useConfigHealth()

  const rows = flattenResults(report?.res)
  const counts = countResults(rows)

  return (
    <PageShell>
      <PageHeader
        title={t('configAdopt.title')}
        description={t('configAdopt.description')}
        actions={
          <div className="flex items-center gap-2">
            <StatusBadge
              level="info"
              label={t('configAdopt.readOnlyBadge')}
              dot={false}
            />
          </div>
        }
      />

      {/* 纳管语义三条硬约束：读磁盘不改写 / 已纳管不覆盖 / 坏的也记录。
          这三条如果不说清，用户会把纳管误解成「导入」进而期待它改写文件。 */}
      <Panel title={t('configAdopt.semanticsTitle')} icon={<FileSearch className="size-4" />}>
        <ul className="space-y-1 text-sm text-muted-foreground" data-testid="adopt-semantics">
          <li>{t('configAdopt.semanticsNoWrite')}</li>
          <li>{t('configAdopt.semanticsNoOverwrite')}</li>
          <li>{t('configAdopt.semanticsBrokenOk')}</li>
        </ul>
      </Panel>

      {/* ── 1. 选目标 ───────────────────────────────────────────────── */}
      <Panel title={t('configAdopt.stepTarget')}>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant={mode === 'instance' ? 'default' : 'outline'}
              onClick={() => setMode('instance')}
            >
              {t('configAdopt.modeInstance')}
            </Button>
            <Button
              size="sm"
              variant={mode === 'group' ? 'default' : 'outline'}
              onClick={() => setMode('group')}
            >
              {t('configAdopt.modeGroup')}
            </Button>
          </div>

          {mode === 'instance' ? (
            <div className="space-y-2">
              <label className="text-xs text-muted-foreground" htmlFor="adopt-instance-keyword">
                {t('configAdopt.instanceKeyword')}
              </label>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  id="adopt-instance-keyword"
                  className="max-w-xs"
                  value={instKeyword}
                  placeholder={t('configAdopt.instanceKeywordPlaceholder')}
                  onChange={(e) => setInstKeyword(e.target.value)}
                />
                <NativeSelect
                  aria-label={t('configAdopt.instanceLabel')}
                  data-testid="adopt-instance-select"
                  value={instanceId ? String(instanceId) : ''}
                  onChange={(e) => {
                    setInstanceId(e.target.value ? Number(e.target.value) : null)
                    setPicked(new Set())
                    setReport(null)
                  }}
                >
                  <option value="">{t('common.selectPlaceholder')}</option>
                  {(instPage?.items ?? []).map((inst) => (
                    <option key={inst.id} value={inst.id}>
                      {inst.name}
                    </option>
                  ))}
                </NativeSelect>
              </div>
            </div>
          ) : (
            <div className="space-y-3">
              <div className="space-y-2">
                <span className="text-xs text-muted-foreground">{t('configAdopt.groupLabel')}</span>
                <div className="flex flex-wrap gap-2" data-testid="adopt-group-list">
                  {(groups ?? []).map((g) => {
                    const checked = groupIds.includes(g.id)
                    return (
                      <label
                        key={g.id}
                        className="flex items-center gap-2 rounded border border-border px-2 py-1 text-sm"
                      >
                        <Checkbox
                          checked={checked}
                          aria-label={g.name}
                          onCheckedChange={(v) => {
                            setGroupIds((cur) => (v === true ? [...cur, g.id] : cur.filter((x) => x !== g.id)))
                            setReport(null)
                          }}
                        />
                        {g.name}
                        <span className="text-xs text-muted-foreground">({g.instanceCount})</span>
                      </label>
                    )
                  })}
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm">
                <Checkbox
                  checked={includeDescendants}
                  aria-label={t('configAdopt.includeDescendants')}
                  onCheckedChange={(v) => setIncludeDescendants(v === true)}
                />
                {t('configAdopt.includeDescendants')}
              </label>
              {groupIds.length === 1 && subtree.data ? (
                <p className="text-xs text-muted-foreground" data-testid="adopt-group-subtree">
                  {t('configAdopt.groupSubtreeHint', { count: subtree.data.length })}
                </p>
              ) : null}
              <div className="space-y-2">
                <label className="text-xs text-muted-foreground" htmlFor="adopt-path-patterns">
                  {t('configAdopt.pathPatterns')}
                </label>
                <Textarea
                  id="adopt-path-patterns"
                  className="min-h-20 max-w-lg font-mono text-xs"
                  value={pathPatterns}
                  onChange={(e) => setPathPatterns(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">{t('configAdopt.pathPatternsHint')}</p>
              </div>
            </div>
          )}
        </div>
      </Panel>

      {/* ── 2. 扫描 + 勾选（仅单实例形态） ─────────────────────────────── */}
      {mode === 'instance' ? (
        <Panel
          title={t('configAdopt.stepPick')}
          actions={
            <Button
              size="sm"
              variant="outline"
              disabled={!instanceId || discover.isFetching}
              onClick={() => { void discover.refetch() }}
            >
              {discover.isFetching ? t('common.loading') : t('configAdopt.scan')}
            </Button>
          }
        >
          {!instanceId ? (
            <p className="text-sm text-muted-foreground">{t('configAdopt.pickInstanceFirst')}</p>
          ) : discover.isError ? (
            <p className="text-sm text-destructive">{t('configAdopt.scanFailed')}</p>
          ) : candidates.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('configAdopt.noFiles')}</p>
          ) : (
            <div className="space-y-2">
              {discover.data?.truncated ? (
                <p className="text-xs text-[color:var(--warning-ink)]" data-testid="adopt-truncated">
                  {t('configAdopt.truncated')}
                </p>
              ) : null}
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-10" />
                    <TableHead>{t('configAdopt.colPath')}</TableHead>
                    <TableHead>{t('configAdopt.colFormat')}</TableHead>
                    <TableHead>{t('configAdopt.colAdoptable')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {candidates.map((c) => {
                    const adoptable = isAdoptable(c.path, c.format)
                    return (
                      <TableRow key={c.path} data-testid="adopt-candidate-row">
                        <TableCell>
                          <Checkbox
                            aria-label={c.path}
                            disabled={!adoptable}
                            checked={picked.has(c.path)}
                            onCheckedChange={(v) => {
                              setPicked((cur) => {
                                const next = new Set(cur)
                                if (v === true) next.add(c.path)
                                else next.delete(c.path)
                                return next
                              })
                              setReport(null)
                            }}
                          />
                        </TableCell>
                        <TableCell className="font-mono text-xs">{c.path}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">{c.format || '—'}</TableCell>
                        <TableCell>
                          {adoptable ? (
                            <StatusBadge level="success" label={t('configAdopt.adoptable')} dot={false} />
                          ) : (
                            // unsupported 的原因必须写清：否则用户以为「勾不上」是权限问题。
                            <StatusBadge
                              level="neutral"
                              label={t('configAdopt.formatUnsupported')}
                              dot={false}
                            />
                          )}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            </div>
          )}
        </Panel>
      ) : null}

      {/* ── 3. 预演 → 执行 ─────────────────────────────────────────────── */}
      <Panel title={t('configAdopt.stepRun')}>
        <div className="space-y-3">
          {mode === 'instance' ? (
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={createBaseline}
                aria-label={t('configAdopt.createBaseline')}
                onCheckedChange={(v) => setCreateBaseline(v === true)}
              />
              {t('configAdopt.createBaseline')}
            </label>
          ) : (
            <label className="flex items-center gap-2 text-sm">
              <Checkbox
                checked={createBaseline}
                aria-label={t('configAdopt.createBaseline')}
                onCheckedChange={(v) => setCreateBaseline(v === true)}
              />
              {t('configAdopt.createBaseline')}
            </label>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              data-testid="adopt-preview"
              disabled={!canPreview || preview.isPending}
              onClick={() => { void handlePreview() }}
            >
              {preview.isPending ? t('common.loading') : t('configAdopt.preview')}
            </Button>
            <Button
              size="sm"
              data-testid="adopt-execute"
              // 执行前**必须先预演**：纳管是不可撤销的写入（建 config_file 与 v1 版本），
              // 且已纳管不覆盖，误纳管的后果只能靠人工清理。
              disabled={!canPreview || report?.stage !== 'preview' || adopt.isPending}
              onClick={() => { void handleExecute() }}
            >
              {adopt.isPending ? t('common.saving') : t('configAdopt.execute')}
            </Button>
            {report?.stage !== 'preview' ? (
              <span className="text-xs text-muted-foreground" data-testid="adopt-need-preview">
                {t('configAdopt.needPreviewFirst')}
              </span>
            ) : null}
          </div>

          {/* 实例层优先级提示：纳管建的是实例层 v1，分组层覆盖不了它。 */}
          <p
            className="rounded border border-[color:var(--warning-line)] bg-[color:var(--warning-weak)] px-3 py-2 text-xs text-[color:var(--warning-ink)]"
            data-testid="adopt-instance-precedence"
          >
            {t('configAdopt.instancePrecedence')}
          </p>
        </div>
      </Panel>

      {/* ── 4. 报告：预演 / 执行结果 ───────────────────────────────────── */}
      {report ? (
        <Panel
          title={
            report.stage === 'preview' ? t('configAdopt.reportPreviewTitle') : t('configAdopt.reportExecutedTitle')
          }
          data-testid="adopt-report"
        >
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-3 text-sm">
              <span data-testid="adopt-report-summary">{summaryLine(report.res.summary)}</span>
              {/* 分类计数：成功 / 跳过 / 失败 必须分开，尤其「已纳管」不是失败。 */}
              <StatusBadge level="success" label={`成功 ${counts.success}`} />
              <StatusBadge level="neutral" label={`跳过 ${counts.skipped}`} />
              <StatusBadge level="warning" label={`告警 ${counts.warned}`} />
              <StatusBadge level="danger" label={`失败 ${counts.failed}`} />
              {report.res.dryRun ? (
                <StatusBadge level="info" label={t('configAdopt.dryRunBadge')} dot={false} />
              ) : null}
            </div>

            {rows.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('configAdopt.reportEmpty')}</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('configAdopt.colPath')}</TableHead>
                    <TableHead>{t('configAdopt.colStatus')}</TableHead>
                    <TableHead>{t('configAdopt.colHash')}</TableHead>
                    <TableHead>{t('configAdopt.colDetail')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => {
                    const kind = resultKindOf(row)
                    return (
                      <TableRow key={`${row.instanceId}:${row.path}`} data-testid="adopt-result-row">
                        <TableCell className="font-mono text-xs">{row.path}</TableCell>
                        <TableCell>
                          <StatusBadge
                            level={RESULT_LEVEL[kind]}
                            label={t(`configAdopt.fileStatus.${row.status}`)}
                          />
                        </TableCell>
                        <TableCell className="font-mono text-[11px] text-muted-foreground">
                          {row.contentHash ? row.contentHash.slice(0, 12) : '—'}
                        </TableCell>
                        <TableCell className="max-w-md text-xs">
                          {/* 解析告警单独一行提示：纳管照收，但它意味着这份配置本身是坏的。 */}
                          {row.warning ? (
                            <span className="block text-[color:var(--warning-ink)]">
                              {t('configAdopt.warningPrefix')}
                              {row.warning}
                            </span>
                          ) : null}
                          {row.message ? <span className="block text-muted-foreground">{row.message}</span> : null}
                          {!row.warning && !row.message ? '—' : null}
                        </TableCell>
                      </TableRow>
                    )
                  })}
                </TableBody>
              </Table>
            )}
          </div>
        </Panel>
      ) : null}

      {/* ── 5. 健康自检 ───────────────────────────────────────────────── */}
      <Panel
        title={t('configAdopt.healthTitle')}
        icon={<ShieldAlert className="size-4" />}
        actions={
          <Button size="sm" variant="outline" disabled={health.isFetching} onClick={() => { void health.refetch() }}>
            {t('common.refresh')}
          </Button>
        }
      >
        {health.isError ? (
          <p className="text-sm text-destructive">{t('configAdopt.healthFailed')}</p>
        ) : (
          <div className="space-y-2 text-sm" data-testid="adopt-health">
            <div className="flex flex-wrap gap-6">
              <span>
                {t('configAdopt.healthConfigFiles')}:{' '}
                <strong>{health.data?.configFileCount ?? '—'}</strong>
              </span>
              <span>
                {t('configAdopt.healthLayerVersions')}:{' '}
                <strong>{health.data?.layerVersionCount ?? '—'}</strong>
              </span>
              <span>
                {t('configAdopt.healthLastWrite')}:{' '}
                <strong>{health.data?.lastWriteAt ?? t('configAdopt.healthNever')}</strong>
              </span>
            </div>
            {/* 这句提示由后端下发、前端原样展示：JM 无法探测 Beacon 侧开关，
                因此这里只能提醒人工核对，不能做任何自动判定。 */}
            {health.data?.notice ? (
              <p className="text-xs text-muted-foreground" data-testid="adopt-health-notice">
                {health.data.notice}
              </p>
            ) : null}
          </div>
        )}
      </Panel>
    </PageShell>
  )
}
