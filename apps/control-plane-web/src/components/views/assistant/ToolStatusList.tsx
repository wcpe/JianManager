import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui'
import {
  isToolDispatcher,
  isToolExploration,
  toolCallPhase,
  truncateToolPreview,
} from '@/components/views/assistant/assistant-tool-status'
import type { ToolCallView } from '@/components/views/assistant/assistant-tool-status'

/** 
 * 工具调用状态徽标列表（§5）。
 *
 * 为什么是一个独立列表而不是把状态塞进消息气泡里：
 *  - 一次工具调用由 `tool_call` / `tool_status` / `tool_result` **三类事件**共同描述，
 *    它们与 text_delta 交错到达，聚合结果天然是「每 callId 一行」，不是消息流的一环；
 *  - 用户真正想知道的是「助手现在在干什么、卡在哪一步、刚才那次失败是什么码」，
 *    这层信息比工具参数更有用，值得单独成块（参数只在确认卡里全量展示，见 ConfirmationCards）。
 *
 * 视觉沿用设计系统已有的状态 chip（`chip-ok` / `chip-bad` / `chip-idle` / `chip-neutral`），
 * 不在这里另造一套红绿色值——明暗主题的对比度由 token 统一保证。
 */

export interface ToolStatusListProps {
  /** 聚合后的调用列表；空或未定义时整块不渲染（不留空壳）。 */
  items?: ToolCallView[]
  className?: string
}

/** 状态词 → chip 变体。未知状态用 idle 兜底，但文案仍显示原词（见 statusLabel）。 */
function statusVariant(status: string): 'chip-ok' | 'chip-bad' | 'chip-idle' | 'chip-neutral' {
  switch (toolCallPhase(status)) {
    case 'running':
      return 'chip-idle'
    case 'pending_confirmation':
      // 待确认是**唯一需要用户动作**的状态，用实底次要色让它比「执行中」更显眼。
      return 'chip-neutral'
    case 'ok':
      return 'chip-ok'
    case 'failed':
      return 'chip-bad'
    default:
      return 'chip-idle'
  }
}

/** 
 * 状态词 → 中文文案。
 *
 * 刻意**逐个分支写字面量 key**（而不是拼 `t('assistant.toolStatus' + x)`）：
 * 拼出来的 key 无法被 `src/i18n/missing-keys.test.ts` 静态提取，漏补语言包时
 * 英文界面会静默漏出中文兜底。未知状态直接显示后端的原词——宁可露出一个生词，
 * 也不把它假装成「已完成」。
 */
function statusLabel(status: string, t: TFunction): string {
  switch (toolCallPhase(status)) {
    case 'running':
      return t('assistant.toolStatusRunning', '执行中')
    case 'pending_confirmation':
      return t('assistant.toolStatusPendingConfirmation', '待确认')
    case 'ok':
      return t('assistant.toolStatusOk', '已完成')
    case 'failed':
      return t('assistant.toolStatusFailed', '失败')
    default:
      /* 
        unverified：写操作**已发出但回执未经核实**（Worker 断连、超时等）。
        不能显示为「已完成」——那会让用户误以为操作确实生效了；
        也不能直接返回后端原文（界面上会冒出英文 "unverified"）。
      */
      if (status === 'unverified') {
        return t('assistant.toolStatusUnverified', '结果待核实')
      }
      return status
  }
}

export function ToolStatusList({ items, className }: ToolStatusListProps) {
  const { t } = useTranslation()
  if (!items || items.length === 0) return null

  return (
    <section
      data-slot="tool-status-list"
      aria-label={t('assistant.toolStatusTitle', '工具调用')}
      className={className ?? 'flex flex-col gap-1 px-1 pt-1'}
    >
      <p className="text-[11px] font-medium text-muted-foreground">
        {t('assistant.toolStatusTitle', '工具调用')}
      </p>
      <ul className="flex flex-col gap-1">
        {items.map((item) => {
          // 预览按版面截断；完整文本留在 title 上，悬停可看（也可复制）。
          const preview = item.preview ? truncateToolPreview(item.preview) : null
          const clipped = item.truncated === true || preview?.clipped === true
          return (
            <li
              key={item.callId}
              data-call-id={item.callId}
              data-status={item.status}
              className="rounded-md border border-border/60 bg-muted/30 px-2 py-1"
            >
              <div className="flex items-center gap-1.5">
                <Badge variant={statusVariant(item.status)} className="shrink-0">
                  {statusLabel(item.status, t)}
                </Badge>
                {/* 探索类（查工具 / 读参数定义）单独标注：它们不产生业务副作用，
                    若不区分，用户会以为助手在一堆真实操作里乱点。 */}
                {isToolExploration(item.name) ? (
                  <Badge variant="outline" className="shrink-0">
                    {t('assistant.toolKindExplore', '探索')}
                  </Badge>
                ) : null}
                {isToolDispatcher(item.name) ? (
                  <Badge variant="outline" className="shrink-0">
                    {t('assistant.toolKindExec', '执行')}
                  </Badge>
                ) : null}
                {/* 工具名走等宽并 truncate：形如 system.agent.instance_start 的名字很长，
                    不截断会在窄侧栏里把状态徽标挤出可视区。 */}
                <code className="min-w-0 flex-1 truncate text-[11px]" title={item.name}>
                  {item.name}
                </code>
              </div>
              {/* 失败必须给出错误码：只说「失败」等于让用户去猜是权限、参数还是上游超时。 */}
              {item.errorCode ? (
                <p className="mt-0.5 text-[11px] text-status-danger">
                  {t('assistant.toolStatusErrorCode', '错误码：{{code}}', { code: item.errorCode })}
                </p>
              ) : null}
              {preview && preview.text !== '' ? (
                <p
                  className="mt-0.5 line-clamp-2 whitespace-pre-wrap break-all text-[11px] leading-relaxed text-muted-foreground"
                  title={item.preview}
                >
                  {preview.text}
                </p>
              ) : null}
              {clipped ? (
                <p className="mt-0.5 text-[11px] text-muted-foreground/80">
                  {t('assistant.toolStatusTruncated', '结果已截断')}
                </p>
              ) : null}
            </li>
          )
        })}
      </ul>
    </section>
  )
}
