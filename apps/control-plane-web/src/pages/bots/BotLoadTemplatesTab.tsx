import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, Pencil, Plus, Trash2, Play } from 'lucide-react'
import {
  useBotLoadTemplates,
  useDeleteBotLoadTemplate,
  type BotLoadTemplate,
} from '@/api/botLoad'
import { useDebounced } from '@/lib/use-debounced'
import { mergeSearchParams, readTemplatesFilter } from '@/lib/bot-load/url-state'
import { summarizeCommandSchedule, summarizeLoadProfile } from '@/lib/bot-load/summaries'
import TemplateDialog from '@/components/bot-load/TemplateDialog'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
import DangerConfirm from '@/components/DangerConfirm'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'

/** 压测模板列表：搜索/标签/CRUD/从模板运行。 */
export default function BotLoadTemplatesTab() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const filter = readTemplatesFilter(searchParams)
  const [search, setSearch] = useState(filter.q ?? '')
  const debouncedQ = useDebounced(search, 300)
  const page = filter.page ?? 1
  const pageSize = 20

  const query = useBotLoadTemplates({
    page,
    pageSize,
    q: debouncedQ.trim() || undefined,
    tag: filter.tag,
  })
  const deleteTpl = useDeleteBotLoadTemplate()

  const [dialog, setDialog] = useState<{
    open: boolean
    mode: 'create' | 'edit' | 'copy'
    template: BotLoadTemplate | null
  }>({ open: false, mode: 'create', template: null })
  const [wizardTpl, setWizardTpl] = useState<BotLoadTemplate | null>(null)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [deleteId, setDeleteId] = useState<number | null>(null)

  const items = useMemo(() => query.data?.items ?? [], [query.data?.items])
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const tags = useMemo(() => {
    const set = new Set<string>()
    for (const it of items) for (const tag of it.tags) set.add(tag)
    return [...set]
  }, [items])

  const setPage = (p: number) => {
    setSearchParams(mergeSearchParams(searchParams, { page: p <= 1 ? null : p }), { replace: true })
  }

  const setTag = (tag: string) => {
    setSearchParams(mergeSearchParams(searchParams, { tag: tag || null, page: null }), { replace: true })
  }

  // 搜索防抖写 URL
  useMemo(() => {
    const q = debouncedQ.trim()
    if ((filter.q ?? '') === q) return
    setSearchParams(mergeSearchParams(searchParams, { q: q || null, page: null }), { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 仅 debouncedQ 驱动
  }, [debouncedQ])

  const confirmDelete = () => {
    if (deleteId == null) return
    deleteTpl.mutate(deleteId, {
      onSuccess: () => {
        toast.success(t('botsLoad.templateDeleted'))
        setDeleteId(null)
      },
      onError: () => toast.error(t('botsLoad.templateDeleteFailed')),
    })
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t('botsLoad.searchTemplates')}
          className="h-9 w-56"
        />
        <div className="flex flex-wrap gap-1">
          <Button size="xs" variant={!filter.tag ? 'default' : 'outline'} onClick={() => setTag('')}>
            {t('botsLoad.allTags')}
          </Button>
          {tags.map((tag) => (
            <Button
              key={tag}
              size="xs"
              variant={filter.tag === tag ? 'default' : 'outline'}
              onClick={() => setTag(tag)}
            >
              {tag}
            </Button>
          ))}
        </div>
        <Button className="ml-auto" onClick={() => setDialog({ open: true, mode: 'create', template: null })}>
          <Plus className="size-4" /> {t('botsLoad.createTemplate')}
        </Button>
      </div>

      {query.isError && (
        <div className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm">
          {t('botsLoad.templatesLoadFailed')}
          <Button size="xs" variant="outline" className="ml-2" onClick={() => query.refetch()}>
            {t('common.refresh')}
          </Button>
        </div>
      )}

      {query.isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : items.length === 0 ? (
        <p className="rounded-lg border py-10 text-center text-muted-foreground">{t('botsLoad.templatesEmpty')}</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/40">
              <TableRow>
                <TableHead>{t('common.name')}</TableHead>
                <TableHead>{t('botsLoad.tags')}</TableHead>
                <TableHead>{t('botsLoad.commandSummary')}</TableHead>
                <TableHead>{t('botsLoad.profileSummaryCol')}</TableHead>
                <TableHead>{t('botsLoad.updatedAt')}</TableHead>
                <TableHead className="text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((tpl) => {
                const cmd = summarizeCommandSchedule(tpl.commandSchedule)
                const prof = summarizeLoadProfile(tpl.loadProfile)
                return (
                  <TableRow key={tpl.id}>
                    <TableCell>
                      <div className="font-medium">{tpl.name}</div>
                      {tpl.description && (
                        <div className="text-xs text-muted-foreground line-clamp-1">{tpl.description}</div>
                      )}
                    </TableCell>
                    <TableCell className="text-xs">
                      {tpl.tags.length ? tpl.tags.join(', ') : '—'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t('botsLoad.cmdSummaryText', {
                        count: cmd.commandCount,
                        occ: cmd.occurrenceCount,
                      })}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t('botsLoad.profSummaryText', {
                        type: tpl.loadProfile.type,
                        target: prof.targetBots,
                      })}
                    </TableCell>
                    <TableCell className="text-xs tabular-nums">
                      {new Date(tpl.updatedAt).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="xs"
                          variant="outline"
                          onClick={() => {
                            setWizardTpl(tpl)
                            setWizardOpen(true)
                          }}
                          aria-label={t('botsLoad.runFromTemplate')}
                        >
                          <Play className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => setDialog({ open: true, mode: 'edit', template: tpl })}
                          aria-label={t('common.edit')}
                        >
                          <Pencil className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => setDialog({ open: true, mode: 'copy', template: tpl })}
                          aria-label={t('botsLoad.copyTemplate')}
                        >
                          <Copy className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => setDeleteId(tpl.id)}
                          aria-label={t('common.delete')}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}

      <div className="flex items-center justify-between text-xs text-muted-foreground">
        <span>{t('bots.totalCount', { count: total })}</span>
        <div className="flex items-center gap-2">
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => setPage(page - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => setPage(page + 1)}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>

      <TemplateDialog
        open={dialog.open}
        onOpenChange={(open) => setDialog((d) => ({ ...d, open }))}
        template={dialog.template}
        mode={dialog.mode}
      />
      <BotLoadWizard open={wizardOpen} onOpenChange={setWizardOpen} template={wizardTpl} />
      <DangerConfirm
        open={deleteId !== null}
        title={t('botsLoad.deleteTemplateTitle')}
        description={t('botsLoad.deleteTemplateDesc')}
        scope="group"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteId(null)}
      />
    </div>
  )
}
