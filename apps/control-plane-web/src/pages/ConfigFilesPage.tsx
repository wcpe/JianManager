import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router'
import { FileCode2, Layers, Pencil, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Input } from '@jianmanager/ui/components/input'
import { ListSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Label } from '@jianmanager/ui/components/label'
import { Textarea } from '@jianmanager/ui/components/textarea'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import {
  CONFIG_FILE_FORMATS,
  useConfigFiles,
  useCreateConfigFile,
  useDeleteConfigFile,
  useUpdateConfigFile,
} from '@/api/configFiles'
import type { ConfigFileFormat, ConfigFileView } from '@/api/configFiles'
import { apiErrorMessage } from '@/lib/shared/api-error'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { formatRelativeTime } from '@/lib/shared/relative-time'

/** 列表每页条数（与后端 `pageSize` 上限 200 无冲突，取常见密度）。 */
const PAGE_SIZE = 20

/** 表单草稿（新建与编辑共用；`name` / `format` 只在新建时可填）。 */
interface FileDraft {
  name: string
  format: ConfigFileFormat
  description: string
  schemaJson: string
}

const EMPTY_DRAFT: FileDraft = { name: '', format: 'yaml', description: '', schemaJson: '' }

/** 
 * 分层配置·配置文件列表页。
 *
 * 职责：分页 + 关键字搜索列出配置逻辑文件（name / format / 层数 / 更新时间），
 * 提供新建、改元数据、软删与进入详情四个入口。
 *
 * 「层」是这套模型的核心计量单位：一条配置可以同时在多个分组与多台实例上各有版本，
 * 列表里的「层数」就是「这份配置被多少处覆盖过」——为 0 表示只有定义还没有任何内容。
 */
