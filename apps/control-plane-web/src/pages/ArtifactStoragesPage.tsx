import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useArtifactStorages,
  useCreateArtifactStorage,
  useUpdateArtifactStorage,
  useDeleteArtifactStorage,
  useActivateArtifactStorage,
  useTestArtifactStorage,
  useTestArtifactStorageDraft,
  type ArtifactStorageChannel,
  type ArtifactStorageTestResult,
  type SaveArtifactStorageBody,
} from '@/api/artifactStorages'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'
import DangerConfirm from '@/components/DangerConfirm'

/** 表单态：面板仅可创建 s3 渠道（local 由内置「本机存储」独占，ADR-073 决策 2）。 */
const emptyForm: SaveArtifactStorageBody = {
  name: '', type: 's3', endpoint: '', bucket: '', region: '', prefix: '',
  accessKey: '', secretKey: '', useSsl: false, presignTtlSeconds: 600,
}

/**
 * 文件存储配置页（FR-347，见 ADR-073）：客户端分发制品的外置对象存储渠道管理。
 * 活跃渠道 = 新上传制品落点（存量制品按各自记录读取）；凭证直填、后端可逆加密，
 * 编辑不回显明文（留空 = 保留）。仅平台管理员可访问。
 */
export default function ArtifactStoragesPage() {
  const { t } = useTranslation()
  const { data: channels, isLoading } = useArtifactStorages()
  const create = useCreateArtifactStorage()
  const update = useUpdateArtifactStorage()
  const del = useDeleteArtifactStorage()
  const activate = useActivateArtifactStorage()
  const testSaved = useTestArtifactStorage()
  const testDraft = useTestArtifactStorageDraft()
  const [form, setForm] = useState<SaveArtifactStorageBody>(emptyForm)
  const [draftTestResult, setDraftTestResult] = useState<ArtifactStorageTestResult | null>(null)
  const [showForm, setShowForm] = useState(false)
  /** 编辑目标；null = 创建模式。 */
  const [editing, setEditing] = useState<ArtifactStorageChannel | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<ArtifactStorageChannel | null>(null)
  /** 设活跃确认目标（影响后续上传落点，需确认语义）。 */
  const [activateTarget, setActivateTarget] = useState<ArtifactStorageChannel | null>(null)
  const gate = useFieldGate()

  const set = (k: keyof SaveArtifactStorageBody, v: string | boolean | number) => {
    setDraftTestResult(null)
    setForm((f) => ({ ...f, [k]: v }))
  }

  const errors = validateFields(
    { name: form.name, endpoint: form.endpoint ?? '', bucket: form.bucket ?? '' },
    {
      name: [validateRequired],
      endpoint: [validateRequired],
      bucket: [validateRequired],
    },
  )

  /** 编辑入口：回显非敏字段；凭证永不回显（hasSecretKey 时占位提示「留空保留」）。 */
  const openEdit = (ch: ArtifactStorageChannel) => {
    setForm({
      name: ch.name, type: ch.type, endpoint: ch.endpoint, bucket: ch.bucket, region: ch.region,
      prefix: ch.prefix, accessKey: '', secretKey: '', useSsl: ch.useSsl,
      presignTtlSeconds: ch.presignTtlSeconds,
    })
    setEditing(ch)
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
        toast.success(t('artifactStorages.updated', '已更新'))
      } else {
        await create.mutateAsync(form)
        toast.success(t('artifactStorages.created', '渠道已创建'))
      }
      setForm(emptyForm)
      setEditing(null)
      gate.reset()
      setShowForm(false)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || (editing
        ? t('artifactStorages.updateFailed', '更新渠道失败')
        : t('artifactStorages.createFailed', '创建渠道失败')))
    }
  }

  const handleDelete = (id: number) => {
    del.mutate(id, {
      onSuccess: () => toast.success(t('common.deleted', '已删除')),
      // 删除守卫命中（内置/活跃/被制品引用）→ 用后端 message 呈现准确原因。
      onError: (err: unknown) => {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('artifactStorages.deleteFailed', '删除失败'))
      },
    })
    setDeleteTarget(null)
  }

  const handleActivate = (id: number) => {
    activate.mutate(id, {
      onSuccess: () => toast.success(t('artifactStorages.activated', '已设为活跃渠道，后续上传将落此渠道')),
      onError: (err: unknown) => {
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('artifactStorages.activateFailed', '设活跃失败'))
      },
    })
    setActivateTarget(null)
  }

  const handleTestDraft = () => {
    if (hasErrors(errors)) return
    // 编辑态凭证留空：带 id 让后端复用存库凭证探测。
    testDraft.mutate({ ...form, id: editing?.id }, {
      onSuccess: (result) => {
        setDraftTestResult(result)
        if (result.ok) toast.success(result.message)
        else toast.error(result.message)
      },
      onError: () => {
        const message = t('artifactStorages.testFailed', '测试连接失败')
        setDraftTestResult({ ok: false, message, latencyMs: 0 })
        toast.error(message)
      },
    })
  }

  const handleTest = (id: number) => {
    testSaved.mutate(id, {
      onSuccess: (result) => {
        if (result.ok) toast.success(result.message)
        else toast.error(result.message)
      },
      onError: () => toast.error(t('artifactStorages.testFailed', '测试连接失败')),
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div>
          <h1 className="text-2xl font-bold">{t('artifactStorages.title', '文件存储配置')}</h1>
          <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
            {t('artifactStorages.subtitle', '配置客户端分发制品的存储渠道。活跃渠道决定新上传制品的落点；S3 兼容渠道（rustfs / MinIO 等）由对象存储直接分发下载流量，主控不中继大文件。')}
          </p>
        </div>
        <button
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
          onClick={() => { setForm(emptyForm); setEditing(null); setDraftTestResult(null); gate.reset(); setShowForm(true) }}
        >
          {t('artifactStorages.add', '新增 S3 渠道')}
        </button>
      </div>

      <Dialog open={showForm} onOpenChange={(o) => { setShowForm(o); if (!o) { setDraftTestResult(null); setEditing(null) } }}>
        <DialogContent className={`${scrollableDialogContentClass} sm:max-w-2xl`}>
          <DialogHeader>
            <DialogTitle>
              {editing ? t('artifactStorages.edit', '编辑存储渠道') : t('artifactStorages.add', '新增 S3 渠道')}
            </DialogTitle>
          </DialogHeader>
          <form id="artifact-storage-form" onSubmit={submit}>
            <ScrollableDialogBody className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('artifactStorages.name', '名称')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.name}
                  aria-invalid={!!gate.show('name', errors.name)}
                  onChange={(e) => set('name', e.target.value)}
                  onBlur={() => gate.touch('name')} />
                <FieldError error={gate.show('name', errors.name)} />
              </div>
              <div className="flex flex-col gap-1 text-sm">
                <FieldLabel required>{t('artifactStorages.bucket', 'Bucket')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive" value={form.bucket}
                  aria-invalid={!!gate.show('bucket', errors.bucket)}
                  onChange={(e) => set('bucket', e.target.value)}
                  onBlur={() => gate.touch('bucket')} />
                <FieldError error={gate.show('bucket', errors.bucket)} />
              </div>
              <div className="flex flex-col gap-1 text-sm md:col-span-2">
                <FieldLabel required>{t('artifactStorages.endpoint', 'Endpoint')}</FieldLabel>
                <input className="p-2 border rounded bg-background aria-invalid:border-destructive"
                  placeholder={t('artifactStorages.endpointHint', '如 rustfs.example.com:9000（内网 http 常态；协议由「启用 TLS」决定）')}
                  aria-invalid={!!gate.show('endpoint', errors.endpoint)}
                  value={form.endpoint} onChange={(e) => set('endpoint', e.target.value)}
                  onBlur={() => gate.touch('endpoint')} />
                <FieldError error={gate.show('endpoint', errors.endpoint)} />
              </div>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.region', 'Region')}
                <input className="p-2 border rounded bg-background" placeholder="us-east-1" value={form.region}
                  onChange={(e) => set('region', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.prefix', '对象键前缀')}
                <input className="p-2 border rounded bg-background" value={form.prefix}
                  onChange={(e) => set('prefix', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.presignTtl', '预签名有效期（秒）')}
                <input type="number" min={60} max={3600} className="p-2 border rounded bg-background"
                  value={form.presignTtlSeconds ?? 600}
                  onChange={(e) => set('presignTtlSeconds', Number(e.target.value))} />
                <span className="text-xs text-muted-foreground">{t('artifactStorages.presignTtlHint', '玩家下载跳转链接的有效时长，60~3600 秒')}</span>
              </label>
              <label className="flex items-center gap-2 text-sm mt-6">
                <Checkbox checked={form.useSsl}
                  onCheckedChange={(v) => set('useSsl', v === true)} aria-label={t('artifactStorages.useSsl', '启用 TLS')} />
                {t('artifactStorages.useSsl', '启用 TLS')}
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.accessKey', 'Access Key')}
                <input className="p-2 border rounded bg-background font-mono"
                  placeholder={editing?.hasAccessKey ? t('artifactStorages.keyKeepHint', '已配置，留空保留') : ''}
                  value={form.accessKey} onChange={(e) => set('accessKey', e.target.value)} />
              </label>
              <label className="flex flex-col gap-1 text-sm">
                {t('artifactStorages.secretKey', 'Secret Key')}
                {/* 编辑不回显明文（SK 脱敏）：留空 = 保留原值，填入 = 覆盖。 */}
                <input type="password" autoComplete="new-password" className="p-2 border rounded bg-background font-mono"
                  placeholder={editing?.hasSecretKey ? t('artifactStorages.keyKeepHint', '已配置，留空保留') : ''}
                  value={form.secretKey} onChange={(e) => set('secretKey', e.target.value)} />
              </label>
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
              {t('common.cancel', '取消')}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={handleTestDraft}
              disabled={testDraft.isPending || hasErrors(errors)}
            >
              {t('artifactStorages.testConnection', '测试连接')}
            </Button>
            <Button type="submit" form="artifact-storage-form" disabled={create.isPending || update.isPending || hasErrors(errors)}>
              {editing ? t('common.save', '保存') : t('artifactStorages.create', '创建')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('artifactStorages.name', '名称')}</TableHead>
              <TableHead>{t('artifactStorages.type', '类型')}</TableHead>
              <TableHead>{t('artifactStorages.endpoint', 'Endpoint')}</TableHead>
              <TableHead>{t('artifactStorages.presignTtlShort', '签名时效')}</TableHead>
              <TableHead>{t('artifactStorages.lastTest', '最近测试')}</TableHead>
              <TableHead className="text-right">{t('artifactStorages.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(channels ?? []).map((ch: ArtifactStorageChannel) => (
              <TableRow key={ch.id}>
                <TableCell className="font-medium">
                  <span className="inline-flex items-center gap-2">
                    {ch.name}
                    {ch.builtin && <Badge variant="outline">{t('artifactStorages.builtin', '内置')}</Badge>}
                    {ch.active && <Badge>{t('artifactStorages.active', '活跃')}</Badge>}
                  </span>
                </TableCell>
                <TableCell><Badge variant="outline">{ch.type.toUpperCase()}</Badge></TableCell>
                <TableCell className="font-mono text-xs">
                  {ch.type === 's3' ? `${ch.endpoint} / ${ch.bucket}${ch.prefix ? ` / ${ch.prefix}` : ''}` : t('artifactStorages.localEndpoint', '主控数据根')}
                </TableCell>
                <TableCell className="text-xs">{ch.type === 's3' ? `${ch.presignTtlSeconds}s` : '-'}</TableCell>
                <TableCell className="text-xs">
                  {ch.lastTestAt ? (
                    <span className={ch.lastTestOk ? 'text-status-success' : 'text-status-danger'}>
                      {ch.lastTestMessage || (ch.lastTestOk ? t('artifactStorages.testOk', '连接正常') : t('artifactStorages.testFailed', '测试连接失败'))}
                    </span>
                  ) : '-'}
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => handleTest(ch.id)}
                    disabled={testSaved.isPending && testSaved.variables === ch.id}
                  >
                    {t('artifactStorages.test', '测试')}
                  </Button>
                  {!ch.active && (
                    <Button variant="ghost" size="xs" onClick={() => setActivateTarget(ch)}>
                      {t('artifactStorages.setActive', '设活跃')}
                    </Button>
                  )}
                  {/* 内置行不可编辑/删除；活跃行禁删（先切走活跃）。 */}
                  {!ch.builtin && (
                    <>
                      <Button variant="ghost" size="xs" onClick={() => openEdit(ch)}>
                        {t('common.edit', '编辑')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-status-danger hover:text-status-danger"
                        disabled={ch.active}
                        onClick={() => setDeleteTarget(ch)}
                      >
                        {t('common.delete', '删除')}
                      </Button>
                    </>
                  )}
                </TableCell>
              </TableRow>
            ))}
            {(!channels || channels.length === 0) && !isLoading && (
              <TableRow>
                <TableCell colSpan={6} className="h-16 text-center text-muted-foreground">{t('artifactStorages.empty', '暂无存储渠道')}</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      {/* 设活跃确认：影响后续上传落点（存量制品不迁移、按原渠道读取）。 */}
      <Dialog open={activateTarget !== null} onOpenChange={(o) => { if (!o) setActivateTarget(null) }}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t('artifactStorages.activateConfirmTitle', '切换活跃存储渠道？')}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t('artifactStorages.activateConfirmDesc', '之后新上传的客户端分发制品将落入「{{name}}」；已上传的制品保持原位置、读取不受影响。', { name: activateTarget?.name ?? '' })}
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setActivateTarget(null)}>
              {t('common.cancel', '取消')}
            </Button>
            <Button type="button" onClick={() => { if (activateTarget) handleActivate(activateTarget.id) }} disabled={activate.isPending}>
              {t('artifactStorages.setActive', '设活跃')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('artifactStorages.deleteConfirm', '确定删除此存储渠道？')}
        scope="platform"
        confirmLabel={t('common.delete', '删除')}
        onConfirm={() => { if (deleteTarget) handleDelete(deleteTarget.id) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
