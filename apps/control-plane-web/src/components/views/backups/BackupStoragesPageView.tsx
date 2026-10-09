/**
 * @file BackupStoragesPageView：备份远程存储后端页的受控视图，列表取数、四个写动作与 toast 由应用容器负责。
 * @input lib/form-validation（字段校验）、lib/use-field-gate（错误展示时机门控）、Table/Dialog/Combobox
 *        等原语、views/DangerConfirm（删除二次确认）、翻译上下文
 * @output BackupStoragesPageView、BackupStoragesPageViewProps、BackupStorageRow、BackupStorageForm、BackupStorageTestOutcome
 * @sync apps/control-plane-web/src/pages/BackupStoragesPage.tsx、apps/control-plane-web/src/pages/BackupStoragesPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-057 存储后端管理 + FR-338 编辑 + FR-072 表单校验）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Database, Pencil, Trash2, Zap } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
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
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { validateRequired, validateEnvRef, validateFields, hasErrors } from '@/lib/shared/form-validation'
import { useFieldGate } from '@/lib/hooks/use-field-gate'
import { formatFileSize } from '@/lib/shared/format-file-size'

/** 可选存储类型（local 由内置「本机存储」独占，此页只管远程后端）。 */
const TYPES = ['s3', 'sftp', 'webdav'] as const
const TYPE_OPTIONS: ComboboxOption[] = TYPES.map((tp) => ({ value: tp, label: tp.toUpperCase() }))

/**
 * 备份存储后端行（本视图渲染与表单回填所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/backupStorages` 的 `BackupStorage`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `ArtifactVersionsPageView`、`NodeRepairPanel` 的取舍）。
 *
 * 注意 `accessKeyEnv` / `secretKeyEnv` 是 `${ENV_VAR}` **引用**而非明文——后端从不返回明文，
 * 视图只回显这串引用，故不存在打码问题；视图任何位置都不得展示解密后的凭据。
 */
export interface BackupStorageRow {
  id: number
  name: string
  /** local | s3 | sftp | webdav */
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  /** Access Key 的环境变量引用，如 `${JIANMANAGER_BACKUP_S3_AK}`。 */
  accessKeyEnv: string
  /** Secret Key 的环境变量引用。 */
  secretKeyEnv: string
  useSsl: boolean
  /** 最近一次测试时间；空表示从未测试。 */
  lastTestAt?: string
  lastTestOk: boolean
  lastTestMessage: string
  /** 已完成备份份数（后端聚合，不落库存储记录）。 */
  backupCount: number
  /** 已完成备份占用字节数（后端聚合）。 */
  usedBytes: number
}

/**
 * 表单草稿（创建与编辑同形）。字段全部必填以贴合运行时取值——
 * 编辑时由行值整体回填，创建时由 `emptyForm` 铺满，故永不出现 undefined 受控/非受控切换。
 */
export interface BackupStorageForm {
  name: string
  type: string
  endpoint: string
  bucket: string
  region: string
  prefix: string
  accessKeyEnv: string
  secretKeyEnv: string
  useSsl: boolean
}

/** 草稿测试连接的结果（视图只需 ok 与 message 做内联回显，toast 文案归容器）。 */
export interface BackupStorageTestOutcome {
  ok: boolean
  message: string
}

/** 新建时的空表单（useSsl 默认开）。 */
const emptyForm: BackupStorageForm = {
  name: '', type: 's3', endpoint: '', bucket: '', region: '', prefix: '',
  accessKeyEnv: '', secretKeyEnv: '', useSsl: true,
}

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 列表与加载态经 props 注入（容器调 `useBackupStorages()`）；
 * - 四个写动作以回调上报，由容器执行 mutation 并决定成功/失败文案：提交与草稿测试回传
 *   `Promise`，视图据返回值决定是否复位本地编辑态（失败时保留草稿与窗口便于重试）；
 * - 对话框开合、表单草稿、编辑目标、字段门控（touched/submitted）是纯 UI 状态，留在本组件内
 *   （编辑目标不触发重新取数，取数由容器的 mutation 成功钩子失效缓存驱动）；
 * - 行内测试按钮的禁用只认容器注入的 `rowTestingId`——只有容器知道 mutation 在测哪一行。
 */
