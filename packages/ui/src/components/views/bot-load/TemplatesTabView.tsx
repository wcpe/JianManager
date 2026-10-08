import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Pencil, Plus, Trash2, Play } from 'lucide-react'
import { Button } from '../../button'
import { Input } from '../../input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../../table'
import { summarizeCommandSchedule, summarizeLoadProfile } from '../../../lib/bot-load-summaries'
import type { BotLoadTemplate } from '../../../lib/bot-load-types'

/** 模板列表分页尺寸（容器取数须与本常量一致）。 */
export const TEMPLATES_TAB_PAGE_SIZE = 20

/**
 * 压测模板列表 tab 的注入契约（受控视图，ADR-097 b 范式）。
 *
 * 取数与 URL 同步（搜索防抖、标签、页码）在容器，mutation 与 toast 由容器的回调实现；
 * 包内只做展示与派生（当前页标签集合、行摘要）。
 */
export interface TemplatesTabViewProps {
  /** 当前页模板数据（容器经 `useBotLoadTemplates` 取数）。 */
  data?: { items: BotLoadTemplate[]; total: number }
  /** 当前页码（URL 状态，容器维护）。 */
  page: number
  isLoading?: boolean
  isError?: boolean
  /** 错误态「刷新」回调（容器 `refetch`）。 */
  onRefresh: () => void
  /** 翻页（容器写回 URL）。 */
  onPageChange: (page: number) => void
  /** 搜索框值（会触发取数，故由容器受控；防抖与 URL 写回在容器侧）。 */
  search: string
  /** 搜索框变更（容器做防抖后写 URL）。 */
  onSearchChange: (value: string) => void
  /** 当前标签过滤（URL 状态，容器维护）。 */
  activeTag?: string
  /** 标签切换（容器写回 URL）。 */
  onTagChange: (tag: string) => void
  /** 顶部「创建模板」（容器打开创建对话框）。 */
  onCreateClick: () => void
  /** 从模板运行：容器打开向导。 */
  onRunFromTemplate: (tpl: BotLoadTemplate) => void
  /** 编辑模板：容器打开对话框。 */
  onEditTemplate: (tpl: BotLoadTemplate) => void
  /** 复制模板：容器打开对话框。 */
  onCopyTemplate: (tpl: BotLoadTemplate) => void
  /** 删除模板：容器进入危险操作确认。 */
  onDeleteTemplate: (tpl: BotLoadTemplate) => void
}

/** 压测模板列表：搜索/标签/分页展示与行操作入口（CRUD 与运行由容器执行）。 */
export function TemplatesTabView({
  data,
  page,
  isLoading,
  isError,
  onRefresh,
  onPageChange,
  search,
  onSearchChange,
  activeTag,
  onTagChange,
  onCreateClick,
  onRunFromTemplate,
  onEditTemplate,
  onCopyTemplate,
  onDeleteTemplate,
}: TemplatesTabViewProps) {
  const { t } = useTranslation()

  const items = useMemo(() => data?.items ?? [], [data?.items])
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / TEMPLATES_TAB_PAGE_SIZE))

  // 标签集合由当前页数据派生（与原实现一致，不走服务端聚合）。
  const tags = useMemo(() => {
    const set = new Set<string>()
    for (const it of items) for (const tag of it.tags) set.add(tag)
    return [...set]
  }, [items])

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          placeholder={t('botsLoad.searchTemplates')}
          aria-label={t('botsLoad.searchTemplates')}
          className="h-9 w-56"
        />
        <div className="flex flex-wrap gap-1">
          <Button size="xs" variant={!activeTag ? 'default' : 'outline'} onClick={() => onTagChange('')}>
            {t('botsLoad.allTags')}
          </Button>
          {tags.map((tag) => (
            <Button
              key={tag}
              size="xs"
              variant={activeTag === tag ? 'default' : 'outline'}
              onClick={() => onTagChange(tag)}
            >
              {tag}
            </Button>
          ))}
        </div>
        <Button className="ml-auto" onClick={onCreateClick}>
          <Plus className="size-4" /> {t('botsLoad.createTemplate')}
        </Button>
      </div>

      {isError && (
        <div className="rounded border border-destructive/40 bg-destructive/10 p-3 text-sm">
          {t('botsLoad.templatesLoadFailed')}
          <Button size="xs" variant="outline" className="ml-2" onClick={onRefresh}>
            {t('common.refresh')}
          </Button>
        </div>
      )}

      {isLoading ? (
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
                          onClick={() => onRunFromTemplate(tpl)}
                          aria-label={t('botsLoad.runFromTemplate')}
                        >
                          <Play className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => onEditTemplate(tpl)}
                          aria-label={t('common.edit')}
                        >
                          <Pencil className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => onCopyTemplate(tpl)}
                          aria-label={t('botsLoad.copyTemplate')}
                        >
                          <Copy className="size-3.5" />
                        </Button>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => onDeleteTemplate(tpl)}
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
          <Button size="xs" variant="ghost" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => onPageChange(page + 1)}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}
