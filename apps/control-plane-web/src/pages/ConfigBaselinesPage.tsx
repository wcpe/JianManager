import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { GitCompareArrows, Plus, RefreshCw, Trash2 } from 'lucide-react'
import {
  useConfigBaselines,
  useUpsertBaseline,
  useDeleteBaseline,
  useBaselineDrift,
  useConvergeBaseline,
  type ConfigBaseline,
} from '@/api/configBaselines'
import { useInstanceGroups, type InstanceGroupNode } from '@/api/instanceGroups'
import { composeScopeKey, isValidScopeKey, scopeKindOf, scopeValueOf, shortHash, type ScopeKind } from '@/lib/config-baseline'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
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
import DangerConfirm from '@/components/DangerConfirm'

/**
 * 配置基线页（FR-458）：配置模板下发、跨实例漂移检测、一键收敛。
 *
 * 基线键为 `(scopeKey, filePath)`，`scopeKey` 限定应共享该基线的实例集合
 * （`all` / `group:<id>` 含子树 / `network:<id>` / `tag:<tag>` / `instance:<id>`）。
 * 漂移检测只读，收敛复用后端 `ConfigService.Write` 逐台推送并复核残余漂移。
 */
export default function ConfigBaselinesPage() {
  const { t } = useTranslation()
  const { data: baselines, isLoading } = useConfigBaselines()
  const del = useDeleteBaseline()
  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<ConfigBaseline | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ConfigBaseline | null>(null)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-xl font-bold">{t('baselines.title')}</h1>
          <p className="text-xs text-muted-foreground">{t('baselines.subtitle')}</p>
        </div>
        <Button size="sm" onClick={() => { setEditing(null); setCreateOpen(true) }} data-testid="baseline-create">
          <Plus className="mr-1 size-4" />
          {t('baselines.create')}
        </Button>
      </div>

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
                        onClick={() => setSelectedId((cur) => (cur === b.id ? null : b.id))}
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

      {selectedId !== null && <BaselineDriftPanel baselineId={selectedId} onClose={() => setSelectedId(null)} />}

      {createOpen && (
        <BaselineEditorDialog
          initial={editing}
          onClose={() => { setCreateOpen(false); setEditing(null) }}
        />
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('baselines.deleteTitle')}
        description={t('baselines.deleteDesc', { scope: deleteTarget?.scopeKey ?? '' })}
        confirmLabel={t('common.delete')}
        scope="group"
        pending={del.isPending}
        onConfirm={() => {
          if (deleteTarget) del.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) })
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/** 漂移检测 + 一键收敛面板。 */
function BaselineDriftPanel({ baselineId, onClose }: { baselineId: number; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: drift, isLoading, refetch, isFetching } = useBaselineDrift(baselineId)
  const converge = useConvergeBaseline()
  const [result, setResult] = useState<{ targeted: number; succeeded: number; failed: number } | null>(null)
  // 读取失败的行无法判定一致性（FR-458 nit）：全部 drifted===0 时若仍有 error 行，
  // 不能报「已全部一致」，否则运维会误以为集群齐平。
  const errorCount = (drift?.items ?? []).filter((it) => it.error).length
  const allConverged = !!drift && drift.drifted === 0 && errorCount === 0

  return (
    <Panel
      title={t('baselines.driftTitle', { id: baselineId })}
      bodyClassName="p-0"
      actions={
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" disabled={isFetching} onClick={() => refetch()} data-testid="baseline-drift-refresh">
            <RefreshCw className={`mr-1 size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
            {t('baselines.refresh')}
          </Button>
          <Button
            size="sm"
            disabled={converge.isPending || !drift || drift.drifted === 0}
            onClick={() =>
              converge.mutate(
                { baselineId },
                { onSuccess: (res) => setResult({ targeted: res.targeted, succeeded: res.succeeded, failed: res.failed }) },
              )
            }
            data-testid="baseline-converge"
          >
            {converge.isPending ? t('baselines.converging') : t('baselines.converge')}
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

/** 创建/编辑基线对话框。 */
function BaselineEditorDialog({ initial, onClose }: { initial: ConfigBaseline | null; onClose: () => void }) {
  const { t } = useTranslation()
  const upsert = useUpsertBaseline()
  // 组织树分组（ADR-033）才是 `group:<id>` 的 id 空间：手填数字极易误填「用户组 id」
  // （两者 id 正交、数值上常巧合相等），故改为从分组树选择（NEW-ISSUE 修复）。
  const { data: orgGroups } = useInstanceGroups()
  const groupOptions = useMemo(() => flattenGroupOptions(orgGroups ?? []), [orgGroups])
  const [kind, setKind] = useState<ScopeKind>(initial ? scopeKindOf(initial.scopeKey) : 'all')
  const [scopeValue, setScopeValue] = useState(initial ? scopeValueOf(initial.scopeKey) : '')
  const [filePath, setFilePath] = useState(initial?.filePath ?? 'server.properties')
  const [content, setContent] = useState(initial?.content ?? '')
  const [message, setMessage] = useState(initial?.message ?? '')

  const scopeKey = composeScopeKey(kind, scopeValue)
  const valid = isValidScopeKey(scopeKey) && filePath.trim() !== ''
  // 编辑既有基线时，其分组可能已被删除（脏数据）：仍列出来，避免静默丢掉原 scope 取值。
  const orphanGroup = kind === 'group' && scopeValue !== '' && !groupOptions.some((g) => String(g.id) === scopeValue)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!valid) {
      toast.error(t('baselines.invalid'))
      return
    }
    upsert.mutate(
      { scopeKey, filePath: filePath.trim(), content, message: message.trim() || undefined },
      { onSuccess: onClose },
    )
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
            <Button type="submit" disabled={upsert.isPending || !valid}>
              {upsert.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
