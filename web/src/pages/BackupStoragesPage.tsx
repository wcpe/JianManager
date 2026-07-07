import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useBackupStorages,
  useCreateBackupStorage,
  useDeleteBackupStorage,
  useTestBackupStorage,
  type BackupStorage,
  type BackupStorageTestResult,
  type CreateBackupStorageBody,
} from '@/api/backupStorages'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validateEnvRef, validateFields, hasErrors } from '@/lib/form-validation'
import DangerConfirm from '@/components/DangerConfirm'

const TYPES = ['s3', 'sftp', 'webdav'] as const
const TYPE_OPTIONS: ComboboxOption[] = TYPES.map((tp) => ({ value: tp, label: tp.toUpperCase() }))

const emptyForm: CreateBackupStorageBody = {
  name: '', type: 's3', endpoint: '', bucket: '', region: '', prefix: '',
  accessKeyEnv: '', secretKeyEnv: '', useSsl: true,
}

function formatBytes(bytes: number | undefined) {
  const value = Number(bytes ?? 0)
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let current = value / 1024
  for (const unit of units) {
    if (current < 1024 || unit === 'TB') {
      return `${current.toFixed(current >= 10 ? 0 : 1)} ${unit}`
    }
    current /= 1024
  }
  return `${value} B`
}

/**
 * 备份远程存储后端管理页（FR-057）。
 * 凭证以 ${ENV_VAR} 形式引用环境变量，不收明文（config-files.md）；仅平台管理员可访问。
 */
