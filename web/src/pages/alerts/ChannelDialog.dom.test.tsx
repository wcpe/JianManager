import { describe, it, expect, vi } from 'vitest'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import type { AlertChannelInfo } from '@/api/alerts'
import { ChannelDialog } from './ChannelDialog'

/**
 * ChannelDialog 强断言（FR-208 通知通道对话框）：直接渲染组件并传 props（自绘模态，
 * 无 role=dialog，故按标题 <h2> 锁面板再取字段）。覆盖：字段渲染 + 编辑回填、
 * 创建/更新提交成功（POST/PUT 命中、onClose 回调被调）、凭证 ${ENV} 预校验闸、
 * 500 注入显错误态不崩。渲染前 loginMockUser() 让 requireAuth 保护的 /alerts/* 放行。
 *
 * 注：「测试发送」按钮位于 AlertsPage ChannelsTab（页面，非本对话框），不在本文件区域内，
 * 故此处不测；其端点 POST /alerts/channels/:id/test 由页面用例覆盖。
 */

/** 已存在的 webhook 通道（编辑回填靶子）。config.url 为明文（非 ${ENV}），触发预校验闸。 */
const existingWebhook: AlertChannelInfo = {
  id: 1,
  uuid: 'chan-webhook',
  name: '运维 Webhook',
  type: 'webhook',
  enabled: true,
  config: JSON.stringify({ url: 'https://hooks.example.com/ops' }),
  createdAt: new Date().toISOString(),
}

/** 取模态面板（<h2> 标题的父 div）。 */
function panelByTitle(title: string): HTMLElement {
  const heading = screen.getByRole('heading', { name: title })
  return heading.parentElement as HTMLElement
}

describe('ChannelDialog（mock 假后端）', () => {
  it('① 创建模式：默认站内类型渲染名称字段 + 站内提示', async () => {
    loginMockUser()
    renderWithProviders(<ChannelDialog channel={null} onClose={vi.fn()} />)

    const panel = panelByTitle('新建通道')
    // 名称输入 + 启用复选 + 站内类型提示文案。
    expect(within(panel).getAllByRole('textbox').length).toBeGreaterThanOrEqual(1)
    expect(within(panel).getByRole('checkbox', { name: '启用' })).toBeChecked()
    expect(
      within(panel).getByText('站内通知无需外部配置，告警将出现在事件列表中。'),
    ).toBeInTheDocument()
  })

  it('① 编辑模式：webhook 通道回填名称 + URL', async () => {
    loginMockUser()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={vi.fn()} />)

    const panel = panelByTitle('编辑通道')
    const nameInput = within(panel).getAllByRole('textbox')[0] as HTMLInputElement
    expect(nameInput.value).toBe('运维 Webhook')
    // webhook 类型 → URL 字段渲染并回填种子值（明文 url）。
    const urlInput = within(panel).getByPlaceholderText('${JM_DINGTALK_WEBHOOK}') as HTMLInputElement
    expect(urlInput.value).toBe('https://hooks.example.com/ops')
  })

  it('② 创建提交（站内）→ POST /alerts/channels 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

    const panel = panelByTitle('新建通道')
    await userEvent.type(within(panel).getAllByRole('textbox')[0], '钉钉运维群')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 编辑提交：URL 改为 ${ENV} 引用后 → PUT /alerts/channels/:id 成功，onClose 被调', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={onClose} />)

    const panel = panelByTitle('编辑通道')
    const urlInput = within(panel).getByPlaceholderText('${JM_DINGTALK_WEBHOOK}')
    await userEvent.clear(urlInput)
    // userEvent.type 把 { 视作特殊键起始，须 {{ 转义出字面 {；} 本身是字面量。
    await userEvent.type(urlInput, '${{JM_OPS_WEBHOOK}')
    expect((urlInput as HTMLInputElement).value).toBe('${JM_OPS_WEBHOOK}')
    await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('② 凭证预校验闸：明文 URL（非 ${ENV}）→ 显错误且保存禁用，不提交', async () => {
    loginMockUser()
    const onClose = vi.fn()
    renderWithProviders(<ChannelDialog channel={existingWebhook} onClose={onClose} />)

    const panel = panelByTitle('编辑通道')
    // 种子 URL 为明文 https://...，非 ${ENV} 引用 → 校验失败文案 + 保存禁用。
    expect(within(panel).getByText('凭证须以 ${ENV_VAR} 形式引用环境变量')).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: '保存' })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('③ 注入 500：提交后对话框不关闭、字段保留（非崩溃，错误态）', async () => {
    // mutateAsync 在 onError(toast) 后仍 reject；吞掉本用例内的未处理拒绝避免污染 runner。
    const onRejection = () => {}
    process.on('unhandledRejection', onRejection)
    try {
      loginMockUser()
      mockInject('post', '/alerts/channels', { kind: 'status', status: 500 })
      const onClose = vi.fn()
      renderWithProviders(<ChannelDialog channel={null} onClose={onClose} />)

      const panel = panelByTitle('新建通道')
      const nameInput = within(panel).getAllByRole('textbox')[0]
      await userEvent.type(nameInput, '飞书群')
      await userEvent.click(within(panel).getByRole('button', { name: '保存' }))

      // 失败（onError 弹 toast，不调 onClose）→ 对话框仍在、未崩溃、已填值保留。
      await waitFor(() => expect((nameInput as HTMLInputElement).value).toBe('飞书群'))
      expect(onClose).not.toHaveBeenCalled()
      expect(screen.getByRole('heading', { name: '新建通道' })).toBeInTheDocument()
    } finally {
      process.off('unhandledRejection', onRejection)
    }
  })
})
