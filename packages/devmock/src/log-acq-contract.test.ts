import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach } from 'vitest'
import { server } from './server'
import { resetDb } from './db'
import { clearInjections } from './inject'

/**
 * 节点级日志采集域 mock 的**语义对齐**测试（FR-503 节点端 / FR-504 手动导入归档）。
 *
 * 断言的是「mock 与真后端语义一致」，而不是「端点能返回 200」——这批端点过去在 devmock
 * 里整体缺失（节点详情「日志采集」分段在 dev:mock/e2e 下恒为加载失败），补齐后若语义漂移，
 * 面板的对照列与 422 提示就会与真机不同，dom/e2e 用例随即变成假绿。
 *
 * 锁定四件事：
 * - 四列语义（default/platform/effective/override + overridden）与 `LogAcqSettingItem` 同构；
 * - platform 列**真的**取自 `/settings` 的 `log.acq.*` 行（改平台值 ⇒ 节点视图随之变化）；
 * - 写入校验与真后端同口径（越界键 / 非法布尔 / 非法 duration / 非字符串值 ⇒ 422，
 *   且整体拒绝、不产生部分写入）；
 * - 手动导入归档的理由必填与全节点范围的管理员门（403）。
 */
const BASE = 'http://localhost/api/v1'

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
  clearInjections()
})
afterAll(() => server.close())

let authHeaders: Record<string, string> = {}

/**
 * 每个用例前重新登录取 token —— 不能放 beforeAll：`afterEach` 的 `resetDb()` 会连
 * `sessions` 集合一起重播，前一例的会话随之失效。
 */
async function loginAs(username: string, password: string): Promise<Record<string, string>> {
  const res = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  const body = (await res.json()) as { accessToken: string }
  return { Authorization: `Bearer ${body.accessToken}` }
}

beforeEach(async () => {
  authHeaders = await loginAs('admin', 'admin123')
})

/** 视图形态（只声明断言需要的字段）。 */
interface AcqItem {
  key: string
  default: string
  platform: string
  effective: string
  override: string
  overridden: boolean
}
interface AcqView {
  nodeUuid?: string
  nodeName?: string
  items: AcqItem[]
}

async function getView(nodeId = 1): Promise<AcqView> {
  const res = await fetch(`${BASE}/nodes/${nodeId}/settings/log-acquisition`, {
    headers: authHeaders,
  })
  expect(res.status).toBe(200)
  return (await res.json()) as AcqView
}

async function putJson(
  path: string,
  body: unknown,
  headers = authHeaders,
): Promise<{ status: number; data: Record<string, unknown> }> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'PUT',
    headers: { ...headers, 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return { status: res.status, data: (await res.json()) as Record<string, unknown> }
}

async function deleteJson(
  path: string,
  headers = authHeaders,
): Promise<{ status: number; data: AcqView }> {
  const res = await fetch(`${BASE}${path}`, { method: 'DELETE', headers })
  return { status: res.status, data: (await res.json()) as AcqView }
}

async function postJson(
  path: string,
  body?: unknown,
  headers = authHeaders,
): Promise<{ status: number; data: Record<string, unknown> }> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { ...headers, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return { status: res.status, data: (await res.json()) as Record<string, unknown> }
}

const item = (view: AcqView, key: string): AcqItem => {
  const found = view.items.find((i) => i.key === key)
  expect(found, `视图应含 ${key}`).toBeTruthy()
  return found!
}

