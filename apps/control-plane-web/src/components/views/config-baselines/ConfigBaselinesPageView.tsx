/**
 * @file ConfigBaselinesPageView：配置基线页的受控视图（基线列表 + 漂移面板 + 编辑弹窗），
 *       取数、三个写动作（保存 / 删除 / 收敛）与 toast 由应用容器负责。
 * @input lib/config-baseline（scopeKey 拼装与解析、哈希截断）、lib/instance-group（InstanceGroupNode）、
 *        views/DangerConfirm（删除二次确认）、Button/Input/Panel/Dialog/Table/StatusBadge 等原语、翻译上下文
 * @output ConfigBaselinesPageView、ConfigBaselinesPageViewProps、BaselineDriftPanelView、BaselineDriftPanelViewProps、
 *         BaselineEditorDialogView、BaselineEditorDialogViewProps、ConfigBaselineRow、BaselineDriftItemView、
 *         BaselineDriftView、BaselineConvergeOutcome、BaselineFormValues
 * @sync apps/control-plane-web/src/pages/ConfigBaselinesPage.tsx、apps/control-plane-web/src/pages/ConfigBaselinesPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-458 配置基线模板下发、跨实例漂移检测与一键收敛）
 */
import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { GitCompareArrows, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/views/DangerConfirm'
import { composeScopeKey, isValidScopeKey, scopeKindOf, scopeValueOf, shortHash } from '@/lib/config-baseline'
import type { ScopeKind } from '@/lib/config-baseline'
import type { InstanceGroupNode } from '@/lib/instance-group'

/**
 * 配置基线行（本视图渲染与编辑回填所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/configBaselines` 的 `ConfigBaseline`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `ScheduleLogRow`、`BackupStorageRow` 的取舍）。
 */
export interface ConfigBaselineRow {
  id: number
  /** 应共享该基线的实例集合：`all` / `group:<id>` / `network:<id>` / `tag:<tag>` / `instance:<id>`。 */
  scopeKey: string
  filePath: string
  /** 基线正文（列表不展示，编辑时回填草稿）。 */
  content: string
  /** sha256(content)，列表只展示截断值。 */
  contentHash: string
  /** 变更说明。 */
  message: string
}

/** 单实例漂移比对结果（本视图渲染所需的最小字段集）。 */
export interface BaselineDriftItemView {
  instanceId: number
  instanceName: string
  /** true = 与基线不一致。 */
  drift: boolean
  /** 该实例当前版本的哈希（无版本为空串）。 */
  currentHash: string
  /** 读取失败原因；有值即表示该行「无法判定」，不能参与一致性结论。 */
  error?: string
}

/** 漂移检测结果：`items` 为逐台明细，`drifted` 为后端统计的漂移台数。 */
export interface BaselineDriftView {
  items: BaselineDriftItemView[]
  drifted: number
}

/** 收敛汇总（视图只做内联回显，逐台明细与残余漂移不在此渲染）。 */
export interface BaselineConvergeOutcome {
  targeted: number
  succeeded: number
  failed: number
}

/**
 * 编辑器提交的草稿值：与基线落库体同形，但**原样未归一化**
 * （`filePath` / `message` 保留首尾空格）——trim 与「空 message 省略」属落库策略，由容器做。
 */
export interface BaselineFormValues {
  /** 已按 scope 种类拼好的 scopeKey（`all` 不带取值）。 */
  scopeKey: string
  filePath: string
  content: string
  /** 变更说明；未填为空串。 */
  message: string
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 基线列表、漂移明细、分组候选与全部加载/在途态经 props 注入
 *   （容器调 `useConfigBaselines` / `useBaselineDrift` / `useInstanceGroups`）；
 * - 三个写动作以回调上报，由容器执行 mutation 并决定成功/失败文案：保存与删除回传
 *   `Promise<boolean>`，视图据返回值决定是否关窗（失败时保留草稿 / 确认框便于重试）；
 *   收敛回传 `Promise<BaselineConvergeOutcome | null>`，仅用于面板内联回显汇总；
 * - **归容器**的受控状态：`driftBaselineId`——它是 `useBaselineDrift` 的查询键，一变就触发取数，
 *   属「面板在看哪条基线」这一查询语义；容器同时是其关闭动作（同 id 再触发即收起）的唯一持有者；
 * - **留本组件**的纯 UI 状态：编辑弹窗开合与编辑目标、删除确认目标、编辑器草稿；
 *   编辑目标不触发取数（取数由容器的 mutation 成功钩子失效缓存驱动）；
 * - 删除二次确认沿用原语义：scope 固定 `group`，是否放行由容器读角色等级后注入 `dangerAllowed`——
 *   组件库不持鉴权状态，故本视图可在无登录态环境（组件博物馆）独立渲染。
 */
export interface ConfigBaselinesPageViewProps {
  /** 基线列表；容器取数后注入（缺省或空数组渲染空态）。 */
  baselines?: ConfigBaselineRow[]
  /** 列表加载态。 */
  isLoading?: boolean
  /** 组织树分组候选项（`group:<id>` scope 的选择器数据源）；容器取数后注入。 */
  groups?: InstanceGroupNode[]
  /** 当前展开漂移面板的基线 ID；null 表示不展开（此时容器不发漂移请求）。 */
  driftBaselineId: number | null
  /** 展开/收起某条基线的漂移面板（同一 id 再次触发即为收起，由容器翻转）。 */
  onToggleDrift: (id: number) => void
  /** 当前基线（若有）的漂移明细；容器按 `driftBaselineId` 取数后注入。 */
  drift?: BaselineDriftView
  /** 漂移明细首次加载态（面板正文占位）。 */
  driftLoading?: boolean
  /** 漂移明细重取在途（按钮转圈并禁用，含后台刷新）。 */
  driftFetching?: boolean
  /** 重新拉取漂移明细。 */
  onRefreshDrift: () => void
  /** 收敛在途：禁用收敛按钮并显示进行中文案。 */
  converging?: boolean
  /** 保存在途：禁用编辑弹窗的提交按钮并显示保存中文案。 */
  saving?: boolean
  /** 删除在途：禁用确认按钮。 */
  deleting?: boolean
  /** 危险操作（删除）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
  /** 保存基线；`editingId` 为 null 表示创建。返回是否成功——成功才关窗。 */
  onSubmit: (values: BaselineFormValues, editingId: number | null) => Promise<boolean>
  /** 删除已确认的基线（二次确认已在本视图内完成）。返回是否成功——成功才关确认框。 */
  onDelete: (baseline: ConfigBaselineRow) => Promise<boolean>
  /** 一键收敛：把基线推送到所有漂移实例并复核残余漂移；回传汇总供面板内联回显。 */
  onConverge: (baselineId: number) => Promise<BaselineConvergeOutcome | null>
}

/** 漂移检测 + 一键收敛面板的受控契约：明细与两个在途态由外壳注入，收敛结果的内联回显留本组件。 */
export interface BaselineDriftPanelViewProps {
  /** 当前查看的基线 ID（容器据此取数；本组件只用于文案与收敛上报）。 */
  baselineId: number
  /** 漂移明细；缺省表示尚未取到（配合 `isLoading` 渲染占位）。 */
  drift?: BaselineDriftView
  /** 首次加载态。 */
  isLoading?: boolean
  /** 重取在途（刷新按钮转圈并禁用）。 */
  isFetching?: boolean
  /** 重新拉取明细。 */
  onRefresh: () => void
  /** 收敛在途。 */
  converging?: boolean
  /** 触发收敛；返回汇总（失败为 null，此时保留上次结果）。 */
  onConverge: (baselineId: number) => Promise<BaselineConvergeOutcome | null>
  /** 收起面板。 */
  onClose: () => void
}

/** 创建/编辑基线对话框的受控契约：`groups` 与 `saving` 由外壳决定，草稿与校验留本组件。 */
export interface BaselineEditorDialogViewProps {
  /** 编辑目标；null 表示创建（两者都用于挂载瞬间一次性回填草稿）。 */
  initial: ConfigBaselineRow | null
  /** 组织树分组候选（`group:<id>` 选择器）；缺省时退化为空候选列表。 */
  groups?: InstanceGroupNode[]
  /** 保存在途：禁用提交按钮并显示保存中文案。 */
  saving?: boolean
  /** 关闭/取消回调（提交成功后本组件也会调用它）。 */
  onClose: () => void
  /** 提交草稿；`editingId` 为 null 表示创建。返回是否成功——成功才关窗（失败保留草稿便于重试）。 */
  onSubmit: (values: BaselineFormValues, editingId: number | null) => Promise<boolean>
}

const SCOPE_KINDS: ScopeKind[] = ['all', 'group', 'network', 'tag', 'instance']

/** 组织树分组选项（含层级缩进），供 scope 选择器使用。 */
interface GroupOption {
  id: number
  name: string
  depth: number
}

/**
 * 把后端扁平组织树节点（ADR-033，parentId 邻接表）按层级展开为下拉选项。
 * 只读展示用途：对孤儿/环节点健壮——先走可达节点，未覆盖的兜底按根追加，绝不丢节点
 * （否则运维会遇到「分组存在但选不到」）。
 */
function flattenGroupOptions(nodes: InstanceGroupNode[]): GroupOption[] {
  const known = new Set(nodes.map((n) => n.id))
  const childrenOf = new Map<number | null, InstanceGroupNode[]>()
  for (const n of nodes) {
    const parent = n.parentId != null && known.has(n.parentId) ? n.parentId : null
    const bucket = childrenOf.get(parent)
    if (bucket) bucket.push(n)
    else childrenOf.set(parent, [n])
  }
  const sortFn = (a: InstanceGroupNode, b: InstanceGroupNode) =>
    a.sort !== b.sort ? a.sort - b.sort : a.id - b.id
  const out: GroupOption[] = []
  const seen = new Set<number>()
  const walk = (parent: number | null, depth: number) => {
    for (const n of [...(childrenOf.get(parent) ?? [])].sort(sortFn)) {
      if (seen.has(n.id)) continue
      seen.add(n.id)
      out.push({ id: n.id, name: n.name, depth })
      walk(n.id, depth + 1)
    }
  }
  walk(null, 0)
  for (const n of nodes) {
    if (!seen.has(n.id)) out.push({ id: n.id, name: n.name, depth: 0 })
  }
  return out
}

/** 漂移检测 + 一键收敛面板：明细只读渲染，收敛结果在面板内内联回显。 */
export function BaselineDriftPanelView({
  baselineId,
  drift,
  isLoading = false,
  isFetching = false,
  onRefresh,
  converging = false,
  onConverge,
  onClose,
}: BaselineDriftPanelViewProps) {
  const { t } = useTranslation()
  const [result, setResult] = useState<BaselineConvergeOutcome | null>(null)
  // 读取失败的行无法判定一致性（FR-458 nit）：全部 drifted===0 时若仍有 error 行，
  // 不能报「已全部一致」，否则运维会误以为集群齐平。
  const errorCount = (drift?.items ?? []).filter((it) => it.error).length
  const allConverged = !!drift && drift.drifted === 0 && errorCount === 0

  const handleConverge = async () => {
    const outcome = await onConverge(baselineId)
    // 只有成功才有汇总可回显；失败时保留上次结果（与原实现同：mutation 未成功则不更新 result）。
    if (outcome) setResult(outcome)
  }

  return (
    <Panel
      title={t('baselines.driftTitle', { id: baselineId })}
      bodyClassName="p-0"
      actions={
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" disabled={isFetching} onClick={onRefresh} data-testid="baseline-drift-refresh">
            <RefreshCw className={`mr-1 size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
            {t('baselines.refresh')}
          </Button>
          <Button
            size="sm"
            disabled={converging || !drift || drift.drifted === 0}
            onClick={() => { void handleConverge() }}
            data-testid="baseline-converge"
          >
            {converging ? t('baselines.converging') : t('baselines.converge')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onClose}>
            {t('baselines.close')}
          </Button>
        </div>
      }
    >
      {isLoading ? (
        <p className="p-4 text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="space-y-2 p-3">
          <div className="flex items-center gap-2 text-sm">
            <span>{t('baselines.driftedCount', { count: drift?.drifted ?? 0, total: drift?.items.length ?? 0 })}</span>
            {allConverged && <StatusBadge level="success" label={t('baselines.allConverged')} />}
            {drift && errorCount > 0 && (
              <StatusBadge level="warning" label={t('baselines.readFailedHint', { count: errorCount })} />
            )}
          </div>

          {result && (
            <div className="rounded-md border bg-muted/40 px-3 py-2 text-xs" data-testid="baseline-converge-result">
              {t('baselines.convergeResult', { targeted: result.targeted, succeeded: result.succeeded, failed: result.failed })}
            </div>
          )}

          <div className="max-h-80 overflow-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('baselines.colInstance')}</TableHead>
                  <TableHead>{t('baselines.colState')}</TableHead>
                  <TableHead>{t('baselines.colHash')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(drift?.items ?? []).map((it) => (
                  <TableRow key={it.instanceId}>
                    <TableCell className="text-xs">
                      <span className="font-mono text-muted-foreground">#{it.instanceId}</span> {it.instanceName}
                    </TableCell>
                    <TableCell>
                      {it.error ? (
                        <StatusBadge level="warning" label={t('baselines.errorState')} />
                      ) : it.drift ? (
                        <StatusBadge level="danger" label={t('baselines.drift')} />
                      ) : (
                        <StatusBadge level="success" label={t('baselines.inSync')} />
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-[11px] text-muted-foreground">{shortHash(it.currentHash)}</TableCell>
                  </TableRow>
                ))}
                {(drift?.items.length ?? 0) === 0 && (
                  <TableRow>
                    <TableCell colSpan={3} className="text-center text-xs text-muted-foreground">
                      {t('baselines.noInstances')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </div>
      )}
    </Panel>
  )
}

/**
 * 创建/编辑基线对话框。
 *
 * 挂载即打开、卸载即关闭（容器按需挂载，草稿随挂载按 `initial` 一次性初始化）——
 * 与原实现的 `<Dialog open>` 条件挂载一致，故无需 effect 回填，也不会残留上次草稿。
 */
export function BaselineEditorDialogView({
  initial,
  groups,
  saving = false,
  onClose,
  onSubmit,
}: BaselineEditorDialogViewProps) {
  const { t } = useTranslation()
  // 组织树分组（ADR-033）才是 `group:<id>` 的 id 空间：手填数字极易误填「用户组 id」
  // （两者 id 正交、数值上常巧合相等），故改为从分组树选择（NEW-ISSUE 修复）。
  const groupOptions = useMemo(() => flattenGroupOptions(groups ?? []), [groups])
  const [kind, setKind] = useState<ScopeKind>(initial ? scopeKindOf(initial.scopeKey) : 'all')
  const [scopeValue, setScopeValue] = useState(initial ? scopeValueOf(initial.scopeKey) : '')
  const [filePath, setFilePath] = useState(initial?.filePath ?? 'server.properties')
  const [content, setContent] = useState(initial?.content ?? '')
  const [message, setMessage] = useState(initial?.message ?? '')

  const scopeKey = composeScopeKey(kind, scopeValue)
  const valid = isValidScopeKey(scopeKey) && filePath.trim() !== ''
  // 编辑既有基线时，其分组可能已被删除（脏数据）：仍列出来，避免静默丢掉原 scope 取值。
  const orphanGroup = kind === 'group' && scopeValue !== '' && !groupOptions.some((g) => String(g.id) === scopeValue)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    // 非法时不发请求：提交按钮已按 !valid 禁用，此处只是兜底（原实现在该分支弹 toast，
    // toast 归容器后不再可达的提示不补——保持「不发请求」这一实质行为）。
    if (!valid) return
    // 失败（false，容器已提示）时不关窗、不清草稿，便于修正后重试。
    const ok = await onSubmit({ scopeKey, filePath, content, message }, initial?.id ?? null)
    if (ok) onClose()
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{initial ? t('baselines.editTitle') : t('baselines.createTitle')}</DialogTitle>
          <DialogDescription>{t('baselines.dialogDesc')}</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-3">
          <div>
            <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('baselines.scope')}</label>
            <div className="flex gap-2">
              <select
                aria-label={t('baselines.scopeKind')}
                className="h-8 w-32 shrink-0 rounded-md border bg-background px-2 text-sm"
                value={kind}
                onChange={(e) => setKind(e.target.value as ScopeKind)}
              >
                {SCOPE_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {t(`baselines.scopeKind_${k}`)}
                  </option>
                ))}
              </select>
              {kind === 'group' ? (
                <select
                  aria-label={t('baselines.scopeValue')}
                  data-testid="baseline-scope-value"
                  className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-xs"
                  value={scopeValue}
                  onChange={(e) => setScopeValue(e.target.value)}
                >
                  <option value="">{t('baselines.scopeGroupPlaceholder')}</option>
                  {groupOptions.map((g) => (
                    <option key={g.id} value={String(g.id)}>
                      {`${'\u3000'.repeat(g.depth)}${g.name} (#${g.id})`}
                    </option>
                  ))}
                  {orphanGroup && (
                    <option value={scopeValue}>{t('baselines.scopeGroupOrphan', { id: scopeValue })}</option>
                  )}
                </select>
              ) : (
                kind !== 'all' && (
                  <Input
                    aria-label={t('baselines.scopeValue')}
                    className="h-8 font-mono text-xs"
                    value={scopeValue}
                    onChange={(e) => setScopeValue(e.target.value)}
                    placeholder={kind === 'tag' ? 'prod' : '1'}
                  />
                )
              )}
            </div>
            {kind === 'group' && (
              <p className="mt-1 text-[11px] text-muted-foreground" data-testid="baseline-scope-group-hint">
                {t('baselines.scopeGroupHint')}
              </p>
            )}
            <p className="mt-1 font-mono text-[11px] text-muted-foreground">{scopeKey}</p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('baselines.filePath')}</label>
            <Input
              aria-label={t('baselines.filePath')}
              className="h-8 font-mono text-xs"
              value={filePath}
              onChange={(e) => setFilePath(e.target.value)}
              placeholder="server.properties"
            />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('baselines.content')}</label>
            <textarea
              aria-label={t('baselines.content')}
              className="h-40 w-full rounded-md border bg-background px-2 py-1.5 font-mono text-xs"
              value={content}
              onChange={(e) => setContent(e.target.value)}
            />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-muted-foreground">{t('baselines.message')}</label>
            <Input
              aria-label={t('baselines.message')}
              className="h-8 text-sm"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
            />
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={saving || !valid}>
              {saving ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 配置基线页（FR-458）：配置模板下发、跨实例漂移检测、一键收敛。
 *
 * 基线键为 `(scopeKey, filePath)`，`scopeKey` 限定应共享该基线的实例集合
 * （`all` / `group:<id>` 含子树 / `network:<id>` / `tag:<tag>` / `instance:<id>`）。
 * 漂移检测只读，收敛复用后端 `ConfigService.Write` 逐台推送并复核残余漂移。
 * 列表、漂移明细与两个弹窗的受控契约见 `ConfigBaselinesPageViewProps`。
 */
export function ConfigBaselinesPageView({
  baselines,
  isLoading = false,
  groups,
  driftBaselineId,
  onToggleDrift,
  drift,
  driftLoading = false,
  driftFetching = false,
  onRefreshDrift,
  converging = false,
  saving = false,
  deleting = false,
  dangerAllowed,
  onSubmit,
  onDelete,
  onConverge,
}: ConfigBaselinesPageViewProps) {
  const { t } = useTranslation()
  const [createOpen, setCreateOpen] = useState(false)
  // 正在编辑的基线（null 表示未编辑且 createOpen 时即创建模式）。
  const [editing, setEditing] = useState<ConfigBaselineRow | null>(null)
  // 待删除确认的基线。
  const [deleteTarget, setDeleteTarget] = useState<ConfigBaselineRow | null>(null)

  // 收窄后取局部常量：面板打开时 id 必非空，且在关闭回调里保持这一收窄。
  const driftId = driftBaselineId

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语。
    // 本页原页头的三段（h1 / 副标题 / 创建按钮）与 PageHeader 的 title / description /
    // actions 一一对应，映射干净。
    <PageShell data-page="config-baselines">
      <PageHeader
        title={t('baselines.title')}
        description={t('baselines.subtitle')}
        actions={
          <Button size="sm" onClick={() => { setEditing(null); setCreateOpen(true) }} data-testid="baseline-create">
            <Plus className="mr-1 size-4" />
            {t('baselines.create')}
          </Button>
        }
      />

      <Panel title={t('baselines.listTitle')} bodyClassName="p-0">
        {isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : !baselines || baselines.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">{t('baselines.empty')}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('baselines.colScope')}</TableHead>
                <TableHead>{t('baselines.colFile')}</TableHead>
                <TableHead>{t('baselines.colHash')}</TableHead>
                <TableHead>{t('baselines.colMessage')}</TableHead>
                <TableHead className="text-right">{t('baselines.colActions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {baselines.map((b) => (
                <TableRow key={b.id} data-testid={`baseline-row-${b.id}`}>
                  <TableCell className="font-mono text-xs">{b.scopeKey}</TableCell>
                  <TableCell className="font-mono text-xs">{b.filePath}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground" title={b.contentHash}>
                    {shortHash(b.contentHash)}
                  </TableCell>
                  <TableCell className="max-w-0 truncate text-xs">{b.message}</TableCell>
                  <TableCell className="text-right">
                    <div className="inline-flex gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onToggleDrift(b.id)}
                        data-testid={`baseline-drift-${b.id}`}
                      >
                        <GitCompareArrows className="mr-1 size-3.5" />
                        {t('baselines.detectDrift')}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => { setEditing(b); setCreateOpen(true) }}>
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="text-destructive"
                        onClick={() => setDeleteTarget(b)}
                        aria-label={t('baselines.delete')}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>

      {driftId !== null && (
        <BaselineDriftPanelView
          baselineId={driftId}
          drift={drift}
          isLoading={driftLoading}
          isFetching={driftFetching}
          onRefresh={onRefreshDrift}
          converging={converging}
          onConverge={onConverge}
          onClose={() => onToggleDrift(driftId)}
        />
      )}

      {createOpen && (
        <BaselineEditorDialogView
          initial={editing}
          groups={groups}
          saving={saving}
          onClose={() => { setCreateOpen(false); setEditing(null) }}
          onSubmit={onSubmit}
        />
      )}

      {/* 删除二次确认：scope/allowed 语义与原页一致——scope 固定 group，是否放行由容器注入。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('baselines.deleteTitle')}
        description={t('baselines.deleteDesc', { scope: deleteTarget?.scopeKey ?? '' })}
        confirmLabel={t('common.delete')}
        scope="group"
        allowed={dangerAllowed}
        pending={deleting}
        onConfirm={() => {
          if (!deleteTarget) return
          const target = deleteTarget
          // 失败时确认框保持打开（容器已提示失败原因），便于直接重试。
          void onDelete(target).then((ok) => { if (ok) setDeleteTarget(null) })
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default ConfigBaselinesPageView
