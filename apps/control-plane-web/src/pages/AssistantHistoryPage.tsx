import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, Pencil, Undo2 } from 'lucide-react'
import { Badge, Button, FieldLabel, Input, Panel, Textarea } from '@jianmanager/ui'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import {
  useAssistantConversationSearch,
  useAssistantMessages,
  useAssistantToolCalls,
  useEditAssistantMessage,
  useRollbackAssistantConversation,
} from '@/api/assistant-chat'
import { ToolStatusList } from '@/components/views/assistant/ToolStatusList'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { toast } from 'sonner'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 对话历史页（FR-542 / FR-543）。
 *
 * ## 为什么检索走服务端
 *
 * 会话与消息量随时间增长。把全部会话拉到浏览器再过滤既慢，又会让
 * 「搜索结果不全」变成难察觉的假象（只搜到已加载的那一页）。
 *
 * ## 回退的语义必须显式告知
 *
 * 回退**只回退会话内容，不撤销已发生的副作用**：回退到「启动实例」之前，
 * 那个实例仍然是启动着的。确认弹窗里必须写清这句——
 * 不写会让用户以为回退等于撤销，而真实世界的实例状态没有任何变化。
 */
export default function AssistantHistoryPage() {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  // 防抖后的关键词：键入过程中不发请求，避免每敲一个字打一次后端。
  const [committed, setCommitted] = useState('')
  const [openedId, setOpenedId] = useState<number | null>(null)

  // 防抖定时器。用 useRef 而不是 useMemo 包一层可变对象：
  // 后者每次渲染都要靠「不写依赖」维持引用稳定，属于反模式（lint 也会拒绝）。
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const onKeywordChange = (next: string) => {
    setKeyword(next)
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setCommitted(next.trim()), 300)
  }

  const searchQuery = useAssistantConversationSearch(committed)
  const messagesQuery = useAssistantMessages(openedId)
  // 历史工具回显（FR-519）：按 messageId 分组的工具调用记录。
  const toolCallsQuery = useAssistantToolCalls(openedId)
  // 键是字符串化的 messageId：JSON 对象键只能是字符串，这里统一转换一次，
  // 避免在渲染循环里反复 String() 并因此漏掉某处。
  const toolCallsByMessage = toolCallsQuery.data ?? {}
  const editMessage = useEditAssistantMessage()
  const rollback = useRollbackAssistantConversation()

  const [editing, setEditing] = useState<{ id: number; content: string } | null>(null)
  // 原地保存：成功后关闭编辑态；失败保持编辑态，让用户能改完再试（内容不丢）。
  const saveEditing = () => {
    if (!editing) return
    void editMessage
      .mutateAsync({ messageId: editing.id, content: editing.content })
      .then(() => {
        setEditing(null)
        toast.success(t('assistant.historyEditSaved', '已保存'))
      })
      .catch((err) => toast.error(errMessage(err, t('assistant.historyEditFailed', '保存失败'))))
  }
  const [rollbackTarget, setRollbackTarget] = useState<number | null>(null)

  const results = searchQuery.data ?? []

  return (
    <PageShell>
      <PageHeader
        title={t('assistant.historyTitle', '对话历史')}
        description={t(
          'assistant.historyDesc',
          '按关键词检索历史对话（命中标题与消息正文）。可更正自己发过的消息，或把会话回退到某条消息。',
        )}
      />

      <Panel title={t('assistant.historySearch', '检索')}>
        <div className="space-y-1">
          <FieldLabel htmlFor="assistant-history-search">
            {t('assistant.historyKeyword', '关键词')}
          </FieldLabel>
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              id="assistant-history-search"
              className="pl-9"
              value={keyword}
              placeholder={t('assistant.historyPlaceholder', '输入关键词，例如实例名或报错片段')}
              onChange={(e) => onKeywordChange(e.target.value)}
            />
          </div>
          <p className="text-xs text-muted-foreground">
            {t(
              'assistant.historySearchHint',
              '检索在服务端执行，只返回你自己的会话；空关键词不检索。',
            )}
          </p>
        </div>
      </Panel>

      <Panel title={t('assistant.historyResults', '结果')}>
        {committed === '' ? (
          <EmptyState
            title={t('assistant.historyNeedKeyword', '输入关键词开始检索')}
            description={t(
              'assistant.historyNeedKeywordDesc',
              '历史记录不会默认全量列出，以免一次加载把大量内容拖进浏览器。',
            )}
          />
        ) : searchQuery.isLoading ? (
          <p className="py-6 text-center text-sm text-muted-foreground">
            {t('common.loading', '加载中…')}
          </p>
        ) : searchQuery.isError ? (
          <p className="py-6 text-center text-sm text-destructive" role="alert">
            {t('assistant.historyError', '检索失败，请稍后重试。')}
          </p>
        ) : results.length === 0 ? (
          <EmptyState
            title={t('assistant.historyNoResults', '没有匹配的对话')}
            description={t('assistant.historyNoResultsDesc', '换个关键词试试，或确认该内容确实保存过。')}
          />
        ) : (
          <ul className="divide-y">
            {results.map((c) => (
              <li key={c.id} className="flex items-center justify-between gap-3 py-2">
                <button
                  type="button"
                  className="min-w-0 flex-1 text-left"
                  aria-label={t('assistant.historyOpen', '打开会话 {{title}}', {
                    title: c.title || `#${c.id}`,
                  })}
                  onClick={() => setOpenedId(c.id)}
                >
                  <p className="truncate text-sm font-medium">{c.title || `#${c.id}`}</p>
                  <p className="truncate text-xs text-muted-foreground">{c.updatedAt}</p>
                </button>
                <Button size="sm" variant="outline" onClick={() => setOpenedId(c.id)}>
                  {t('assistant.historyOpenShort', '打开')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      {openedId !== null && (
        <Panel
          title={t('assistant.historyMessages', '消息')}
          actions={
            <Button size="sm" variant="ghost" onClick={() => setOpenedId(null)}>
              {t('common.close', '关闭')}
            </Button>
          }
        >
          {messagesQuery.isLoading ? (
            <p className="py-6 text-center text-sm text-muted-foreground">
              {t('common.loading', '加载中…')}
            </p>
          ) : messagesQuery.isError ? (
            <p className="py-6 text-center text-sm text-destructive" role="alert">
              {t('assistant.historyMessagesError', '读取消息失败。')}
            </p>
          ) : (messagesQuery.data ?? []).length === 0 ? (
            <EmptyState
              title={t('assistant.historyEmptyMessages', '该会话还没有消息')}
              description={t('assistant.historyEmptyMessagesDesc', '可以回到对话页继续提问。')}
            />
          ) : (
            <ul className="space-y-2">
              {(messagesQuery.data ?? []).map((m) => {
                const superseded = Boolean(m.supersededAt)
                return (
                  <li
                    key={m.id}
                    className={`rounded-md border p-2 ${superseded ? 'opacity-50' : ''}`}
                  >
                    <div className="flex items-center gap-2">
                      <Badge variant="secondary">{m.role}</Badge>
                      {m.editedAt && (
                        <Badge variant="outline">
                          {t('assistant.historyEdited', '已编辑 {{n}} 次', { n: m.editCount ?? 1 })}
                        </Badge>
                      )}
                      {superseded && (
                        <Badge variant="outline">{t('assistant.historySuperseded', '已被回退')}</Badge>
                      )}
                      {m.role === 'user' && !superseded && (
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={t('assistant.historyEdit', '编辑这条消息')}
                          onClick={() => setEditing({ id: m.id, content: m.content })}
                        >
                          <Pencil className="size-3.5" />
                        </Button>
                      )}
                      {!superseded && (
                        <Button
                          size="sm"
                          variant="ghost"
                          aria-label={t('assistant.historyRollbackTo', '回退到这条消息')}
                          onClick={() => setRollbackTarget(m.id)}
                        >
                          <Undo2 className="size-3.5" />
                        </Button>
                      )}
                    </div>
                    {editing?.id === m.id ? (
                      /* 原地编辑：在消息位置直接改，而不是弹窗。
                         弹窗会把「改了哪条」的上下文遮掉，长会话里尤其容易改错。 */
                      <div className="mt-1 space-y-2">
                        <Textarea
                          aria-label={t('assistant.historyEditInput', '编辑消息内容')}
                          className="min-h-20"
                          value={editing.content}
                          onChange={(e) => setEditing({ id: m.id, content: e.target.value })}
                        />
                        <div className="flex items-center gap-2">
                          <Button
                            size="sm"
                            disabled={editMessage.isPending || editing.content.trim() === ''}
                            onClick={() => saveEditing()}
                          >
                            {t('common.save', '保存')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={editMessage.isPending}
                            onClick={() => setEditing(null)}
                          >
                            {t('common.cancel', '取消')}
                          </Button>
                          <p className="text-xs text-muted-foreground">
                            {t(
                              'assistant.historyEditHint',
                              '只更正文本，不会自动重新生成回复；原文保留可追溯。',
                            )}
                          </p>
                        </div>
                      </div>
                    ) : (
                      <p className="mt-1 whitespace-pre-wrap break-words text-sm">{m.content}</p>
                    )}
                    {/* 历史工具回显（FR-519）：把当时调用了哪些工具、各自什么状态补回来。
                        此前这些数据只落在事件流里、没有任何读取面，回看历史是「纯文本」的，
                        用户无法判断助手当时到底调没调工具、卡在哪一步、失败的是什么码。
                        role=tool 的消息本身不发回气泡——它的 content 是给模型看的观察结果，
                        与状态块重复；状态块才是人读的那一层。 */}
                    {toolCallsByMessage[String(m.id)] && (
                      <div className="mt-2">
                        <ToolStatusList items={toolCallsByMessage[String(m.id)]} />
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </Panel>
      )}

      {/* 回退确认：必须显式说明「不撤销副作用」，否则用户会以为实例被一并停回去了。 */}
      <DangerConfirm
        open={rollbackTarget !== null}
        title={t('assistant.historyRollbackTitle', '回退到这条消息')}
        description={t(
          'assistant.historyRollbackDesc',
          '该消息之后的内容将从会话中移除（不物理删除，可追溯）。注意：回退不会撤销已经发生的操作——例如已启动的实例仍然是启动着的。',
        )}
        confirmLabel={t('assistant.historyRollbackConfirm', '确认回退')}
        onConfirm={() => {
          const target = rollbackTarget
          setRollbackTarget(null)
          if (target === null || openedId === null) return
          void rollback
            .mutateAsync({ conversationId: openedId, messageId: target })
            .then((res) =>
              toast.success(
                t('assistant.historyRollbackDone', '已回退 {{n}} 条消息', { n: res.superseded }),
              ),
            )
            .catch((err) =>
              toast.error(errMessage(err, t('assistant.historyRollbackFailed', '回退失败'))),
            )
        }}
        onCancel={() => setRollbackTarget(null)}
      />
    </PageShell>
  )
}