describe('devmock × 节点级日志采集（FR-503）', () => {
  it('① 返回四列对照视图：默认/平台/生效/覆盖，未覆盖项继承平台', async () => {
    const view = await getView()
    expect(view.nodeUuid).toBe('node-alpha')
    expect(view.nodeName).toBe('alpha')
    expect(view.items.map((i) => i.key)).toEqual([
      'log.acq.rules_enabled',
      'log.acq.realtime_tail',
      'log.acq.gz_import_enabled',
      'log.acq.gz_scan_interval',
      'log.acq.path_overrides',
    ])
    const rules = item(view, 'log.acq.rules_enabled')
    // 未覆盖：effective 继承 platform，override 为空。
    expect(rules).toMatchObject({
      default: 'true',
      platform: 'true',
      effective: 'true',
      override: '',
      overridden: false,
    })
  })

  it('② PUT 写节点覆盖并在响应与后续 GET 中保持（effective=override）', async () => {
    const { status, data } = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.gz_scan_interval': '1h' },
    })
    expect(status).toBe(200)
    const scanned = item(data as unknown as AcqView, 'log.acq.gz_scan_interval')
    expect(scanned).toMatchObject({ override: '1h', effective: '1h', overridden: true })

    // 后续 GET 必须保持（真后端把覆盖落库，不是只回显本次请求）。
    const view = await getView()
    expect(item(view, 'log.acq.gz_scan_interval')).toMatchObject({
      override: '1h',
      effective: '1h',
      overridden: true,
    })
    // 未被写入的项不受影响。
    expect(item(view, 'log.acq.realtime_tail').overridden).toBe(false)
  })

  it('③ 写入非法值一律 422，且整体拒绝、不产生部分写入', async () => {
    // 越界键（本端点不承担「任意键的节点级覆盖」）。
    const unknown = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.level': 'debug' },
    })
    expect(unknown.status).toBe(422)

    // 布尔键只接受 true|false。
    const badBool = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.rules_enabled': 'yes' },
    })
    expect(badBool.status).toBe(422)

    // 周期：空合法；0/负数/乱写非法。
    for (const bad of ['0', '0s', '-1h', 'abc']) {
      const res = await putJson('/nodes/1/settings/log-acquisition', {
        values: { 'log.acq.gz_scan_interval': bad },
      })
      expect(res.status, `周期 ${bad} 应被拒`).toBe(422)
    }

    // 路径覆盖：须为 JSON 对象且**值为字符串**（与后端 map[string]string 同口径）。
    const arrayJson = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.path_overrides': '[]' },
    })
    expect(arrayJson.status).toBe(422)
    const numericValue = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.path_overrides': '{"inst:1":1}' },
    })
    expect(numericValue.status).toBe(422)

    // 「合法键 + 非法键」同批提交：整体拒绝，合法项也不得落库（真后端不做部分写入）。
    const mixed = await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.realtime_tail': 'false', 'log.acq.rules_enabled': 'nope' },
    })
    expect(mixed.status).toBe(422)
    const view = await getView()
    expect(item(view, 'log.acq.realtime_tail').overridden).toBe(false)
    expect(item(view, 'log.acq.rules_enabled').overridden).toBe(false)
  })

  it('④ DELETE 支持只清一项与清全部（幂等）', async () => {
    await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.realtime_tail': 'false', 'log.acq.gz_scan_interval': '30m' },
    })

    const single = await deleteJson(
      '/nodes/1/settings/log-acquisition?key=log.acq.gz_scan_interval',
    )
    expect(single.status).toBe(200)
    expect(item(single.data, 'log.acq.gz_scan_interval').overridden).toBe(false)
    // 只清单项：另一项仍被覆盖。
    expect(item(single.data, 'log.acq.realtime_tail')).toMatchObject({
      override: 'false',
      effective: 'false',
      overridden: true,
    })

    const all = await deleteJson('/nodes/1/settings/log-acquisition')
    expect(all.status).toBe(200)
    expect(all.data.items.every((i) => !i.overridden)).toBe(true)

    // 非法 key 参数 422；重复删除仍 200（幂等）。
    const badKey = await deleteJson('/nodes/1/settings/log-acquisition?key=log.level')
    expect(badKey.status).toBe(422)
    expect((await deleteJson('/nodes/1/settings/log-acquisition')).status).toBe(200)
  })

  it('⑤ platform 列来自 /settings 的 log.acq.* 行：改平台值 ⇒ 节点视图随之变化', async () => {
    // 通过平台设置端点改（设置页走的就是这条），节点视图的 platform 列必须跟着变——
    // 否则「界面显示的生效值」与真机分叉，节点面板会展示一个不存在的平台默认。
    const updated = await putJson('/settings', {
      values: { 'log.acq.gz_scan_interval': '2h' },
    })
    expect(updated.status).toBe(200)

    const view = await getView()
    expect(item(view, 'log.acq.gz_scan_interval').platform).toBe('2h')
    // 该节点未覆盖 ⇒ effective 继承新的平台值。
    expect(item(view, 'log.acq.gz_scan_interval').effective).toBe('2h')

    // 写入节点覆盖后，覆盖压过平台值。
    await putJson('/nodes/1/settings/log-acquisition', {
      values: { 'log.acq.gz_scan_interval': '10m' },
    })
    const overridden = await getView()
    expect(item(overridden, 'log.acq.gz_scan_interval')).toMatchObject({
      platform: '2h',
      override: '10m',
      effective: '10m',
    })
  })

  it('⑥ 节点不存在 → 404；写端点非平台管理员 → 403', async () => {
    const missing = await fetch(`${BASE}/nodes/99999/settings/log-acquisition`, {
      headers: authHeaders,
    })
    expect(missing.status).toBe(404)

    const operator = await loginAs('operator', 'op123456')
    // 读放行（节点读权限），写仅平台管理员（同真后端 UpdateNode/DeleteNode）。
    const read = await fetch(`${BASE}/nodes/1/settings/log-acquisition`, { headers: operator })
    expect(read.status).toBe(200)
    const write = await putJson(
      '/nodes/1/settings/log-acquisition',
      { values: { 'log.acq.realtime_tail': 'false' } },
      operator,
    )
    expect(write.status).toBe(403)
    const clear = await deleteJson('/nodes/1/settings/log-acquisition', operator)
    expect(clear.status).toBe(403)
  })
})

