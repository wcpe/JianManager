/**
 * 节点日志采集面板（FR-503）草稿生命周期 DOM 断言。
 *
 * 本面板的编辑是**暂存式**（改完还要点保存才落库），因此草稿的每一个出口都要么正确、
 * 要么可见。覆盖三条红线：
 * - 保存只提交真正改动的键，成功后**只清除仍是本次提交值**的键——
 *   PUT 在途期间的新输入绝不能被一起抹掉（review D-02：旧实现 `setDraft({})` 会）；
 * - 存在未提交改动时给出可见提示（用户能区分「已提交」与「只是敲进来了」）；
 * - 改回原值等于没改：不发请求、不留提示。
 */
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import NodeLogAcqPanel from './NodeLogAcqPanel'

/** 平台/基线取值（与后端 defaultValue 同口径：开关默认、周期与覆盖留空）。 */
const PLATFORM: Record<string, string> = {
  'log.acq.rules_enabled': 'true',
  'log.acq.realtime_tail': 'true',
  'log.acq.gz_import_enabled': 'false',
  'log.acq.gz_scan_interval': '',
  'log.acq.path_overrides': '',
}

/** 构造节点对照视图：四列语义（default/platform/effective/override + overridden）。 */
function acqView(overrides: Record<string, string> = {}) {
  return {
    nodeUuid: 'node-uuid-1',
    nodeName: 'node-one',
    items: Object.keys(PLATFORM).map((key) => ({
      key,
      default: PLATFORM[key],
      platform: PLATFORM[key],
      effective: overrides[key] ?? PLATFORM[key],
      override: overrides[key] ?? '',
      overridden: overrides[key] !== undefined,
    })),
  }
}

const SCAN_KEY = 'log.acq.gz_scan_interval'
const PATH_KEY = 'log.acq.path_overrides'

/**
 * 悬停式 PUT：只有测试显式放行（release）才返回，用来构造「保存中继续输入」的窗口。
 * 放行时把提交值并入 saved ⇒ 后续 GET（mutation 成功后的失效重取）返回覆盖后的视图。
 */
function suspendedPut(
  onBody?: (body: { values: Record<string, string> }) => void,
): { handlers: ReturnType<typeof http.put>[]; release: () => void; saved: () => Record<string, string> } {
  let releaseFn: (() => void) | null = null
  let savedValues: Record<string, string> = {}
  const handler = http.put(API('/nodes/1/settings/log-acquisition'), async ({ request }) => {
    const body = (await request.json()) as { values: Record<string, string> }
    onBody?.(body)
    await new Promise<void>((resolve) => {
      releaseFn = () => {
        savedValues = { ...savedValues, ...body.values }
        resolve()
      }
    })
    return HttpResponse.json(acqView(savedValues))
  })
  return {
    handlers: [handler],
    release: () => releaseFn?.(),
    saved: () => savedValues,
  }
}

