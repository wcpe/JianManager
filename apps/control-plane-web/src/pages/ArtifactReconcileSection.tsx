import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Settings2 } from 'lucide-react'
import { toast } from 'sonner'

import {
  useCleanupOrphans,
  useReconcileDiffs,
  useReconcileRuns,
  useReconcileSettings,
  useResolveMissing,
  useTriggerReconcile,
  useUpdateReconcileSettings,
  type ArtifactReconcileRun,
} from '@/api/artifactReconcile'
import DangerConfirm from '@/components/DangerConfirm'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import { formatBytes } from './runtime-assets-view'

interface ApiError extends Error {
  response?: { data?: { message?: string } }
}

export interface ArtifactChannelRef {
  id: number
  name: string
  type: string
}

function statusClass(status: ArtifactReconcileRun['status']) {
  if (status === 'succeeded') return 'bg-status-success/15 text-status-success'
  if (status === 'failed') return 'bg-destructive/15 text-destructive'
  return 'bg-status-info/15 text-status-info'
}

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

export default function ArtifactReconcileSection({ channels }: { channels: ArtifactChannelRef[] }) {
  const { t } = useTranslation()
  const s3Channels = channels.filter((channel) => channel.type === 's3')
  const settings = useReconcileSettings()
  const runs = useReconcileRuns()
  const updateSettings = useUpdateReconcileSettings()
  const trigger = useTriggerReconcile()
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [intervalHours, setIntervalHours] = useState(24)
  const [reportRun, setReportRun] = useState<ArtifactReconcileRun | null>(null)

  const openSettings = () => {
    setEnabled(settings.data?.enabled ?? true)
    setIntervalHours(settings.data?.intervalHours ?? 24)
    setSettingsOpen(true)
  }
  const saveSettings = () => {
    updateSettings.mutate({ enabled, intervalHours }, {
      onSuccess: () => {
        toast.success(t('artifactReconcile.settingsSaved'))
        setSettingsOpen(false)
      },
      onError: (error: ApiError) => toast.error(error.response?.data?.message || t('artifactReconcile.actionFailed')),
    })
  }
  const runNow = () => {
    trigger.mutate(undefined, {
      onSuccess: (result) => toast.success(t('artifactReconcile.triggered', { started: result.started.length, skipped: result.skipped.length })),
      onError: (error: ApiError) => toast.error(error.response?.data?.message || t('artifactReconcile.actionFailed')),
    })
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
          <Button size="sm" disabled={s3Channels.length === 0 || trigger.isPending} onClick={runNow}>
            <RefreshCw className={cn('size-4', trigger.isPending && 'animate-spin')} />{t('artifactReconcile.runNow')}
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
              {(runs.data ?? []).map((run) => (
                <TableRow key={run.id}>
                  <TableCell>{run.channelName}</TableCell>
                  <TableCell><span className={cn('rounded px-1.5 py-0.5 text-[10px]', statusClass(run.status))}>{t(`artifactReconcile.status_${run.status}`)}</span></TableCell>
                  <TableCell>{t(`artifactReconcile.trigger_${run.triggeredBy}`)}</TableCell>
                  <TableCell>{new Date(run.startedAt).toLocaleString()}</TableCell>
                  <TableCell className="text-right">{run.indexCount}</TableCell><TableCell className="text-right">{run.objectCount}</TableCell>
                  <TableCell className="text-right text-destructive">{run.missingCount}</TableCell><TableCell className="text-right text-destructive">{run.orphanCount}</TableCell>
                  <TableCell className="text-right"><Button variant="outline" size="xs" disabled={run.status === 'running'} onClick={() => setReportRun(run)}>{t('artifactReconcile.viewReport')}</Button></TableCell>
                </TableRow>
              ))}
              {(runs.data ?? []).length === 0 && <TableRow><TableCell colSpan={9} className="text-center text-muted-foreground">{t('artifactReconcile.noRuns')}</TableCell></TableRow>}
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
            {settings.data?.nextRunAt && <p className="text-xs text-muted-foreground">{t('artifactReconcile.nextRunAt', { time: new Date(settings.data.nextRunAt).toLocaleString() })}</p>}
          </ScrollableDialogBody>
          <DialogFooter><Button variant="outline" onClick={() => setSettingsOpen(false)}>{t('common.cancel')}</Button><Button disabled={updateSettings.isPending || intervalHours < 1 || intervalHours > 720} onClick={saveSettings}>{t('common.save')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>

      {reportRun && <ReconcileReportDialog run={reportRun} open onOpenChange={(open) => { if (!open) setReportRun(null) }} />}
    </section>
  )
}

function ReconcileReportDialog({ run, open, onOpenChange }: { run: ArtifactReconcileRun; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const pageSize = 50
  const [missingPage, setMissingPage] = useState(1)
  const [orphanPage, setOrphanPage] = useState(1)
  const missing = useReconcileDiffs(run.id, 'missing', missingPage, pageSize)
  const orphan = useReconcileDiffs(run.id, 'orphan', orphanPage, pageSize)
  const resolve = useResolveMissing()
  const cleanup = useCleanupOrphans()
  const [confirm, setConfirm] = useState<'missing' | 'orphan' | null>(null)
  const openMissing = missing.data?.items.some((item) => item.status === 'open') ?? false
  const openOrphans = orphan.data?.items.some((item) => item.status === 'open') ?? false

  const confirmAction = () => {
    if (confirm === 'missing') {
      resolve.mutate(run.id, { onSuccess: (result) => toast.success(t('artifactReconcile.markedResult', result)), onSettled: () => setConfirm(null) })
    } else if (confirm === 'orphan') {
      cleanup.mutate(run.id, { onSuccess: (result) => toast.success(t('artifactReconcile.cleanedResult', result)), onSettled: () => setConfirm(null) })
    }
  }

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-5xl`}>
          <DialogHeader><DialogTitle>{t('artifactReconcile.reportTitle', { channel: run.channelName })}</DialogTitle><DialogDescription>{t('artifactReconcile.reportSummary', { index: run.indexCount, objects: run.objectCount, matched: run.matchedCount })}</DialogDescription></DialogHeader>
          <ScrollableDialogBody className="space-y-5">
            <DiffSection title={t('artifactReconcile.missingTitle')} action={<Button variant="destructive" size="sm" disabled={!openMissing || resolve.isPending} onClick={() => setConfirm('missing')}>{t('artifactReconcile.markLost')}</Button>}>
              <Table className="text-xs"><TableHeader><TableRow><TableHead>{t('artifactReconcile.sha')}</TableHead><TableHead>{t('artifactReconcile.objectKey')}</TableHead><TableHead className="text-right">{t('runtimeAssets.size')}</TableHead><TableHead>{t('artifactReconcile.disposition')}</TableHead></TableRow></TableHeader>
                <TableBody>{(missing.data?.items ?? []).map((item) => <TableRow key={item.id}><TableCell className="font-mono">{item.sha256.slice(0, 12)}</TableCell><TableCell className="max-w-96 truncate font-mono" title={item.objectKey}>{item.objectKey}</TableCell><TableCell className="text-right">{formatBytes(item.size)}</TableCell><TableCell>{item.status === 'open' ? t('artifactReconcile.open') : t(`artifactReconcile.action_${item.resolvedAction}`)}</TableCell></TableRow>)}</TableBody>
              </Table><Pager page={missingPage} total={missing.data?.total ?? 0} pageSize={pageSize} onChange={setMissingPage} />
            </DiffSection>
            <DiffSection title={t('artifactReconcile.orphanTitle')} action={<Button variant="destructive" size="sm" disabled={!openOrphans || cleanup.isPending} onClick={() => setConfirm('orphan')}>{t('artifactReconcile.cleanupOrphans')}</Button>}>
              <Table className="text-xs"><TableHeader><TableRow><TableHead>{t('artifactReconcile.objectKey')}</TableHead><TableHead className="text-right">{t('runtimeAssets.size')}</TableHead><TableHead>{t('artifactReconcile.lastModified')}</TableHead><TableHead>{t('artifactReconcile.disposition')}</TableHead></TableRow></TableHeader>
                <TableBody>{(orphan.data?.items ?? []).map((item) => <TableRow key={item.id}><TableCell className="max-w-96 truncate font-mono" title={item.objectKey}>{item.objectKey}</TableCell><TableCell className="text-right">{formatBytes(item.size)}</TableCell><TableCell>{item.lastModified ? new Date(item.lastModified).toLocaleString() : '—'}</TableCell><TableCell>{item.status === 'open' ? t('artifactReconcile.open') : t(`artifactReconcile.action_${item.resolvedAction}`)}</TableCell></TableRow>)}</TableBody>
              </Table><Pager page={orphanPage} total={orphan.data?.total ?? 0} pageSize={pageSize} onChange={setOrphanPage} />
            </DiffSection>
          </ScrollableDialogBody>
          <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t('common.close')}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
      <DangerConfirm open={confirm !== null} title={confirm === 'missing' ? t('artifactReconcile.markLostConfirm') : t('artifactReconcile.cleanupConfirm')} description={confirm === 'missing' ? t('artifactReconcile.markLostDescription') : t('artifactReconcile.cleanupDescription')} confirmLabel={confirm === 'missing' ? t('artifactReconcile.markLost') : t('artifactReconcile.cleanupOrphans')} pending={resolve.isPending || cleanup.isPending} onConfirm={confirmAction} onCancel={() => setConfirm(null)} />
    </>
  )
}

function DiffSection({ title, action, children }: { title: string; action: React.ReactNode; children: React.ReactNode }) {
  return <section className="space-y-2"><div className="flex items-center justify-between gap-2"><h3 className="text-sm font-semibold">{title}</h3>{action}</div><div className="rounded border">{children}</div></section>
}
