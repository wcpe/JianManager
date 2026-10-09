/**
 * @file ArtifactReconcileSectionView：制品 S3 一致性对账区块的受控视图，设置查询、运行列表、差异分页与两个处置动作由应用容器负责。
 * @input Panel/Button/Input/Dialog/Table/scrollable-dialog 原语、lib/runtime-assets-view（字节格式化）、views/DangerConfirm、翻译上下文
 * @output ArtifactReconcileSectionView、ArtifactReconcileSectionViewProps、ArtifactReportState、ArtifactDiffQueryState、各视图契约类型
 * @sync apps/control-plane-web/src/pages/ArtifactReconcileSection.tsx、apps/control-plane-web/src/pages/RuntimeAssetsPage.tsx
 * @since FR-502（组件受控化迁包；原 FR-349 制品索引可视化与 S3 一致性对账）
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Settings2 } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/views/DangerConfirm'
import { cn } from '@jianmanager/ui'
import { formatBytes } from '@/lib/runtime-assets/runtime-assets-view'

/** 制品存储渠道引用（本视图只消费展示与筛选所需字段）。 */
export interface ArtifactChannelRefView {
  id: number
  name: string
  type: string
}

/** 一次对账运行（本视图只消费展示所需字段）。 */
export interface ArtifactReconcileRunView {
  id: number
  channelName: string
  status: 'running' | 'succeeded' | 'failed'
  triggeredBy: 'manual' | 'scheduled'
  startedAt: string
  indexCount: number
  objectCount: number
  matchedCount: number
  missingCount: number
  orphanCount: number
}

/** 一条差异明细（本视图只消费展示所需字段）。 */
export interface ArtifactReconcileDiffView {
  id: number
  sha256: string
  objectKey: string
  size: number
  lastModified?: string
  status: 'open' | 'resolved'
  resolvedAction: string
}

/** 差异分页结果（本视图只用 items 与 total，分页参数由容器注入）。 */
export interface ArtifactReconcileDiffPageView {
  items: ArtifactReconcileDiffView[]
  total: number
}

/** 对账设置（含下次计划运行时间）。 */
export interface ArtifactReconcileSettingsView {
  enabled: boolean
  intervalHours: number
  nextRunAt?: string
}

/**
 * 一侧差异的分页查询状态。
 *
 * 页码/页大小会触发重新取数（且是 API 查询参数），故归容器持有——视图只渲染并上报翻页意图。
 */
export interface ArtifactDiffQueryState {
  page: number
  pageSize: number
  /** 该页数据；缺省时按空列表渲染（与原实现「加载中即空表」一致）。 */
  data?: ArtifactReconcileDiffPageView
}

/** 报告弹窗的受控状态：选中运行 + 缺失/孤儿两侧的分页与数据。 */
export interface ArtifactReportState {
  run: ArtifactReconcileRunView
  missing: ArtifactDiffQueryState
  orphan: ArtifactDiffQueryState
}

/**
 * 受控边界：设置、运行列表、差异分页数据与全部在途标志经 props 注入；设置保存、手动触发、
 * 差异处置以回调上报，由外壳执行 mutation 并决定提示文案——包内不取数、不发请求、不弹 toast。
 * 弹窗开合、设置草稿与「待确认为哪一侧」是纯 UI 状态，留视图内；会触发重新取数的页码
 * （`ArtifactReportState`）与「当前看哪次运行」归容器。
 * 需要「成功才关闭 / 结算后才关闭」的两处用 `Promise<boolean>`（包内惯例，见 `NodeProbeVersionPanel`）。
 */
