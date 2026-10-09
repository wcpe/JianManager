// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做三块数据取数、写动作触发、事件筛选持有与两个对话框容器的接线。
import { useState } from 'react'
import { useAlertRules, useAlertEvents, useDeleteAlertRule, useUpdateAlertRule, useAlertChannels, useDeleteAlertChannel, useTestAlertChannel, useAcknowledgeEvent, useMarkAllRead, useUnreadAlertCount } from '@/api/alerts'
import { AlertsPageView } from '@/components/views/alerts/AlertsPageView'
import type { AlertEventFilter, AlertTab } from '@/components/views/alerts/AlertsPageView'
import { RuleDialog } from './alerts/RuleDialog'
import { ChannelDialog } from './alerts/ChannelDialog'

/**
 * 告警页容器（ADR-097 b 范式）：三块数据取数、全部写动作与未读计数在这里决定，
 * 三个 Tab 的展示、Tab 选中、对话框开合与删除目标交共享视图。
 *
 * 上提的受控状态有两个：`filter`（事件筛选，含页码）——它是 `useAlertEvents` 的查询键；
 * 以及 `tab`——事件列表原先挂在事件 Tab 内容里，Radix 未激活即不挂载，故只有切到事件 Tab 才请求，
 * 现在 hook 归本层，改用 `enabled` 门控同一时机，tab 因此也成为「会触发取数的状态」。
 * 两个对话框由应用侧容器实现（节点/实例候选与 mutation 在应用侧），按视图的开合语义经插槽注入。
 * 保留原路径与原默认导出，路由表（route-chunks 的 `/alerts`）无需改动。
 */
export default function AlertsPage() {
  // 事件筛选：改条件即回第 1 页的合成在视图内完成，这里只落库。
  const [filter, setFilter] = useState<AlertEventFilter>({})
  // 当前 Tab：事件查询只在事件 Tab 激活时发起，保住原先「未激活的 Tab 内容不挂载」的取数时机。
  const [tab, setTab] = useState<AlertTab>('rules')
  const { data: unread } = useUnreadAlertCount()
  const { data: rules, isLoading: rulesLoading } = useAlertRules()
  const { data: channels, isLoading: channelsLoading } = useAlertChannels()
  const { data: eventPage } = useAlertEvents(filter, { enabled: tab === 'events' })

  const deleteRule = useDeleteAlertRule()
  const updateRule = useUpdateAlertRule()
  const deleteChannel = useDeleteAlertChannel()
  const testChannel = useTestAlertChannel()
  const acknowledgeEvent = useAcknowledgeEvent()
  const markAllRead = useMarkAllRead()

  return (
    <AlertsPageView
      tab={tab}
      onTabChange={setTab}
      unreadCount={unread}
      rules={rules}
      rulesLoading={rulesLoading}
      ruleUpdating={updateRule.isPending}
      channels={channels}
      channelsLoading={channelsLoading}
      channelTesting={testChannel.isPending}
      events={eventPage?.items}
      eventsTotal={eventPage?.total}
      eventFilter={filter}
      onEventFilterChange={setFilter}
      onToggleRule={(rule) => updateRule.mutate({ id: rule.id, enabled: !rule.enabled })}
      onDeleteRule={(rule) => deleteRule.mutate(rule.id)}
      onTestChannel={(channel) => testChannel.mutate(channel.id)}
      onDeleteChannel={(channel) => deleteChannel.mutate(channel.id)}
      onAcknowledgeEvent={(eventId) => acknowledgeEvent.mutate(eventId)}
      onMarkAllRead={() => markAllRead.mutate()}
      renderRuleDialog={({ rule, channels: dialogChannels, onClose }) => (
        <RuleDialog rule={rule} channels={dialogChannels} onClose={onClose} />
      )}
      renderChannelDialog={({ channel, onClose }) => (
        <ChannelDialog channel={channel} onClose={onClose} />
      )}
    />
  )
}
