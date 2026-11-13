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
 * 配置页（ConfigExplorer → ResourceExplorer config 模式）打开**非配置文件**的行为。
 *
 * 缺陷：配置模式下只要注入 `config`，任何打开的文件都走配置编辑器 →
 * `GET /instances/:id/configs/read?path=logs/latest.log` → 后端 422
 * 「仅允许操作配置文件」。左侧目录树呈现工作目录**全部**文件（含 logs/、jar），
 * 所以从配置页双击日志是常规路径，必须退回普通文件编辑器（`files/read` + CodeEditor）
 * 与文件版本抽屉（`files/versions`），而配置文件仍走配置端点与配置版本抽屉。
 *
 * 断言策略：用 msw `request:start` 记录发出的请求路径，直接钉「发了哪个端点」，
 * 比看错误文案更贴近根因；编辑器类型用「配置编辑器独有的提交说明输入框」作标记。
 */

/** 一条被记录的请求。 */
interface Recorded {
  pathname: string
  /** query 里的 path 参数（无则空串）。 */
  path: string
}

function recordRequests() {
  const requests: Recorded[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (!url.pathname.startsWith('/api/v1/instances/1/')) return
    requests.push({ pathname: url.pathname, path: url.searchParams.get('path') ?? '' })
  }
  server.events.on('request:start', listener)
  return {
    requests,
    only: (pathname: string) => requests.filter((r) => r.pathname === pathname || r.pathname.startsWith(`${pathname}/`)),
    stop: () => server.events.removeListener('request:start', listener),
  }
}

const FILES_READ = '/api/v1/instances/1/files/read'
const CONFIGS_READ = '/api/v1/instances/1/configs/read'
const FILES_VERSIONS = '/api/v1/instances/1/files/versions'
const CONFIGS_VERSIONS = '/api/v1/instances/1/configs/versions'

const LATEST_LOG = 'logs/latest.log'
const LOG_CONTENT = '[12:00:00] [Server thread/INFO]: Done (3.141s)!\n[12:00:01] [Server thread/INFO]: Player joined\n'

/** 工作目录种子：根级配置文件 + logs/latest.log（配置页左树会真实呈现这两类）。 */
function seedFileTree() {
  const file = (name: string, size: number) => ({
    name,
    isDir: false,
    size,
    modTime: 1_700_000_000,
    modeString: 'rw-r--r--',
    writable: true,
    readable: true,
  })
  server.use(
    http.get(API('/instances/:id/files'), ({ request }) => {
      const path = new URL(request.url).searchParams.get('path') ?? ''
      if (path === 'logs') return HttpResponse.json([file('latest.log', LOG_CONTENT.length)])
      if (path === '') {
        return HttpResponse.json([
          { name: 'logs', isDir: true, size: 0, modTime: 1_700_000_000, modeString: 'rwxr-xr-x', writable: true },
          file('server.properties', 100),
        ])
      }
      return HttpResponse.json([])
    }),
    http.get(API('/instances/:id/files/read'), ({ request }) => {
      const path = new URL(request.url).searchParams.get('path') ?? ''
      if (path === LATEST_LOG) return HttpResponse.text(LOG_CONTENT)
      return HttpResponse.json({ error: 'NOT_FOUND', message: '文件不存在' }, { status: 404 })
    }),
  )
}

/** 配置编辑器独有的标记（提交说明输入框），用于判定「这是配置编辑器不是 CodeEditor」。 */
const CONFIG_EDITOR_PLACEHOLDER = '提交说明（可选）'

/** 进入 logs 目录并双击 latest.log。 */
async function openLatestLog(user: ReturnType<typeof userEvent.setup>) {
  // 目录树与列表各有一份「logs」，逐个双击都能完成导航（幂等）。
  for (const row of await screen.findAllByText('logs')) await user.dblClick(row)
  await user.dblClick(await screen.findByText('latest.log'))
}

describe('配置页打开非配置文件', () => {
  let rec: ReturnType<typeof recordRequests>

  beforeEach(() => {
    loginMockUser()
    seedFileTree()
    rec = recordRequests()
  })

  afterEach(() => {
    rec.stop()
  })

  it('打开 logs/latest.log 不请求 configs/read，改用 files/read + 普通编辑器', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ConfigExplorer instanceId={1} />)

    await screen.findByText('server.properties')
    await openLatestLog(user)

    // 关键断言：配置端点一次都不许打（打出去就是 422）。
    expect(rec.only(CONFIGS_READ)).toEqual([])
    // 内容走普通文件读取，且路径正是刚打开的那一个。
    await waitFor(() => expect(rec.only(FILES_READ).map((r) => r.path)).toContain(LATEST_LOG))

    // 普通 CodeEditor 渲染出日志内容，配置编辑器（提交说明输入框）不在场。
    await waitFor(() => {
      const content = container.querySelector('.cm-content')
      expect(content).not.toBeNull()
      expect(content!.textContent).toContain('Done (3.141s)!')
    })
    expect(screen.queryByPlaceholderText(CONFIG_EDITOR_PLACEHOLDER)).toBeNull()
  })

  it('打开 server.properties 仍走 configs/read + 配置编辑器', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConfigExplorer instanceId={1} />)

    await user.dblClick(await screen.findByText('server.properties'))

    await waitFor(() => expect(rec.only(CONFIGS_READ).map((r) => r.path)).toContain('server.properties'))
    // 配置编辑器在场（提交说明输入框 + 校验徽标）。
    expect(await screen.findByPlaceholderText(CONFIG_EDITOR_PLACEHOLDER)).toBeInTheDocument()
    expect(rec.only(FILES_READ)).toEqual([])
  })

  it('非配置文件的历史抽屉走文件版本接口，不请求 configs/versions', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConfigExplorer instanceId={1} />)

    await screen.findByText('server.properties')
    await openLatestLog(user)
    await screen.findByRole('button', { name: '历史版本' })
    await user.click(screen.getByRole('button', { name: '历史版本' }))

    await waitFor(() => expect(rec.only(FILES_VERSIONS).map((r) => r.path)).toContain(LATEST_LOG))
    expect(rec.only(CONFIGS_VERSIONS)).toEqual([])
  })

  it('配置文件的历史抽屉仍走配置版本接口', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConfigExplorer instanceId={1} />)

    await user.dblClick(await screen.findByText('server.properties'))
    await screen.findByPlaceholderText(CONFIG_EDITOR_PLACEHOLDER)
    await user.click(screen.getByRole('button', { name: '历史版本' }))

    await waitFor(() =>
      expect(rec.only(CONFIGS_VERSIONS).map((r) => decodeURIComponent(r.pathname))).toContain(
        `${CONFIGS_VERSIONS}/server.properties`,
      ),
    )
    expect(rec.only(FILES_VERSIONS)).toEqual([])
  })
})
