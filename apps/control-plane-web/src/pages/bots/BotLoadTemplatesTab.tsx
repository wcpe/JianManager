import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBotLoadTemplates, useDeleteBotLoadTemplate, type BotLoadTemplate } from '@/api/botLoad'
import { useDebounced } from '@/lib/use-debounced'
import { mergeSearchParams, readTemplatesFilter } from '@/lib/bot-load/url-state'
import TemplateDialog from '@/components/bot-load/TemplateDialog'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
import DangerConfirm from '@/components/DangerConfirm'
import { TEMPLATES_TAB_PAGE_SIZE, TemplatesTabView } from '@/components/views/bot-load/TemplatesTabView'

/**
 * 压测模板列表 tab 的容器：取数、搜索防抖/筛选写 URL、删除 mutation 与三处弹窗装配。
 * 展示层已回迁应用侧（`TemplatesTabView`，ADR-097）。
 */
export default function BotLoadTemplatesTab() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const filter = readTemplatesFilter(searchParams)
  const [search, setSearch] = useState(filter.q ?? '')
  const debouncedQ = useDebounced(search, 300)
  const page = filter.page ?? 1

  const query = useBotLoadTemplates({
    page,
    pageSize: TEMPLATES_TAB_PAGE_SIZE,
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
    <>
      <TemplatesTabView
        data={query.data}
        page={page}
        isLoading={query.isLoading}
        isError={query.isError}
        onRefresh={() => {
          void query.refetch()
        }}
        onPageChange={(p) =>
          setSearchParams(mergeSearchParams(searchParams, { page: p <= 1 ? null : p }), { replace: true })
        }
        search={search}
        onSearchChange={setSearch}
        activeTag={filter.tag}
        onTagChange={(tag) =>
          setSearchParams(mergeSearchParams(searchParams, { tag: tag || null, page: null }), { replace: true })
        }
        onCreateClick={() => setDialog({ open: true, mode: 'create', template: null })}
        onRunFromTemplate={(tpl) => {
          setWizardTpl(tpl)
          setWizardOpen(true)
        }}
        onEditTemplate={(tpl) => setDialog({ open: true, mode: 'edit', template: tpl })}
        onCopyTemplate={(tpl) => setDialog({ open: true, mode: 'copy', template: tpl })}
        onDeleteTemplate={(tpl) => setDeleteId(tpl.id)}
      />

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
    </>
  )
}