export interface ArtifactReconcileSectionViewProps {
  /** 制品渠道（含类型，用于筛出 S3 渠道）；外壳取数后注入。 */
  channels: ArtifactChannelRefView[]
  /** 近期对账运行；外壳取数后注入。缺省按空列表渲染（与原实现一致：无独立加载/错误态）。 */
  runs?: ArtifactReconcileRunView[]
  /** 对账设置；外壳取数后注入。 */
  settings?: ArtifactReconcileSettingsView
  /** 设置在途：禁用保存按钮。 */
  settingsSaving?: boolean
  /** 手动触发在途：禁用「立即运行」并转圈。 */
  triggering?: boolean
  /** 「标记丢失」在途：禁用并显示确认弹窗 pending。 */
  resolving?: boolean
  /** 「清理孤儿」在途：禁用并显示确认弹窗 pending。 */
  cleaning?: boolean
  /** 报告弹窗状态；缺省/null 表示关闭。 */
  report?: ArtifactReportState | null
  /** 保存对账设置（`enabled` + `intervalHours`）；返回是否成功（成功才关闭弹窗）。 */
  onSaveSettings: (payload: { enabled: boolean; intervalHours: number }) => Promise<boolean>
  /** 立即触发一次对账（全部 S3 渠道）。 */
  onTrigger: () => void
  /** 打开某次运行的差异报告；容器据此重置两侧页码并取数。 */
  onOpenReport: (run: ArtifactReconcileRunView) => void
  /** 关闭报告弹窗。 */
  onCloseReport: () => void
  /** 缺失项翻页（容器重新取数）。 */
  onMissingPageChange: (page: number) => void
  /** 孤儿项翻页（容器重新取数）。 */
  onOrphanPageChange: (page: number) => void
  /** 把缺失项标记为丢失；在途状态经 `resolving` 注入，返回是否成功（成败都关闭确认框）。 */
  onResolveMissing: () => Promise<boolean>
  /** 清理孤儿对象；在途状态经 `cleaning` 注入，返回是否成功（成败都关闭确认框）。 */
  onCleanupOrphans: () => Promise<boolean>
}

/** 运行状态 → 徽章配色。 */
function statusClass(status: ArtifactReconcileRunView['status']) {
  if (status === 'succeeded') return 'bg-status-success/15 text-status-success'
  if (status === 'failed') return 'bg-destructive/15 text-destructive'
  return 'bg-status-info/15 text-status-info'
}

/** 差异表分页器（页码由容器持有）。 */
function Pager({ page, total, pageSize, onChange }: { page: number; total: number; pageSize: number; onChange: (page: number) => void }) {
  const { t } = useTranslation()
  const pages = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="flex items-center justify-end gap-2 text-xs text-muted-foreground">
      <Button variant="outline" size="xs" disabled={page <= 1} onClick={() => onChange(page - 1)}>{t('artifactReconcile.previous')}</Button>
      <span>{page} / {pages}</span>
      <Button variant="outline" size="xs" disabled={page >= pages} onClick={() => onChange(page + 1)}>{t('artifactReconcile.next')}</Button>
    </div>
  )
}

/**
 * 制品索引与 S3 对象一致性对账（FR-349）：手动触发 + 定期设置 + 差异报告（缺失标记失效 / 孤儿二次确认清理）。
 * 仅当存在 S3 制品渠道时可用（无渠道时给出说明）。
 */
