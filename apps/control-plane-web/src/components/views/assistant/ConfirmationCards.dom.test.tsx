import { beforeAll, describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { ConfirmationCards } from '@/components/views/assistant/ConfirmationCards'

/** 
 * 确认卡的两条可访问性/时效验收（§7）：
 *
 *  1. **过期即禁用**：`expiresAt` 已过期 → 允许/拒绝两键都禁用，并说明「该确认已过期」。
 *     不禁用会让人以为还能点，点下去必然失败，白跑一趟。
 *  2. **键盘可达**：两键都能 Tab 到、能聚焦。
 *
 * 【为什么能在这里验，而不是非跑通 tool_calls 不可】
 * 组件的数据源是 `GET /assistant/runs/:id/confirmations`，与「票据是怎么产生的」无关。
 * 用 MSW 直接造一条过期票据，就能把「过期渲染 + 禁用」这条路径测干净——
 * 造一张真票据反而更绕，且要依赖 Worker 与工具调用链路。
 */
const testI18n = i18n.createInstance()
void testI18n
  .use(initReactI18next)
  .init({ lng: 'zh-CN', fallbackLng: 'zh-CN', resources: { 'zh-CN': { translation: {} } } })

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

/** 造一张票据；`minutesFromNow` 为负表示已过期。 */
function ticket(id: number, minutesFromNow: number) {
  return {
    id,
    toolName: 'bot_send_command',
    operation: '让 Bot 下发命令',
    args: { botId: 1, command: 'say hi' },
    targetType: 'bot',
    targetId: '1',
    expiresAt: new Date(Date.now() + minutesFromNow * 60_000).toISOString(),
    risk: 'write',
  }
}

function renderCards(items: ReturnType<typeof ticket>[]) {
  server.use(
    http.get(API('/assistant/runs/:runId/confirmations'), () =>
      HttpResponse.json({ items }),
    ),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <I18nextProvider i18n={testI18n}>
        <ConfirmationCards runId={1} />
      </I18nextProvider>
    </QueryClientProvider>,
  )
}

describe('确认卡 · 过期即禁用', () => {
  it('过期票据：两个按钮都禁用，并说明原因', async () => {
    renderCards([ticket(1, -5)]) // 5 分钟前就过期了

    // 等票据渲染出来（按钮文案来自 i18n 默认值）
    const approve = await screen.findByRole('button', { name: /允许|批准|同意/ })
    const reject = await screen.findByRole('button', { name: /拒绝/ })

    expect(approve, '过期的「允许」必须禁用').toBeDisabled()
    expect(reject, '过期的「拒绝」也必须禁用').toBeDisabled()

    // 说明原因：不能只是灰掉，否则用户不知道该怎么办
    expect(
      screen.getByText(/该确认已过期/),
      '过期必须给出原因文案',
    ).toBeTruthy()
  })

  it('对照：未过期票据的按钮可用（证明上面的禁用不是测量失灵）', async () => {
    renderCards([ticket(2, 5)])
    const approve = await screen.findByRole('button', { name: /允许|批准|同意/ })
    expect(approve, '未过期的「允许」应可点击').not.toBeDisabled()
  })
})

describe('确认卡 · 键盘可达', () => {
  it('两个按钮都能获得焦点（可 Tab 到）', async () => {
    renderCards([ticket(3, 5)])
    const approve = await screen.findByRole('button', { name: /允许|批准|同意/ })
    const reject = await screen.findByRole('button', { name: /拒绝/ })

    approve.focus()
    expect(document.activeElement, '「允许」应可获得焦点').toBe(approve)

    reject.focus()
    expect(document.activeElement, '「拒绝」应可获得焦点').toBe(reject)

    /* 
      「能 Tab 到」的判据放在**按钮自身**：原生 <button> 默认 tabIndex=0，
      即进入 Tab 序列；被 disabled 时才退出序列。这里断言的是「未被禁用且
      tabIndex 不小于 0」，比「从文档起按一次 Tab 恰好落在它上面」稳定得多——
      后者依赖页面上有多少个前置可聚焦元素，加个链接就会假红（本条初版正是如此）。
    */
    for (const btn of [approve, reject]) {
      expect(btn, '确认卡按钮不应被禁用').not.toBeDisabled()
      expect(btn.tabIndex, '确认卡按钮应在 Tab 序列内').toBeGreaterThanOrEqual(0)
    }
  })
})