describe('devmock × 手动导入归档（FR-504）', () => {
  it('⑦ 理由必填；成功后只回报「已触发」而不承诺完成计数', async () => {
    const noReason = await postJson('/nodes/1/log-runtime/ingest/import-archives', {
      storageNamespace: '',
    })
    expect(noReason.status).toBe(400)
    expect(noReason.data.error).toBe('REASON_REQUIRED')

    const blank = await postJson('/nodes/1/log-runtime/ingest/import-archives', {
      storageNamespace: '',
      reason: '   ',
    })
    expect(blank.status).toBe(400)

    const ok = await postJson('/nodes/1/log-runtime/ingest/import-archives', {
      storageNamespace: '',
      reason: '补采轮转归档',
    })
    expect(ok.status).toBe(200)
    expect(ok.data.state).toBe('imported')
    expect(String(ok.data.message)).toContain('已触发归档导入扫描')
    // 不返回计数：导入分批且可能跨多个采集轮，计数以 Worker 日志为准。
    expect(ok.data).not.toHaveProperty('imported')
  })

  it('⑧ 空范围 = 全节点导入，仅平台管理员；指定实例范围须为 inst:<id> 形态', async () => {
    const operator = await loginAs('operator', 'op123456')
    const forbidden = await postJson(
      '/nodes/1/log-runtime/ingest/import-archives',
      { storageNamespace: '', reason: '全量补采' },
      operator,
    )
    expect(forbidden.status).toBe(403)

    // 指定单个实例的日志源：普通角色可走这条（后端按实例授权）。
    const scoped = await postJson(
      '/nodes/1/log-runtime/ingest/import-archives',
      { storageNamespace: 'inst:317', reason: '补采该实例归档' },
      operator,
    )
    expect(scoped.status).toBe(200)

    // 形态非法（完整源键 / 纯数字）⇒ 400：真机上传完整源键会扫不到任何源却看着"成功"。
    for (const bad of ['317', 'inst:', 'inst:abc']) {
      const res = await postJson('/nodes/1/log-runtime/ingest/import-archives', {
        storageNamespace: bad,
        reason: 'x',
      })
      expect(res.status, `范围 ${bad} 应被拒`).toBe(400)
    }
  })
})