export default function ConfigFilesPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  // 键入防抖（落定后才进查询参数，避免每个字符都发一次请求）。
  const debouncedKeyword = useDebounced(keyword, 300)

  const listQuery = useConfigFiles({
    ...(debouncedKeyword ? { q: debouncedKeyword } : {}),
    page,
    pageSize: PAGE_SIZE,
  })
  const createMut = useCreateConfigFile()
  const updateMut = useUpdateConfigFile()
  const deleteMut = useDeleteConfigFile()

  // 新建/编辑共用同一个对话框；`editing` 为 null 表示新建。
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<ConfigFileView | null>(null)
  const [draft, setDraft] = useState<FileDraft>(EMPTY_DRAFT)
  const [deleting, setDeleting] = useState<ConfigFileView | null>(null)

  const files = listQuery.data?.files ?? []
  const total = listQuery.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const openCreate = () => {
    setEditing(null)
    setDraft(EMPTY_DRAFT)
    setDialogOpen(true)
  }

  const openEdit = (file: ConfigFileView) => {
    setEditing(file)
    setDraft({
      name: file.name,
      format: file.format,
      description: file.description ?? '',
      schemaJson: file.schemaJson ?? '',
    })
    setDialogOpen(true)
  }

  const submit = async () => {
    const description = draft.description.trim()
    const schemaJson = draft.schemaJson.trim()
    try {
      if (editing) {
        await updateMut.mutateAsync({
          id: editing.id,
          // description / schemaJson 都**无条件带上**：后端用「字段缺席=不改、显式空串=清空」
          // 的三态语义，若在这里按真值条件展开，空值会被省略 → 用户永远清不掉该字段。
          input: { description, schemaJson },
        })
        toast.success(t('configFiles.metaSaved'))
      } else {
        await createMut.mutateAsync({
          name: draft.name.trim(),
          format: draft.format,
          description,
          ...(schemaJson ? { schemaJson } : {}),
        })
        toast.success(t('configFiles.created'))
      }
      setDialogOpen(false)
    } catch (err) {
      // 后端 ALREADY_EXISTS / INVALID_REQUEST 都有定向文案，原样透出。
      toast.error(apiErrorMessage(err, t('configFiles.saveFailed')))
    }
  }

  const confirmDelete = async () => {
    if (!deleting) return
    try {
      await deleteMut.mutateAsync(deleting.id)
      toast.success(t('configFiles.deleted'))
      setDeleting(null)
    } catch (err) {
      toast.error(apiErrorMessage(err, t('configFiles.deleteFailed')))
    }
  }

  const nameInvalid = !editing && draft.name.trim() === ''

  return (
    <PageShell>
      <PageHeader
        title={t('configFiles.title')}
        count={total}
        description={t('configFiles.desc')}
        actions={
          <>
            <Button variant="outline" size="sm" asChild>
              <Link to="/config-baselines">
                <Layers aria-hidden />
                {t('nav.configBaselines')}
              </Link>
            </Button>
            <Button size="sm" onClick={openCreate} data-testid="config-file-create">
              <Plus aria-hidden />
              {t('configFiles.create')}
            </Button>
          </>
        }
      />

      <Panel
        title={t('configFiles.listTitle')}
        bodyClassName="p-0"
        actions={
          <Input
            value={keyword}
            onChange={(e) => {
              setKeyword(e.target.value)
              // 关键字变化必须回到第 1 页：否则会停在超出新结果集范围的空页上。
              setPage(1)
            }}
            placeholder={t('configFiles.searchPlaceholder')}
            aria-label={t('configFiles.searchPlaceholder')}
            className="h-8 w-56"
            data-testid="config-file-search"
          />
        }
        footer={
          totalPages > 1 ? (
            <PaginationBar
              page={page}
              totalPages={totalPages}
              total={total}
              onChange={setPage}
              totalLabel={t('configFiles.total', { total })}
              pageLabel={t('configFiles.pageOf', { page, totalPages })}
              prevLabel={t('common.prevPage')}
              nextLabel={t('common.nextPage')}
            />
          ) : undefined
        }
      >
        {listQuery.isLoading ? (
          <ListSkeleton rows={6} />
        ) : listQuery.isError ? (
          // 取数失败与「没有任何配置文件」必须区分：后者会让运维以为平台还没建过配置。
          <p className="px-4 py-6 text-sm text-destructive" data-testid="config-file-error">
            {apiErrorMessage(listQuery.error, t('configFiles.loadFailed'))}
          </p>
        ) : files.length === 0 ? (
          <EmptyState
            icon={<FileCode2 aria-hidden />}
            title={debouncedKeyword ? t('configFiles.searchEmpty') : t('configFiles.empty')}
            description={debouncedKeyword ? undefined : t('configFiles.emptyHint')}
            action={
              debouncedKeyword ? undefined : (
                <Button size="sm" onClick={openCreate}>
                  {t('configFiles.create')}
                </Button>
              )
            }
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('configFiles.colName')}</TableHead>
                <TableHead>{t('configFiles.colFormat')}</TableHead>
                <TableHead className="text-right">
                  {/* 「层」是这套分层配置的行话：不解释的话没人知道它指「作用域覆盖层」。 */}
                  <span title={t('configFiles.layersTitle')}>{t('configFiles.colLayers')}</span>
                </TableHead>
                <TableHead>{t('configFiles.colCapabilities')}</TableHead>
                <TableHead>{t('configFiles.colUpdated')}</TableHead>
                <TableHead className="text-right">{t('configFiles.colActions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {files.map((file) => (
                <TableRow key={file.id} data-testid={`config-file-row-${file.id}`}>
                  <TableCell>
                    <Link
                      to={`/config-files/${file.id}`}
                      className="font-medium text-foreground hover:text-primary"
                    >
                      {file.name}
                    </Link>
                    {file.description ? (
                      <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">{file.description}</p>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{file.format}</span>
                  </TableCell>
                  <TableCell className="text-right">
                    {file.layerCount > 0 ? (
                      <span className="tabular-nums">{file.layerCount}</span>
                    ) : (
                      /* 
                        层数为 0 = 这条配置还没有任何作用域版本，等于「建了但没生效」。
                        只显示一个 0 会被当成正常值，运维会以为它已经在用了。
                      */
                      <Badge
                        variant="outline"
                        className="border-status-warning/50 text-[10px] text-status-warning"
                        title={t('configFiles.noLayersHint')}
                      >
                        {t('configFiles.layersNone')}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-1">
                      {file.schemaJson ? (
                        <Badge variant="outline" className="text-[10px] text-primary" title={t('configFiles.capSchemaHint')}>
                          {t('configFiles.capSchema')}
                        </Badge>
                      ) : null}
                      {file.sensitivePaths && file.sensitivePaths !== '[]' ? (
                        <Badge variant="outline" className="text-[10px] text-status-warning" title={t('configFiles.capSensitiveHint')}>
                          {t('configFiles.capSensitive')}
                        </Badge>
                      ) : null}
                      {!file.schemaJson && (!file.sensitivePaths || file.sensitivePaths === '[]') ? (
                        <span className="text-xs text-muted-foreground">{t('common.na')}</span>
                      ) : null}
                    </div>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {formatRelativeTime(file.updatedAt) || t('common.na')}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => navigate(`/config-files/${file.id}`)}
                        data-testid={`config-file-open-${file.id}`}
                      >
                        {t('configFiles.open')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t('common.edit')}
                        onClick={() => openEdit(file)}
                        data-testid={`config-file-edit-${file.id}`}
                      >
                        <Pencil aria-hidden />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t('common.delete')}
                        onClick={() => setDeleting(file)}
                        data-testid={`config-file-delete-${file.id}`}
                      >
                        <Trash2 aria-hidden />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? t('configFiles.editTitle') : t('configFiles.createTitle')}</DialogTitle>
            <DialogDescription>{t('configFiles.formHint')}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-1">
              <Label htmlFor="config-file-name">{t('configFiles.fieldName')}</Label>
              <Input
                id="config-file-name"
                value={draft.name}
                // name 是全局唯一标识，创建后不可改（后端会以 NAME_IMMUTABLE 拒绝）。
                disabled={editing !== null}
                placeholder="plugins/Foo/config.yml"
                onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
                aria-invalid={nameInvalid || undefined}
              />
              <p className="text-xs text-muted-foreground">{t('configFiles.fieldNameHint')}</p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="config-file-format">{t('configFiles.fieldFormat')}</Label>
              <NativeSelect
                id="config-file-format"
                value={draft.format}
                disabled={editing !== null}
                onChange={(e) => setDraft((d) => ({ ...d, format: e.target.value as ConfigFileFormat }))}
              >
                {CONFIG_FILE_FORMATS.map((f) => (
                  <option key={f} value={f}>
                    {f}
                  </option>
                ))}
              </NativeSelect>
              <p className="text-xs text-muted-foreground">{t('configFiles.fieldFormatHint')}</p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="config-file-description">{t('configFiles.fieldDescription')}</Label>
              <Input
                id="config-file-description"
                value={draft.description}
                onChange={(e) => setDraft((d) => ({ ...d, description: e.target.value }))}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="config-file-schema">{t('configFiles.fieldSchema')}</Label>
              <Textarea
                id="config-file-schema"
                value={draft.schemaJson}
                rows={4}
                className="font-mono text-xs"
                placeholder='{"type":"object"}'
                onChange={(e) => setDraft((d) => ({ ...d, schemaJson: e.target.value }))}
              />
              <p className="text-xs text-muted-foreground">{t('configFiles.fieldSchemaHint')}</p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => { void submit() }}
              disabled={nameInvalid || createMut.isPending || updateMut.isPending}
              data-testid="config-file-submit"
            >
              {createMut.isPending || updateMut.isPending ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DangerConfirm
        open={deleting !== null}
        title={t('configFiles.deleteTitle')}
        description={t('configFiles.deleteBody', { name: deleting?.name ?? '' })}
        onConfirm={() => { void confirmDelete() }}
        onCancel={() => setDeleting(null)}
        pending={deleteMut.isPending}
      />
    </PageShell>
  )
}