export function ArtifactReconcileSectionView({
  channels,
  runs,
  settings,
  settingsSaving = false,
  triggering = false,
  resolving = false,
  cleaning = false,
  report,
  onSaveSettings,
  onTrigger,
  onOpenReport,
  onCloseReport,
  onMissingPageChange,
  onOrphanPageChange,
  onResolveMissing,
  onCleanupOrphans,
}: ArtifactReconcileSectionViewProps) {
  const { t } = useTranslation()
  const s3Channels = channels.filter((channel) => channel.type === 's3')
  const [settingsOpen, setSettingsOpen] = useState(false)
  // 设置草稿：纯 UI 状态。打开时以容器注入的当前设置回填，关闭不改动服务端值。
  const [enabled, setEnabled] = useState(true)
  const [intervalHours, setIntervalHours] = useState(24)

  const openSettings = () => {
    setEnabled(settings?.enabled ?? true)
    setIntervalHours(settings?.intervalHours ?? 24)
    setSettingsOpen(true)
  }
  const saveSettings = async () => {
    // 成功才关弹窗；失败保留草稿由外壳提示错误。
    if (await onSaveSettings({ enabled, intervalHours })) setSettingsOpen(false)
  }

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">{t('artifactReconcile.title')}</h2>
          <p className="text-xs text-muted-foreground">{t('artifactReconcile.subtitle')}</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={openSettings}><Settings2 className="size-4" />{t('artifactReconcile.settings')}</Button>
          <Button size="sm" disabled={s3Channels.length === 0 || triggering} onClick={onTrigger}>
            <RefreshCw className={cn('size-4', triggering && 'animate-spin')} />{t('artifactReconcile.runNow')}
          </Button>
        </div>
      </div>

      {s3Channels.length === 0 ? (
        <Panel><p className="text-sm text-muted-foreground">{t('artifactReconcile.noS3')}</p></Panel>
      ) : (
        <Panel title={t('artifactReconcile.runs')} bodyClassName="p-0">
          <Table className="text-xs">
            <TableHeader><TableRow>
              <TableHead>{t('artifactReconcile.channel')}</TableHead><TableHead>{t('artifactReconcile.status')}</TableHead>
              <TableHead>{t('artifactReconcile.trigger')}</TableHead><TableHead>{t('artifactReconcile.startedAt')}</TableHead>
              <TableHead className="text-right">{t('artifactReconcile.indexCount')}</TableHead><TableHead className="text-right">{t('artifactReconcile.objectCount')}</TableHead>
              <TableHead className="text-right">{t('artifactReconcile.missing')}</TableHead><TableHead className="text-right">{t('artifactReconcile.orphan')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow></TableHeader>
            <TableBody>
              {(runs ?? []).map((run) => (
                <TableRow key={run.id}>
                  <TableCell>{run.channelName}</TableCell>
                  <TableCell><span className={cn('rounded px-1.5 py-0.5 text-[10px]', statusClass(run.status))}>{t(`artifactReconcile.status_${run.status}`)}</span></TableCell>
                  <TableCell>{t(`artifactReconcile.trigger_${run.triggeredBy}`)}</TableCell>
                  <TableCell>{new Date(run.startedAt).toLocaleString()}</TableCell>
                  <TableCell className="text-right">{run.indexCount}</TableCell><TableCell className="text-right">{run.objectCount}</TableCell>
                  <TableCell className="text-right text-destructive">{run.missingCount}</TableCell><TableCell className="text-right text-destructive">{run.orphanCount}</TableCell>
                  <TableCell className="text-right"><Button variant="outline" size="xs" disabled={run.status === 'running'} onClick={() => onOpenReport(run)}>{t('artifactReconcile.viewReport')}</Button></TableCell>
                </TableRow>
              ))}
              {(runs ?? []).length === 0 && <TableRow><TableCell colSpan={9} className="text-center text-muted-foreground">{t('artifactReconcile.noRuns')}</TableCell></TableRow>}
            </TableBody>
          </Table>
        </Panel>
      )}

      <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
        <DialogContent className={scrollableDialogContentClass}>
          <DialogHeader><DialogTitle>{t('artifactReconcile.settingsTitle')}</DialogTitle><DialogDescription>{t('artifactReconcile.settingsDescription')}</DialogDescription></DialogHeader>
          <ScrollableDialogBody className="space-y-4">
            <label className="flex items-center justify-between gap-3 text-sm"><span>{t('artifactReconcile.enabled')}</span><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /></label>
            <label className="block space-y-1 text-sm"><span>{t('artifactReconcile.intervalHours')}</span><Input type="number" min={1} max={720} value={intervalHours} onChange={(event) => setIntervalHours(Number(event.target.value))} /></label>
            {settings?.nextRunAt && <p className="text-xs text-muted-foreground">{t('artifactReconcile.nextRunAt', { time: new Date(settings.nextRunAt).toLocaleString() })}</p>}
          </ScrollableDialogBody>
          <DialogFooter><Button variant="outline" onClick={() => setSettingsOpen(false)}>{t('common.cancel')}</Button><Button disabled={settingsSaving || intervalHours < 1 || intervalHours > 720} onClick={() => { void saveSettings() }}>{t('common.save')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>

      {report && (
        <ReconcileReportDialog
          report={report}
          resolving={resolving}
          cleaning={cleaning}
          onClose={onCloseReport}
          onMissingPageChange={onMissingPageChange}
          onOrphanPageChange={onOrphanPageChange}
          onResolveMissing={onResolveMissing}
          onCleanupOrphans={onCleanupOrphans}
        />
      )}
    </section>
  )
}

/**
 * 差异报告弹窗：缺失（可标记失效）/ 孤儿（可二次确认清理）两段，各带独立分页。
 * 数据、页码与在途标志全部由容器注入；本组件只保留「当前待确认为哪一侧」这一 UI 状态。
 */
function ReconcileReportDialog({
  report,
  resolving,
  cleaning,
  onClose,
  onMissingPageChange,
  onOrphanPageChange,
  onResolveMissing,
  onCleanupOrphans,
}: {
  report: ArtifactReportState
  resolving: boolean
  cleaning: boolean
  onClose: () => void
  onMissingPageChange: (page: number) => void
  onOrphanPageChange: (page: number) => void
  onResolveMissing: () => Promise<boolean>
  onCleanupOrphans: () => Promise<boolean>
}) {
  const { t } = useTranslation()
  const { run, missing, orphan } = report
  // 二次确认意图：'missing' | 'orphan' | null。纯 UI 状态，留视图内。
  const [confirm, setConfirm] = useState<'missing' | 'orphan' | null>(null)
  const openMissing = missing.data?.items.some((item) => item.status === 'open') ?? false
  const openOrphans = orphan.data?.items.some((item) => item.status === 'open') ?? false

  // 原实现以 onSettled 关确认框：请求在途时保持打开并显示 pending，成败都关。
  const confirmAction = async () => {
    try {
      if (confirm === 'missing') await onResolveMissing()
      else if (confirm === 'orphan') await onCleanupOrphans()
    } finally {
      setConfirm(null)
    }
  }

  return (
    <>
      <Dialog open onOpenChange={(open) => { if (!open) onClose() }}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-5xl`}>
          <DialogHeader><DialogTitle>{t('artifactReconcile.reportTitle', { channel: run.channelName })}</DialogTitle><DialogDescription>{t('artifactReconcile.reportSummary', { index: run.indexCount, objects: run.objectCount, matched: run.matchedCount })}</DialogDescription></DialogHeader>
          <ScrollableDialogBody className="space-y-5">
            <DiffSection title={t('artifactReconcile.missingTitle')} action={<Button variant="destructive" size="sm" disabled={!openMissing || resolving} onClick={() => setConfirm('missing')}>{t('artifactReconcile.markLost')}</Button>}>
              <Table className="text-xs"><TableHeader><TableRow><TableHead>{t('artifactReconcile.sha')}</TableHead><TableHead>{t('artifactReconcile.objectKey')}</TableHead><TableHead className="text-right">{t('runtimeAssets.size')}</TableHead><TableHead>{t('artifactReconcile.disposition')}</TableHead></TableRow></TableHeader>
                <TableBody>{(missing.data?.items ?? []).map((item) => <TableRow key={item.id}><TableCell className="font-mono">{item.sha256.slice(0, 12)}</TableCell><TableCell className="max-w-96 truncate font-mono" title={item.objectKey}>{item.objectKey}</TableCell><TableCell className="text-right">{formatBytes(item.size)}</TableCell><TableCell>{item.status === 'open' ? t('artifactReconcile.open') : t(`artifactReconcile.action_${item.resolvedAction}`)}</TableCell></TableRow>)}</TableBody>
              </Table><Pager page={missing.page} total={missing.data?.total ?? 0} pageSize={missing.pageSize} onChange={onMissingPageChange} />
            </DiffSection>
            <DiffSection title={t('artifactReconcile.orphanTitle')} action={<Button variant="destructive" size="sm" disabled={!openOrphans || cleaning} onClick={() => setConfirm('orphan')}>{t('artifactReconcile.cleanupOrphans')}</Button>}>
              <Table className="text-xs"><TableHeader><TableRow><TableHead>{t('artifactReconcile.objectKey')}</TableHead><TableHead className="text-right">{t('runtimeAssets.size')}</TableHead><TableHead>{t('artifactReconcile.lastModified')}</TableHead><TableHead>{t('artifactReconcile.disposition')}</TableHead></TableRow></TableHeader>
                <TableBody>{(orphan.data?.items ?? []).map((item) => <TableRow key={item.id}><TableCell className="max-w-96 truncate font-mono" title={item.objectKey}>{item.objectKey}</TableCell><TableCell className="text-right">{formatBytes(item.size)}</TableCell><TableCell>{item.lastModified ? new Date(item.lastModified).toLocaleString() : '—'}</TableCell><TableCell>{item.status === 'open' ? t('artifactReconcile.open') : t(`artifactReconcile.action_${item.resolvedAction}`)}</TableCell></TableRow>)}</TableBody>
              </Table><Pager page={orphan.page} total={orphan.data?.total ?? 0} pageSize={orphan.pageSize} onChange={onOrphanPageChange} />
            </DiffSection>
          </ScrollableDialogBody>
          <DialogFooter><Button variant="outline" onClick={onClose}>{t('common.close')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
      <DangerConfirm open={confirm !== null} title={confirm === 'missing' ? t('artifactReconcile.markLostConfirm') : t('artifactReconcile.cleanupConfirm')} description={confirm === 'missing' ? t('artifactReconcile.markLostDescription') : t('artifactReconcile.cleanupDescription')} confirmLabel={confirm === 'missing' ? t('artifactReconcile.markLost') : t('artifactReconcile.cleanupOrphans')} pending={resolving || cleaning} onConfirm={() => { void confirmAction() }} onCancel={() => setConfirm(null)} />
    </>
  )
}

/** 报告内的一段差异（标题 + 右侧动作 + 表格），保持两段结构一致。 */
function DiffSection({ title, action, children }: { title: string; action: ReactNode; children: ReactNode }) {
  return <section className="space-y-2"><div className="flex items-center justify-between gap-2"><h3 className="text-sm font-semibold">{title}</h3>{action}</div><div className="rounded border">{children}</div></section>
}