export default function BackupStoragesPage() {
  const { t } = useTranslation()
  const { data: storages, isLoading } = useBackupStorages()
  const create = useCreateBackupStorage()
  const del = useDeleteBackupStorage()
  const testStorage = useTestBackupStorage()
  const [form, setForm] = useState<CreateBackupStorageBody>(emptyForm)
  const [showForm, setShowForm] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const [testResults, setTestResults] = useState<Record<number, BackupStorageTestResult>>({})

  const set = (k: keyof CreateBackupStorageBody, v: string | boolean) => setForm((f) => ({ ...f, [k]: v }))

  // 凭证须为 ${ENV_VAR} 形式（config-files.md），名称必填（FR-072）。
  const errors = validateFields(
    { name: form.name, accessKeyEnv: form.accessKeyEnv ?? '', secretKeyEnv: form.secretKeyEnv ?? '' },
    {
      name: [validateRequired],
      accessKeyEnv: [validateEnvRef],
      secretKeyEnv: [validateEnvRef],
    },
  )

  const endpointHint = () =>
    form.type === 's3' ? t('backupStorages.endpointHintS3', 'S3 endpoint')
      : form.type === 'sftp' ? t('backupStorages.endpointHintSftp', 'SFTP 主机')
        : t('backupStorages.endpointHintWebdav', 'WebDAV 基地址')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    try {
      await create.mutateAsync(form)
      toast.success(t('backupStorages.create', '创建'))
      setForm(emptyForm)
      setShowForm(false)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('backupStorages.createFailed', '创建存储后端失败'))
    }
  }

  const handleDelete = (id: number) => {
    del.mutate(id, {
      onSuccess: () => toast.success(t('common.deleted', '已删除')),
      onError: (err: unknown) => {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('backupStorages.deleteFailed', '删除失败'))
      },
    })
    setDeleteTarget(null)
  }

  const handleTest = async (id: number) => {
    try {
      const { result } = await testStorage.mutateAsync(id)
      setTestResults((prev) => ({ ...prev, [id]: result }))
      if (result.ok) {
        toast.success(t('backupStorages.testSuccess', '连接成功'))
      } else {
        toast.error(result.message || t('backupStorages.testFailed', '连接失败'))
      }
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('backupStorages.testFailed', '连接失败'))
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div>
          <h1 className="text-2xl font-bold">{t('backupStorages.title', '备份存储后端')}</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">{t('backupStorages.subtitle', '')}</p>
        </div>
        <button
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
          onClick={() => { setForm(emptyForm); setShowForm(true) }}
        >
          {t('backupStorages.add', '新增存储后端')}
        </button>
      </div>

      <Dialog open={showForm} onOpenChange={(o) => setShowForm(o)}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
          <DialogHeader>
            <DialogTitle>{t('backupStorages.add', '新增存储后端')}</DialogTitle>
          </DialogHeader>
          <form id="backup-storage-form" onSubmit={submit}>
            <ScrollableDialogBody className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('backupStorages.name', '名称')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.name}
                  aria-invalid={!!errors.name}
                  onChange={(e) => set('name', e.target.value)} />
                <FieldError error={errors.name} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel>{t('backupStorages.type', '类型')}</FieldLabel>
                <Combobox options={TYPE_OPTIONS} value={form.type} onChange={(v) => set('type', v)} allowCustom={false} />
              </div>
              <label className="flex flex-col gap-1 text-sm md:col-span-2">
                {t('backupStorages.endpoint', 'Endpoint')}
                <input className="p-2 border rounded bg-background" placeholder={endpointHint()} value={form.endpoint}
                  onChange={(e) => set('endpoint', e.target.value)} />
              </label>
              {form.type === 's3' && (
                <>
                  <label className="flex flex-col gap-1 text-sm">
                    {t('backupStorages.bucket', 'Bucket')}
                    <input className="p-2 border rounded bg-background" value={form.bucket}
                      onChange={(e) => set('bucket', e.target.value)} />
                  </label>
                  <label className="flex flex-col gap-1 text-sm">
                    {t('backupStorages.region', 'Region')}
                    <input className="p-2 border rounded bg-background" placeholder="us-east-1" value={form.region}
                      onChange={(e) => set('region', e.target.value)} />
                  </label>
                </>
              )}
              <label className="flex flex-col gap-1 text-sm">
                {t('backupStorages.prefix', '前缀')}
                <input className="p-2 border rounded bg-background" value={form.prefix}
                  onChange={(e) => set('prefix', e.target.value)} />
              </label>
              {form.type === 's3' && (
                <label className="flex items-center gap-2 text-sm mt-6">
                  <Checkbox checked={form.useSsl}
                    onCheckedChange={(v) => set('useSsl', v === true)} aria-label={t('backupStorages.useSsl', '启用 TLS')} />
                  {t('backupStorages.useSsl', '启用 TLS')}
                </label>
              )}
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel>{t('backupStorages.accessKeyEnv', 'Access Key 环境变量')}</FieldLabel>
                <input className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive" placeholder={t('backupStorages.credentialHint', '')}
                  aria-invalid={!!errors.accessKeyEnv}
                  value={form.accessKeyEnv} onChange={(e) => set('accessKeyEnv', e.target.value)} />
                <FieldError error={errors.accessKeyEnv} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel>{t('backupStorages.secretKeyEnv', 'Secret Key 环境变量')}</FieldLabel>
                <input className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive" placeholder={t('backupStorages.credentialHint', '')}
                  aria-invalid={!!errors.secretKeyEnv}
                  value={form.secretKeyEnv} onChange={(e) => set('secretKeyEnv', e.target.value)} />
                <FieldError error={errors.secretKeyEnv} />
              </div>
            </ScrollableDialogBody>
          </form>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setShowForm(false)}>
              {t('backupStorages.cancel', '取消')}
            </Button>
            <Button type="submit" form="backup-storage-form" disabled={create.isPending || hasErrors(errors)}>
              {t('backupStorages.create', '创建')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('backupStorages.name', '名称')}</TableHead>
              <TableHead>{t('backupStorages.type', '类型')}</TableHead>
              <TableHead>{t('backupStorages.endpoint', 'Endpoint')}</TableHead>
              <TableHead>{t('backupStorages.prefix', '前缀')}</TableHead>
              <TableHead>{t('backupStorages.accessKeyEnv', 'Access Key 环境变量')}</TableHead>
              <TableHead className="text-right">{t('backupStorages.backupCount', '备份数')}</TableHead>
              <TableHead className="text-right">{t('backupStorages.usedSpace', '已用空间')}</TableHead>
              <TableHead className="text-right">{t('backupStorages.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(storages ?? []).map((s: BackupStorage) => {
              const result = testResults[s.id]
              const testing = testStorage.isPending && testStorage.variables === s.id
              return (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell><Badge variant="outline">{s.type.toUpperCase()}</Badge></TableCell>
                  <TableCell className="font-mono text-xs">{s.endpoint}{s.bucket ? ` / ${s.bucket}` : ''}</TableCell>
                  <TableCell>{s.prefix || '-'}</TableCell>
                  <TableCell className="font-mono text-xs">{s.accessKeyEnv || '-'}</TableCell>
                  <TableCell className="text-right tabular-nums">{s.backupCount ?? 0}</TableCell>
                  <TableCell className="text-right tabular-nums">{formatBytes(s.usedBytes)}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center justify-end gap-2">
                      {result && (
                        <span className={result.ok ? 'text-xs text-status-success' : 'text-xs text-status-danger'}>
                          {result.ok
                            ? t('backupStorages.testOkWithLatency', '连通 {{latency}} ms', { latency: result.latencyMs })
                            : result.message}
                        </span>
                      )}
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={testing}
                        onClick={() => handleTest(s.id)}
                      >
                        {testing ? t('backupStorages.testing', '测试中…') : t('backupStorages.testConnection', '测试连接')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-status-danger hover:text-status-danger"
                        onClick={() => setDeleteTarget(s.id)}
                      >
                        {t('common.delete', '删除')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
            {(!storages || storages.length === 0) && !isLoading && (
              <TableRow>
                <TableCell colSpan={8} className="h-16 text-center text-muted-foreground">{t('backupStorages.empty', '暂无存储后端')}</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backupStorages.deleteConfirm', '确定删除此存储后端？')}
        scope="platform"
        confirmLabel={t('common.delete', '删除')}
        onConfirm={() => { if (deleteTarget) handleDelete(deleteTarget) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
