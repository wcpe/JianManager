import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useBackupStorages,
  useCreateBackupStorage,
  useUpdateBackupStorage,
  useDeleteBackupStorage,
  useTestBackupStorage,
  useTestBackupStorageDraft,
  type BackupStorage,
  type BackupStorageTestResult,
  type CreateBackupStorageBody,
} from '@/api/backupStorages'
import { Database, Pencil, Trash2, Zap } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCard,
  TableCardFooter,
  TableCardHeader,
  TableCell,
  TableEmptyRow,
  TableHead,
  TableHeader,
  TableIconButton,
  TableRow,
  TableSkeletonRows,
} from '@jianmanager/ui/components/table'
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
import { useFieldGate } from '@/lib/use-field-gate'
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
 * 备份远程存储后端管理页（FR-057，编辑=FR-338）。
 * 凭证以 ${ENV_VAR} 形式引用环境变量，不收明文（config-files.md）；仅平台管理员可访问。
 * 弹窗 create/edit 双模式：编辑受控回显现值（凭证即 ${VAR} 引用，非明文），type 不可改。
 * 表格外观为方案 A「精工卡片」（opt-in appearance="refined"）。
 */
export default function BackupStoragesPage() {
  const { t } = useTranslation()
  const { data: storages, isLoading } = useBackupStorages()
  const create = useCreateBackupStorage()
  const update = useUpdateBackupStorage()
  const del = useDeleteBackupStorage()
  const testStorage = useTestBackupStorage()
  const testDraft = useTestBackupStorageDraft()
  const [form, setForm] = useState<CreateBackupStorageBody>(emptyForm)
  const [draftTestResult, setDraftTestResult] = useState<BackupStorageTestResult | null>(null)
  const [showForm, setShowForm] = useState(false)
  /** 编辑目标；null = 创建模式（FR-338）。 */
  const [editing, setEditing] = useState<BackupStorage | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const gate = useFieldGate()

  const set = (k: keyof CreateBackupStorageBody, v: string | boolean) => {
    setDraftTestResult(null)
    setForm((f) => ({ ...f, [k]: v }))
  }

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

  /** 编辑入口：行值受控填入表单（凭证字段即 ${VAR} 引用，原样回显无泄露，FR-338）。 */
  const openEdit = (s: BackupStorage) => {
    setForm({
      name: s.name, type: s.type, endpoint: s.endpoint, bucket: s.bucket, region: s.region,
      prefix: s.prefix, accessKeyEnv: s.accessKeyEnv, secretKeyEnv: s.secretKeyEnv, useSsl: s.useSsl,
    })
    setEditing(s)
    setDraftTestResult(null)
    gate.reset()
    setShowForm(true)
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    try {
      if (editing) {
        await update.mutateAsync({ id: editing.id, ...form })
        toast.success(t('backupStorages.updated', '已更新'))
      } else {
        await create.mutateAsync(form)
        toast.success(t('backupStorages.create', '创建'))
      }
      setForm(emptyForm)
      setEditing(null)
      gate.reset()
      setShowForm(false)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || (editing
        ? t('backupStorages.updateFailed', '更新存储后端失败')
        : t('backupStorages.createFailed', '创建存储后端失败')))
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

  const handleTestDraft = () => {
    if (hasErrors(errors)) return
    testDraft.mutate(form, {
      onSuccess: (result) => {
        setDraftTestResult(result)
        if (result.ok) toast.success(result.message)
        else toast.error(result.message)
      },
      onError: () => {
        const message = t('backupStorages.testFailed', '测试连接失败')
        setDraftTestResult({ ok: false, message })
        toast.error(message)
      },
    })
  }

  const handleTest = (id: number) => {
    testStorage.mutate(id, {
      onSuccess: (result) => {
        if (result.ok) toast.success(result.message)
        else toast.error(result.message)
      },
      onError: () => toast.error(t('backupStorages.testFailed', '测试连接失败')),
    })
  }

  const totalUsed = (storages ?? []).reduce((sum, s) => sum + Number(s.usedBytes ?? 0), 0)

  return (
    <div className="space-y-6">
      {/* 方案 A「精工卡片」：页面标题 + 功能说明 + 主操作全部收进卡片头。 */}
      <TableCard>
        <TableCardHeader
          title={t('backupStorages.title', '备份存储后端')}
          count={t('backupStorages.cardCount', { total: (storages ?? []).length })}
          description={t('backupStorages.subtitle', '')}
          actions={
            <Button
              onClick={() => { setForm(emptyForm); setEditing(null); setDraftTestResult(null); gate.reset(); setShowForm(true) }}
            >
              {t('backupStorages.add', '新增存储后端')}
            </Button>
          }
        />

        <Table appearance="refined">
          <TableHeader>
            <TableRow>
              <TableHead>{t('backupStorages.name', '名称')}</TableHead>
              <TableHead>{t('backupStorages.type', '类型')}</TableHead>
              <TableHead>{t('backupStorages.endpoint', 'Endpoint')}</TableHead>
              <TableHead>{t('backupStorages.prefix', '前缀')}</TableHead>
              <TableHead align="right">{t('backupStorages.capacity', '容量')}</TableHead>
              <TableHead>{t('backupStorages.lastTest', '最近测试')}</TableHead>
              <TableHead>{t('backupStorages.accessKeyEnv', 'Access Key 环境变量')}</TableHead>
              <TableHead align="right">{t('backupStorages.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(storages ?? []).map((s: BackupStorage) => {
              // 最近测试文案：优先后端 message，回落到「连接正常 / 测试失败」。
              const lastTestText =
                s.lastTestMessage ||
                (s.lastTestOk ? t('backupStorages.testOk', '连接正常') : t('backupStorages.testFailed', '测试失败'))
              return (
                <TableRow key={s.id}>
                  <TableCell className="font-medium text-foreground">{s.name}</TableCell>
                  <TableCell><Badge variant="chip-neutral">{s.type.toUpperCase()}</Badge></TableCell>
                  <TableCell className="font-mono text-xs">
                    <span className="block max-w-[18rem] truncate" title={s.bucket ? `${s.endpoint} / ${s.bucket}` : s.endpoint}>
                      {s.endpoint}{s.bucket ? ` / ${s.bucket}` : ''}
                    </span>
                  </TableCell>
                  <TableCell>
                    <span className="block max-w-[10rem] truncate" title={s.prefix || undefined}>{s.prefix || '-'}</span>
                  </TableCell>
                  <TableCell align="right" className="text-xs tabular-nums">
                    {formatBytes(s.usedBytes)} · {t('backupStorages.backupCount', '{{count}} 个备份', { count: s.backupCount })}
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.lastTestAt ? (
                      <Badge
                        variant={s.lastTestOk ? 'chip-ok' : 'chip-bad'}
                        title={lastTestText}
                        className="max-w-[14rem]"
                      >
                        <span className="min-w-0 truncate">{lastTestText}</span>
                      </Badge>
                    ) : (
                      <Badge variant="chip-idle">{t('backupStorages.testNever', '未测试')}</Badge>
                    )}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    <span className="block max-w-[14rem] truncate" title={s.accessKeyEnv || undefined}>{s.accessKeyEnv || '-'}</span>
                  </TableCell>
                  {/* 3 个操作 → 图标按钮（方案 A：28px 命中区 / 15px 图标，主操作弱底）；aria-label 保留原文案可达名。 */}
                  <TableCell align="right">
                    <div className="flex items-center justify-end gap-1">
                      <TableIconButton
                        tone="primary"
                        aria-label={t('backupStorages.test', '测试')}
                        title={t('backupStorages.test', '测试')}
                        onClick={() => handleTest(s.id)}
                        disabled={testStorage.isPending && testStorage.variables === s.id}
                      >
                        <Zap />
                      </TableIconButton>
                      <TableIconButton
                        aria-label={t('common.edit', '编辑')}
                        title={t('common.edit', '编辑')}
                        onClick={() => openEdit(s)}
                      >
                        <Pencil />
                      </TableIconButton>
                      <TableIconButton
                        tone="danger"
                        aria-label={t('common.delete', '删除')}
                        title={t('common.delete', '删除')}
                        onClick={() => setDeleteTarget(s.id)}
                      >
                        <Trash2 />
                      </TableIconButton>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
            {isLoading && <TableSkeletonRows rows={3} cols={8} />}
            {(!storages || storages.length === 0) && !isLoading && (
              <TableEmptyRow
                colSpan={8}
                icon={<Database />}
                title={t('backupStorages.empty', '暂无存储后端')}
                description={t('backupStorages.emptyHint')}
              />
            )}
          </TableBody>
        </Table>

        <TableCardFooter>
          {t('backupStorages.cardSummary', { total: (storages ?? []).length, used: formatBytes(totalUsed) })}
        </TableCardFooter>
      </TableCard>

      <Dialog open={showForm} onOpenChange={(o) => { setShowForm(o); if (!o) { setDraftTestResult(null); setEditing(null) } }}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
          <DialogHeader>
            <DialogTitle>
              {editing ? t('backupStorages.edit', '编辑存储后端') : t('backupStorages.add', '新增存储后端')}
            </DialogTitle>
          </DialogHeader>
          {editing && editing.backupCount > 0 && (
            <p className="text-xs rounded-md border border-status-warning/40 bg-status-warning/10 text-status-warning px-3 py-2">
              {t('backupStorages.editInUseHint', '该后端已被备份引用：修改 Endpoint/Bucket/前缀不会迁移已有备份对象，可能影响旧备份的恢复定位。')}
            </p>
          )}
          <form id="backup-storage-form" onSubmit={submit}>
            <ScrollableDialogBody className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('backupStorages.name', '名称')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.name}
                  aria-invalid={!!gate.show('name', errors.name)}
                  onChange={(e) => set('name', e.target.value)}
                  onBlur={() => gate.touch('name')} />
                <FieldError error={gate.show('name', errors.name)} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel>{t('backupStorages.type', '类型')}</FieldLabel>
                {/* 编辑时 type 不可改（改型=删重建，后端 422 双保险，FR-338）。 */}
                <Combobox options={TYPE_OPTIONS} value={form.type} onChange={(v) => set('type', v)} allowCustom={false} disabled={!!editing} />
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
                <input className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive" placeholder={t('backupStorages.accessKeyHint', '')}
                  aria-invalid={!!gate.show('accessKeyEnv', errors.accessKeyEnv)}
                  value={form.accessKeyEnv} onChange={(e) => set('accessKeyEnv', e.target.value)}
                  onBlur={() => gate.touch('accessKeyEnv')} />
                <FieldError error={gate.show('accessKeyEnv', errors.accessKeyEnv)} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel>{t('backupStorages.secretKeyEnv', 'Secret Key 环境变量')}</FieldLabel>
                <input className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive" placeholder={t('backupStorages.secretKeyHint', '')}
                  aria-invalid={!!gate.show('secretKeyEnv', errors.secretKeyEnv)}
                  value={form.secretKeyEnv} onChange={(e) => set('secretKeyEnv', e.target.value)}
                  onBlur={() => gate.touch('secretKeyEnv')} />
                <FieldError error={gate.show('secretKeyEnv', errors.secretKeyEnv)} />
              </div>
            </ScrollableDialogBody>
          </form>
          {draftTestResult && (
            <p
              role="status"
              className={`text-sm ${draftTestResult.ok ? 'text-status-success' : 'text-status-danger'}`}
            >
              {draftTestResult.message}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setShowForm(false)}>
              {t('backupStorages.cancel', '取消')}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={handleTestDraft}
              disabled={testDraft.isPending || hasErrors(errors)}
            >
              {t('backupStorages.testConnection', '测试连接')}
            </Button>
            <Button type="submit" form="backup-storage-form" disabled={create.isPending || update.isPending || hasErrors(errors)}>
              {editing ? t('common.save', '保存') : t('backupStorages.create', '创建')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