describe('NodeLogAcqPanel × 草稿生命周期（FR-503 / review D-02）', () => {
  it('① 只提交改动的键；保存期间改动别的键不会被清掉', async () => {
    loginMockUser()
    const puts: { values: Record<string, string> }[] = []
    const put = suspendedPut((body) => puts.push(body))
    server.use(
      http.get(API('/nodes/1/settings/log-acquisition'), () =>
        HttpResponse.json(acqView(put.saved())),
      ),
      ...put.handlers,
    )

    const user = userEvent.setup()
    renderWithProviders(<NodeLogAcqPanel nodeId={1} online />)

    const scanInput = await screen.findByTestId(`node-log-acq-input-${SCAN_KEY}`)
    const pathInput = screen.getByTestId(`node-log-acq-input-${PATH_KEY}`)

    // 初始无改动 → 没有未保存提示。
    expect(screen.queryByTestId('node-log-acq-unsaved-hint')).not.toBeInTheDocument()

    await user.type(scanInput, '1h')
    // 未保存提示可见（面板的草稿是暂存式，不给提示就分不清「已提交」与「只是敲进来了」）。
    expect(await screen.findByTestId('node-log-acq-unsaved-hint')).toHaveTextContent('1 项未保存改动')

    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    // 只提交真正改动的键：未改的 4 项一个都不提交（避免把「继承平台」固化成节点覆盖）。
    expect(puts[0].values).toEqual({ [SCAN_KEY]: '1h' })

    // PUT 在途期间用户改了**另一个**键 —— 这条新输入必须在保存成功后仍在。
    // 用 fireEvent.change 而非 user.type：userEvent 会把 `{` 解析成键盘描述符（须转义），
    // 而这里只是「在途期间产生一次新输入」，一次性输入与逐键输入的语义等价。
    fireEvent.change(pathInput, { target: { value: '{"inst:1":"proxy.log.0"}' } })
    await act(async () => {
      put.release()
    })

    // 该键未被提交 ⇒ 仍是未保存草稿：输入框保留新值、提示仍然在（1 项）。
    await waitFor(() => expect(pathInput).toHaveValue('{"inst:1":"proxy.log.0"}'))
    expect(screen.getByTestId('node-log-acq-unsaved-hint')).toHaveTextContent('1 项未保存改动')
    // 已提交的键已清出草稿（服务端返回的覆盖值接手展示）。
    // 用 waitFor 包住（2026-10-20 审查）：PUT 成功后该输入框会先回落到旧的查询缓存
    // （platform: ''），要等 invalidateQueries 的重取落地才变回 '1h'。裸断言是无等待快照，
    // 靠 act() 排空微任务侥幸通过，属于 design-system 那条 flaky 的同型写法。
    await waitFor(() => expect(scanInput).toHaveValue('1h'))
  })

  it('② 保存期间对**同一个键**的再次修改也保留（只清仍是提交值的键）', async () => {
    loginMockUser()
    const put = suspendedPut()
    server.use(
      http.get(API('/nodes/1/settings/log-acquisition'), () =>
        HttpResponse.json(acqView(put.saved())),
      ),
      ...put.handlers,
    )

    const user = userEvent.setup()
    renderWithProviders(<NodeLogAcqPanel nodeId={1} online />)
    const scanInput = await screen.findByTestId(`node-log-acq-input-${SCAN_KEY}`)

    await user.type(scanInput, '1h')
    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.getByRole('button', { name: /保存中/ })).toBeDisabled())

    // 在途再次改成 2h（旧实现会在成功回调里 setDraft({})，把这次编辑一起抹掉）。
    await user.clear(scanInput)
    await user.type(scanInput, '2h')
    await act(async () => {
      put.release()
    })

    // 界面保留 2h（= 与已提交的 1h 不同 ⇒ 仍是未保存改动，提示仍在）。
    await waitFor(() => expect(scanInput).toHaveValue('2h'))
    expect(screen.getByTestId('node-log-acq-unsaved-hint')).toBeInTheDocument()
  })

  it('③ 改回原值等于没改：不留提示、不发请求', async () => {
    loginMockUser()
    let putCalls = 0
    server.use(
      http.get(API('/nodes/1/settings/log-acquisition'), () => HttpResponse.json(acqView())),
      http.put(API('/nodes/1/settings/log-acquisition'), () => {
        putCalls++
        return HttpResponse.json(acqView())
      }),
    )

    const user = userEvent.setup()
    renderWithProviders(<NodeLogAcqPanel nodeId={1} online />)
    const scanInput = await screen.findByTestId(`node-log-acq-input-${SCAN_KEY}`)

    await user.type(scanInput, '1h')
    expect(await screen.findByTestId('node-log-acq-unsaved-hint')).toBeInTheDocument()
    await user.clear(scanInput)

    // 判据与提交集同源：改回原值后既没有未保存提示，也不会发一次「没有改动」的请求。
    await waitFor(() =>
      expect(screen.queryByTestId('node-log-acq-unsaved-hint')).not.toBeInTheDocument(),
    )
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(putCalls).toBe(0)
  })
})

/**
 * 回归（2026-10-20 浏览器人工验收发现）：节点面板必须**保存前**就拒绝越界路径。
 *
 * 本面板原先没有任何前端校验，非法值要等 PUT 之后由后端拒绝、再弹 toast；
 * 而设置页（settings-form.validateSettingDraft）早就有同口径校验。同一个非法值
 * 在一个入口当场报错、在另一个入口要先提交一次才失败，属于两处口径漂移。
 * 现两处复用同一个校验函数，本用例锁住「不发请求」这一关键点。
 */
describe('NodeLogAcqPanel × 保存前校验（与设置页同口径）', () => {
  it('绝对路径不发 PUT，直接报错', async () => {
    loginMockUser()
    const puts: { values: Record<string, string> }[] = []
    server.use(
      http.get(API('/nodes/1/settings/log-acquisition'), () => HttpResponse.json(acqView())),
      http.put(API('/nodes/1/settings/log-acquisition'), async ({ request }) => {
        puts.push((await request.json()) as { values: Record<string, string> })
        return HttpResponse.json(acqView())
      }),
    )

    const user = userEvent.setup()
    renderWithProviders(<NodeLogAcqPanel nodeId={1} online />)
    const input = await screen.findByTestId(`node-log-acq-input-${PATH_KEY}`)
    // JSON 文本里的 `{` 是 userEvent 的特殊语法字符，直接用 change 事件赋值。
    fireEvent.change(input, { target: { value: '{"inst:1":"/etc/shadow"}' } })

    await user.click(screen.getByRole('button', { name: /保存/ }))

    await waitFor(() => {
      expect(puts, '越界路径不得发往服务端').toHaveLength(0)
    })
  })

  it('工作目录内的相对路径正常提交', async () => {
    loginMockUser()
    const puts: { values: Record<string, string> }[] = []
    server.use(
      http.get(API('/nodes/1/settings/log-acquisition'), () => HttpResponse.json(acqView())),
      http.put(API('/nodes/1/settings/log-acquisition'), async ({ request }) => {
        puts.push((await request.json()) as { values: Record<string, string> })
        return HttpResponse.json(acqView({ 'log.acq.path_overrides': '{"inst:1":"logs/a.log"}' }))
      }),
    )

    const user = userEvent.setup()
    renderWithProviders(<NodeLogAcqPanel nodeId={1} online />)
    const input = await screen.findByTestId(`node-log-acq-input-${PATH_KEY}`)
    fireEvent.change(input, { target: { value: '{"inst:1":"logs/a.log"}' } })

    await user.click(screen.getByRole('button', { name: /保存/ }))

    await waitFor(() => {
      expect(puts).toHaveLength(1)
    })
    expect(puts[0].values['log.acq.path_overrides']).toBe('{"inst:1":"logs/a.log"}')
  })
})