export interface BackupStoragesPageViewProps {
  /** 存储后端列表；外壳取数后注入（缺省或空数组渲染空态）。 */
  storages?: BackupStorageRow[]
  /** 列表加载态（渲染骨架行）。 */
  isLoading?: boolean
  /** 创建/更新在途：禁用弹窗提交按钮。 */
  saving?: boolean
  /** 草稿测试连接在途：禁用「测试连接」按钮。 */
  testing?: boolean
  /** 行内测试在途的目标 id：只禁用该行的测试按钮，其余行不受影响。 */
  rowTestingId?: number | null
  /** 提交表单；`editingId` 为 null 表示创建。返回是否成功——成功才关窗并清空草稿。 */
  onSubmit: (values: BackupStorageForm, editingId: number | null) => Promise<boolean>
  /** 测试未保存的草稿（不落库）；回传结果供弹窗内联回显。 */
  onTestDraft: (values: BackupStorageForm) => Promise<BackupStorageTestOutcome>
  /** 测试已保存的后端（结果提示由容器负责）。 */
  onTest: (id: number) => void
  /** 删除已保存的后端（二次确认已在本视图内完成）。 */
  onDelete: (id: number) => void
}

/**
 * 备份远程存储后端管理页（FR-057，编辑=FR-338）。
 * 凭证以 ${ENV_VAR} 形式引用环境变量，不收明文（config-files.md）；仅平台管理员可访问。
 * 弹窗 create/edit 双模式：编辑受控回显现值（凭证即 ${VAR} 引用，非明文），type 不可改。
 * 表格外观为方案 A「精工卡片」（opt-in appearance="refined"）。
 */
export function BackupStoragesPageView({
  storages,
  isLoading = false,
  saving = false,
  testing = false,
  rowTestingId = null,
  onSubmit,
  onTestDraft,
  onTest,
  onDelete,
}: BackupStoragesPageViewProps) {
  const { t } = useTranslation()
  const [form, setForm] = useState<BackupStorageForm>(emptyForm)
  const [draftTestResult, setDraftTestResult] = useState<BackupStorageTestOutcome | null>(null)
  const [showForm, setShowForm] = useState(false)
  /** 编辑目标；null = 创建模式（FR-338）。 */
  const [editing, setEditing] = useState<BackupStorageRow | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const gate = useFieldGate()

  const set = (k: keyof BackupStorageForm, v: string | boolean) => {
    // 任何键入都作废上一次草稿测试结论：结论只对应它那一刻的输入。
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
  const openEdit = (s: BackupStorageRow) => {
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
    // 失败（false，容器已弹 toast）时不关窗、不清草稿，便于修正后重试。
    const ok = await onSubmit(form, editing ? editing.id : null)
    if (!ok) return
    setForm(emptyForm)
    setEditing(null)
    gate.reset()
    setShowForm(false)
  }

  const handleTestDraft = async () => {
    if (hasErrors(errors)) return
    // 成功/失败原因都由容器落成结果对象回传，本视图只负责内联展示。
    setDraftTestResult(await onTestDraft(form))
  }

  const totalUsed = (storages ?? []).reduce((sum, s) => sum + Number(s.usedBytes ?? 0), 0)

  return (
    // 全量对齐：标题上移到页面级 PageHeader（页名只出现一次、且在页面最上面），
    // 卡片头只留计数与操作。原先也没有 data-page，迁移时补上。
    <PageShell data-page="backup-storages">
      <PageHeader title={t('backupStorages.title', '备份存储后端')} />
      <TableCard>
        <TableCardHeader
          count={t('backupStorages.cardCount', { total: (storages ?? []).length })}
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
            {(storages ?? []).map((s) => {
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
                    {formatFileSize(s.usedBytes)} · {t('backupStorages.backupCount', '{{count}} 个备份', { count: s.backupCount })}
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
                        onClick={() => onTest(s.id)}
                        disabled={rowTestingId === s.id}
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
          {t('backupStorages.cardSummary', { total: (storages ?? []).length, used: formatFileSize(totalUsed) })}
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
              disabled={testing || hasErrors(errors)}
            >
              {t('backupStorages.testConnection', '测试连接')}
            </Button>
            <Button type="submit" form="backup-storage-form" disabled={saving || hasErrors(errors)}>
              {editing ? t('common.save', '保存') : t('backupStorages.create', '创建')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除二次确认：scope 固定 platform，角色门禁由 DangerConfirm 自行读登录态判定。 */}
      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backupStorages.deleteConfirm', '确定删除此存储后端？')}
        scope="platform"
        confirmLabel={t('common.delete', '删除')}
        onConfirm={() => {
          if (deleteTarget === null) return
          const id = deleteTarget
          setDeleteTarget(null)
          onDelete(id)
        }}
        onCancel={() => setDeleteTarget(null)}
      />
    </PageShell>
  )
}

export default BackupStoragesPageView
