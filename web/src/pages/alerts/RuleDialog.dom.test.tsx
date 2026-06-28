import { describe, it, expect, vi } from 'vitest'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import type { AlertChannelInfo, AlertRuleInfo } from '@/api/alerts'
import { RuleDialog } from './RuleDialog'

/**
 * RuleDialog 强断言（FR-208 告警规则对话框）：直接渲染组件并传 props（自绘模态，
 * 无 role=dialog，故按标题 <h2> 锁面板再取字段）。覆盖：字段渲染 + 编辑回填、
 * 创建/更新提交成功（POST/PUT 命中、onClose 回调被调）、500 注入显错误态不崩。
 * 渲染前 loginMockUser() 让 requireAuth 保护的 /alerts/* 端点放行。
 *
 * onClose 即对话框成功后的回调约定（组件 props 无 onSuccess，成功 = mutateAsync resolve 后调 onClose）。
 */

const seedChannels: AlertChannelInfo[] = [
  {
    id: 1,
    uuid: 'chan-webhook',
    name: '运维 Webhook',
    type: 'webhook',
    enabled: true,
    config: JSON.stringify({ url: 'https://hooks.example.com/ops' }),
    createdAt: new Date().toISOString(),
  },
]

/**
 * 已存在的指标规则（编辑回填靶子）。id 取 1 命中 observ.ts 种子集合，
 * 使 PUT /alerts/rules/1 能更新到行（否则 mock update 找不到行返 404）。
 */
const existingRule: AlertRuleInfo = {
  id: 1,
  uuid: 'rule-mem',
  name: '内存过载告警',
  triggerType: 'metric',
  level: 'critical',
  targetType: 'node',
  targetId: 1,
  metric: 'memory',
  operator: '>',
  threshold: 90,
  durationSec: 120,
  keyword: '',
  eventMatch: '',
  channelIds: '[1]',
  dedupWindowSec: 600,
  silenceStart: '23:00',
  silenceEnd: '07:00',
  notifyRecover: true,
  notifyType: '',
  notifyTarget: '',
  enabled: true,
  createdAt: new Date().toISOString(),
}

/** 取模态面板（<h2> 标题的父 div）。 */
function panelByTitle(title: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: title })
  return heading.parentElement as HTMLElement
}

describe('RuleDialog（mock 假后端）', () => {
  it('① 创建模式：渲染表单字段（名称 + 静默窗口 + 通道复选）', async () => {
    loginMockUser()
    renderWithProviders(<RuleDialog rule={null} channels={seedChannels} onClose={vi.fn()} />)

    const panel = panelByTitle('创建规则')
    // 名称输入（首个 textbox）+ 静默起/止（带 placeholder）渲染。
    expect(within(panel).getAllByRole('textbox').length).toBeGreaterThanOrEqual(3)
    expect(within(panel).getByPlaceholderText('23:00')).toBeInTheDocument()
    expect(within(panel).getByPlaceholderText('07:00')).toBeInTheDocument()
    // 传入的通道作为可勾选项渲染（按 aria-label）。
    expect(within(panel).getByRole('checkbox', { name: '运维 Webhook' })).toBeInTheDocument()
    // 默认触发类型 metric → 阈值/持续 number 字段（spinbutton）出现。
    expect(within(panel).getAllByRole('spinbutton').length).toBeGreaterThanOrEqual(2)
  })

  it('① 编辑模式：种子规则字段回填（名称只读 + 静默窗口 + 通道勾选）', async () => {
    loginMockUser()
    renderWithProviders(<RuleDialog rule={existingRule} channels={seedChannels} onClose={vi.fn()} />)

    const panel = panelByTitle('编辑规则')
    const nameInput = within(panel).getAllByRole('textbox')[0] as HTMLInputElement
    expect(nameInput.value).toBe('内存过载告警')
    expect(nameInput).toBeDisabled() // 编辑时名称/触发类型不可改
    expect((within(panel).getByPlaceholderText('23:00') as HTMLInputElement).value).toBe('23:00')
    expect((within(panel).getByPlaceholderText('07:00') as HTMLInputElement).value).toBe('07:00')
    // channelIds '[1]' → 该通道复选框已勾选。
    expect(within(panel).getByRole('checkbox', { name: '运维 Webhook' })).toBeChecked()
  })

  it('② 创建提交 → POST /alerts/rules 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<RuleDialog rule={null} channels={seedChannels} onClose={onClose} />)

    const panel = panelByTitle('创建规则')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], 'TPS 过低告警')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 编辑提交 → PUT /alerts/rules/:id 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<RuleDialog rule={existingRule} channels={seedChannels} onClose={onClose} />)

    const panel = panelByTitle('编辑规则')
    // 改阈值（首个 spinbutton 即 metric 阈值）后保存，走 update 分支。
    const threshold = within(panel).getAllByRole('spinbutton')[0]
    await userEvent.clear(threshold)
    await userEvent.type(threshold, '95')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 空名称：保存按钮禁用、不触发提交', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<RuleDialog rule={null} channels={seedChannels} onClose={onClose} />)

    const panel = panelByTitle('创建规则')
    // 名称为空 → 必填校验 → 保存禁用。
    expect(within(panel).getByRole('button', { name: '保存' })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('③ 注入 500：提交后对话框不关闭、字段保留（非崩溃，错误态）', async () => {
    // mutateAsync 在 onError(toast) 后仍 reject；吞掉本用例内的未处理拒绝避免污染 runner。
    const onRejection = () => {}
    process.on('unhandledRejection', onRejection)
    try {
      loginMockUser()
      mockInject('post', '/alerts/rules', { kind: 'status', status: 500 })
      const onClose = vi.fn()
      renderWithProviders(<RuleDialog rule={null} channels={seedChannels} onClose={onClose} />)

      const panel = panelByTitle('创建规则')
      const nameInput = within(panel).getAllByRole('textbox')[0]
      await userEvent.type(nameInput, '磁盘告警')
      await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

      // 失败（onError 弹 toast，不调 onClose）→ 对话框仍在、未崩溃、已填值保留。
      await waitFor(() => expect((nameInput as HTMLInputElement).value).toBe('磁盘告警'))
      expect(onClose).not.toHaveBeenCalled()
      expect(screen.getByRole('heading', { name: '创建规则' })).toBeInTheDocument()
    } finally {
      process.off('unhandledRejection', onRejection)
    }
  })
})
