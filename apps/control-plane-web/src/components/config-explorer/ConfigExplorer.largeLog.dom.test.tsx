import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ConfigExplorer from './ConfigExplorer'

/**
 * 大文件（尤其持续追写的日志）的在线预览。
 *
 * 缺陷：原先任何超过 1 MiB 的文件都直接降级为「文件过大，不支持在线预览」，
 * 而 latest.log 几小时内就会越过该阈值——运维最想看的那份文件反而看不了。
 * 单纯调大阈值也不成立：日志一直在增长，整份拉取与整份渲染都不可持续。
 *
 * 修法：超过阈值时只读**尾部窗口**（后端按行对齐），界面如实标「仅显示末尾」
 * 并强制只读（把尾部写回去会丢掉前面的全部内容）。
 */

const LARGE_SIZE = 1_785_600 // 1.7 MB，即用户报告的那份日志量级
const TAIL_CONTENT = '[23:59:58] [Server thread/INFO]: Player joined\n[23:59:59] [Server thread/INFO]: Done\n'

function seedLargeLog() {
  const file = (name: string, size: number) => ({
    name,
    isDir: false,
    size,
    modTime: 1_700_000_000,
    modeString: 'rw-rw-r--',
    writable: true,
    readable: true,
  })
  server.use(
    http.get(API('/instances/:id/files'), ({ request }) => {
      const path = new URL(request.url).searchParams.get('path') ?? ''
      if (path === 'logs') return HttpResponse.json([file('latest.log', LARGE_SIZE)])
      if (path === '') return HttpResponse.json([file('latest.log', LARGE_SIZE), { ...file('logs', 0), isDir: true }])
      return HttpResponse.json([])
    }),
    // 尾部读取：带 tailBytes 才返回尾部，并回告 X-File-Truncated。
    http.get(API('/instances/:id/files/read'), ({ request }) => {
      const url = new URL(request.url)
      const tail = url.searchParams.get('tailBytes')
      if (!tail) {
        return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '应只请求尾部窗口' }, { status: 422 })
      }
      return new HttpResponse(TAIL_CONTENT, {
        status: 200,
        headers: { 'X-File-Truncated': '1', 'X-File-Total-Size': String(LARGE_SIZE) },
      })
    }),
  )
}

interface Recorded {
  pathname: string
  tailBytes: string | null
}

function recordRequests() {
  const requests: Recorded[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (!url.pathname.startsWith('/api/v1/instances/1/files/read')) return
    requests.push({ pathname: url.pathname, tailBytes: url.searchParams.get('tailBytes') })
  }
  server.events.on('request:start', listener)
  return {
    requests,
    stop: () => server.events.removeListener('request:start', listener),
  }
}

/** 进入 logs 目录并双击 latest.log（目录树与列表各有一份「logs」）。 */
async function openLargeLog(user: ReturnType<typeof userEvent.setup>) {
  for (const row of await screen.findAllByText('logs')) await user.dblClick(row)
  await user.dblClick(await screen.findByText('latest.log'))
}

describe('大日志尾部预览', () => {
  let rec: ReturnType<typeof recordRequests>

  beforeEach(() => {
    loginMockUser()
    seedLargeLog()
    rec = recordRequests()
  })

  afterEach(() => {
    rec.stop()
  })

  it('超过 1 MiB 的日志改为只读尾部窗口，不再报「文件过大」', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ConfigExplorer instanceId={1} />)

    await screen.findByText('server.properties').catch(() => undefined)
    await openLargeLog(user)

    // 关键：必须带 tailBytes 去请求，而不是整份拉（整份拉要么 422 要么把大文件塞进浏览器）。
    await waitFor(() => expect(rec.requests.length).toBeGreaterThan(0))
    expect(rec.requests[0].tailBytes).toBeTruthy()

    // 内容按尾部渲染出来了（不再是「文件过大」占位）。
    await waitFor(() => {
      const content = container.querySelector('.cm-content')
      expect(content).not.toBeNull()
      expect(content!.textContent).toContain('Done')
    })
    expect(screen.queryByText(/文件过大/)).toBeNull()
  })

  it('尾部预览明确提示「仅显示末尾」且不可保存', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConfigExplorer instanceId={1} />)

    await screen.findByText('server.properties').catch(() => undefined)
    await openLargeLog(user)

    const notice = await screen.findByTestId('open-file-tail-notice')
    expect(notice.textContent).toContain('仅显示末尾')

    // 保存按钮必须禁用：尾部只是文件的一部分，写回去会丢掉前面的内容。
    const save = screen.getByRole('button', { name: /保存/ })
    expect(save).toBeDisabled()
  })
})
