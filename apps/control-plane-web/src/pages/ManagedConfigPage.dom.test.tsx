import { describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ManagedConfigPage from './ManagedConfigPage'
import {
  releaseGuard,
  isPushTerminal,
  isTargetFailure,
  parseEffectiveJson,
  pushStatusLevel,
  targetStateLevel,
  validateErrors,
  type ManagedConfigLayer,
  type ManagedPushSession,
  type ManagedPushTarget,
} from '@/api/managedConfig'

/**
 * 探针托管配置与下发页 DOM 测试。
 *
 * 盯住的是四处「写错了会直接误导运维」的地方：
 * 1. **禁档键的拒绝原因原样可读**——后端 message 带着具体键名，界面二次包装就会把它埋掉；
 * 2. **409 必须给「重新加载」**——把它当普通失败让人「重试」，只会覆盖别人的修改；
 * 3. **放行动作的条件与后果写明**——灰按钮不解释等于没说；
 * 4. **回滚必须二次确认且说明是重推冻结快照**——不是「按现在的层重新合并」。
 *
 * 另有一组纯函数断言（releaseGuard / state level / effectiveJson），它们承载的是
 * 「后端状态机怎么判，前端就怎么显示」的映射，写错了不会报错但会显示错。
 */

/** 构造一层配置。 */
function layerOf(overrides: Partial<ManagedConfigLayer> = {}): ManagedConfigLayer {
  return {
    id: 3,
    scope: 'global',
    scopeRefId: 0,
    config: '{"collect-period-ticks":10}',
    version: 1,
    createdBy: 1,
    createdAt: '2026-11-16T10:00:00Z',
    updatedAt: '2026-11-16T10:00:00Z',
    ...overrides,
  }
}

/** 构造一次下发会话。 */
/**
 * 配置键目录样例：三种 kind + 一个禁止档键。
 *
 * 覆盖断言需要的最小集合：
 * - bool  → 开关控件
 * - int   → 数字输入 + 边界提示
 * - object(alert.rules) → 规则编辑器
 * - forbidden → 只读行（不渲染可编辑控件）
 */
const CATALOG_FIXTURE: ManagedKeyMeta[] = [
  {
    key: 'debug',
    kind: 'bool',
    group: 'collect',
    tier: 'safe',
    title: '调试日志',
    description: '是否输出 DEBUG 级日志。',
    default: false,
  },
  {
    key: 'collect-period-ticks',
    kind: 'int',
    group: 'collect',
    tier: 'guarded',
    title: '指标采集周期',
    description: '多久采集一次指标。',
    default: 100,
    min: 20,
    unit: 'tick',
    boundsHint: '过小会放大指标量并压主线程',
  },
  {
    key: 'alert.rules',
    kind: 'object',
    group: 'alert',
    tier: 'guarded',
    title: '告警规则',
    description: '每条规则含阈值、持续周期、级别与启用。',
    fields: [
      { key: 'threshold', kind: 'float', group: 'alert', tier: 'guarded', title: '阈值', description: '', default: 0 },
      { key: 'sustain-cycles', kind: 'int', group: 'alert', tier: 'guarded', title: '持续周期', description: '', default: 3, min: 1 },
      { key: 'level', kind: 'string', group: 'alert', tier: 'guarded', title: '级别', description: '', default: 'WARN', options: ['WARN', 'CRITICAL'] },
      { key: 'enabled', kind: 'bool', group: 'alert', tier: 'guarded', title: '启用', description: '', default: true },
    ],
  },
  {
    key: 'bridge.token',
    kind: 'string',
    group: 'local',
    tier: 'forbidden',
    title: '插件桥凭据',
    description: '凭证类键。',
  },
]

function sessionOf(overrides: Partial<ManagedPushSession> = {}): ManagedPushSession {
  return {
    id: 9,
    kind: 'push',
    scope: 'global',
    scopeRefId: 0,
    configVersion: 4,
    batchSize: 2,
    failureRateThreshold: 20,
    ackTimeoutSec: 30,
    status: 'running',
    plannedCount: 6,
    appliedCount: 2,
    failedCount: 0,
    timeoutCount: 0,
    skippedCount: 0,
    pendingCount: 4,
    currentBatch: 1,
    totalBatches: 3,
    createdBy: 1,
    createdAt: '2026-11-16T10:00:00Z',
    updatedAt: '2026-11-16T10:00:00Z',
    ...overrides,
  }
}

/** 构造一个逐台目标。 */
function targetOf(overrides: Partial<ManagedPushTarget> = {}): ManagedPushTarget {
  return {
    id: 1,
    sessionId: 9,
    instanceId: 11,
    instanceName: '生存服',
    nodeId: 2,
    batchNo: 1,
    state: 'applied',
    desiredVersion: 4,
    baseVersion: 3,
    ackVersion: 4,
    attempts: 1,
    createdAt: '2026-11-16T10:00:00Z',
    updatedAt: '2026-11-16T10:00:00Z',
    ...overrides,
  }
}

/** 注册配置层相关的全部端点（layer 为 null 表示该层尚未配置）。 */
function useLayerEndpoints(options: {
  layer?: ManagedConfigLayer | null
  put?: () => Response
  validate?: () => Response
  impact?: () => Response
  /** 下发会话列表；不传给一条默认会话，避免页面挂载时的列表请求变 unhandled。 */
  pushes?: ManagedPushSession[]
} = {}) {
  // `current` 模拟服务端真实状态：保存成功后版本要真的递增，页面重新取数才看得到 v2。
  // 早先这里始终返回同一个固定对象，导致「保存后版本回到 v2」这类断言永远不可能通过，
  // 看起来像实现坏了，实际是假后端不会演进。
  let current = options.layer === undefined ? layerOf() : options.layer
  server.use(
    // 配置键目录：可视化编辑器的数据源。给一份最小但完整的样例（含三种 kind 与一个禁止键），
    // 让「控件按类型渲染」「禁止键只读」这两类断言有据可依。
    http.get(API('/managed-configs/catalog'), () =>
      HttpResponse.json({
        items: CATALOG_FIXTURE,
        groups: {
          collect: '采集节拍',
          alert: '告警',
          local: '仅本地可改（禁止远程写入）',
        },
      }),
    ),
    // 实例搜索：实例作用域下「只有一台时自动选中」靠它判断总数。
    // 默认返回两台（不触发自动选中），需要验证自动选中的用例自行覆盖成一台。
    http.get(API('/instances/search'), () =>
      HttpResponse.json({
        items: [
          { id: 1, name: 'probe-a', nodeId: 1, status: 'running', type: 'minecraft_java', role: 'backend' },
          { id: 2, name: 'probe-b', nodeId: 1, status: 'running', type: 'minecraft_java', role: 'backend' },
        ],
        total: 2,
        page: 1,
        pageSize: 2,
      }),
    ),
    http.get(API('/managed-configs'), () => HttpResponse.json({ items: current ? [current] : [] })),
    http.get(API('/managed-configs/layer'), () =>
      current
        ? HttpResponse.json(current)
        : HttpResponse.json({ error: 'NOT_FOUND', message: '该层尚未配置' }, { status: 404 }),
    ),
    http.post(API('/managed-configs/validate'), () =>
      options.validate ? options.validate() : HttpResponse.json({ Errors: [] }),
    ),
    http.post(API('/managed-configs/impact'), () =>
      options.impact
        ? options.impact()
        : HttpResponse.json({
            affectedInstances: 12,
            totalInstances: 15,
            guardedKeys: ['collect-period-ticks'],
            forbiddenKeys: [],
            metricMultiplier: 2,
            defaultCollectPeriodTicks: 20,
            effectiveCollectPeriodTicks: 10,
            notes: ['采集周期 10 tick → 指标量约为默认的 2.00 倍'],
          }),
    ),
    http.put(API('/managed-configs'), () => {
      if (options.put) return options.put()
      // 成功路径：服务端版本自增，后续 GET 才会返回新版本。
      const next = layerOf({ version: (current?.version ?? 0) + 1 })
      current = next
      return HttpResponse.json(next)
    }),
    // 页面挂载即拉取下发会话列表（「下发会话」页签的数据源）。
    // 测试只关心配置层时也必须给出这条，否则 MSW 会报 unhandled request 并中断后续交互。
    http.get(API('/managed-pushes'), () =>
      HttpResponse.json({ items: options.pushes ?? [sessionOf()], total: (options.pushes ?? [sessionOf()]).length }),
    ),
  )
}

describe('托管配置层：校验 / 影响预演 / 保存', () => {
  it('保存成功后提示并回到最新版本', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints()

    renderWithProviders(<ManagedConfigPage />)

    // 已存在的层：显示当前版本，编辑器内容取自服务端原文。
    //
    // 必须在 waitFor 里**重新查询**节点：数据到达时 React 会替换这个 span，
    // 早先 findByTestId 拿到的旧节点引用不会跟着更新，读它只会永远看到「尚未配置」。
    await waitFor(() => {
      const el = screen.getByTestId('managed-layer-version')
      expect(el.textContent, `实际版本文本="${el.textContent}"`).toContain('当前版本 v1')
    })
    // 编辑器已改为按 catalog 元数据渲染的可视化控件：数值键渲染成数字输入，
    // 其值取自服务端原文（10）。这比断言"文本框里含某字符串"更能证明数据真的落到了控件上。
    //
    // 同时验证越界提示：fixture 里 min=20 而服务端值是 10，界面必须把越界说出来——
    // 否则用户看到 10 会以为它是合法的，直到下发被探针拒才发现。
    const ticks = await screen.findByTestId('managed-field-input-collect-period-ticks')
    expect((ticks as HTMLInputElement).value).toBe('10')
    expect(screen.getByTestId('managed-field-bounds-collect-period-ticks')).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('managed-layer-save'))

    // 保存成功后父级重新取数，版本号回到服务端真源。
    // 先把文本取出来再断言：断言放在 waitFor 里失败时只会抛 jest-dom 的通用信息，
    // 看不到页面上究竟显示成了什么。
    let versionText = ''
    await waitFor(
      () => {
        versionText = screen.getByTestId('managed-layer-version').textContent ?? ''
        expect(versionText).toContain('v2')
      },
      { timeout: 2000 },
    ).catch(() => {
      const body = document.body.textContent ?? ''
      throw new Error(
        `保存后版本未更新（实际="${versionText}"）。页面文本: ${body.replace(/\s+/g, ' ').slice(0, 500)}`,
      )
    })
  })

  it('后端 FORBIDDEN 拒绝原因原样展示（含具体键名）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const message = '托管配置含禁止远程写入的键: 键 "bridge.token" 属于禁止远程写入的监听面/凭证（整包拒绝）'
    let putCount = 0
    useLayerEndpoints({
      put: () => {
        putCount += 1
        return HttpResponse.json({ error: 'INVALID_CONFIG', message }, { status: 400 })
      },
    })

    renderWithProviders(<ManagedConfigPage />)
    // 等保存按钮真正可用：层数据到达前它是禁用的（否则报错会随数据到达的重挂一起消失）。
    await waitFor(() => expect(screen.getByTestId('managed-layer-save')).toBeEnabled())
    fireEvent.click(screen.getByTestId('managed-layer-save'))
    await waitFor(() => expect(putCount).toBe(1))

    const alert = await screen
      .findByTestId('managed-layer-save-error', {}, { timeout: 2000 })
      .catch(() => {
        const html = document.body.innerHTML
        const hasMsg = html.includes('bridge.token')
        throw new Error(`未出现错误提示。消息是否在 DOM 中=${hasMsg}；testid 出现次数=${(html.match(/managed-layer-save-error/g) ?? []).length}`)
      })
    // 原样可读：键名必须出现在页面上，运维据此才知道改哪一个键。
    expect(within(alert).getByText(message)).toBeInTheDocument()
  })

  it('版本冲突（409）提示该层已被他人更新并给出重新加载', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints({
      put: () =>
        HttpResponse.json(
          { error: 'VERSION_CONFLICT', message: 'version mismatch: 期望 1 实际 2' },
          { status: 409 },
        ),
    })

    renderWithProviders(<ManagedConfigPage />)
    await screen.findByTestId('managed-layer-config')
    // 层数据到达前保存按钮是禁用的；不等就绪直接点，报错会随重挂一起消失。
    await waitFor(() => expect(screen.getByTestId('managed-layer-save')).toBeEnabled())
    fireEvent.click(screen.getByTestId('managed-layer-save'))

    const conflict = await screen.findByTestId('managed-layer-conflict')
    expect(within(conflict).getByText('该层已被他人更新，请重新加载后再改，避免覆盖别人的修改。')).toBeInTheDocument()
    // 必须给「重新加载」而不是「重试」：重试只会覆盖别人的修改。
    expect(within(conflict).getByTestId('managed-layer-reload')).toBeInTheDocument()
  })

  it('影响预演展示受影响台数、倍率与危险档键', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints()

    renderWithProviders(<ManagedConfigPage />)
    await screen.findByTestId('managed-layer-config')
    // 层数据到达前保存按钮是禁用的；不等就绪直接点，报错会随重挂一起消失。
    await waitFor(() => expect(screen.getByTestId('managed-layer-save')).toBeEnabled())
    fireEvent.click(screen.getByTestId('managed-impact-run'))

    expect(await screen.findByTestId('managed-impact-affected')).toHaveTextContent('12 / 15 台')
    expect(screen.getByTestId('managed-impact-multiplier')).toHaveTextContent('2 倍')
    // 危险档键必须醒目提示：它能存能下发，但会改变数据量。
    const guarded = screen.getByTestId('managed-impact-guarded')
    expect(within(guarded).getByText('collect-period-ticks')).toBeInTheDocument()
  })

  it('预演发现禁档键时明确拦下保存', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints({
      impact: () =>
        HttpResponse.json({
          affectedInstances: 0,
          totalInstances: 15,
          guardedKeys: [],
          forbiddenKeys: ['bridge.url'],
          metricMultiplier: 1,
          defaultCollectPeriodTicks: 20,
          effectiveCollectPeriodTicks: 20,
          notes: ['存在禁止远程写入的键，本次不可下发'],
        }),
    })

    renderWithProviders(<ManagedConfigPage />)
    await screen.findByTestId('managed-layer-config')
    // 层数据到达前保存按钮是禁用的；不等就绪直接点，报错会随重挂一起消失。
    await waitFor(() => expect(screen.getByTestId('managed-layer-save')).toBeEnabled())
    fireEvent.click(screen.getByTestId('managed-impact-run'))

    const forbidden = await screen.findByTestId('managed-impact-forbidden')
    expect(within(forbidden).getByText('bridge.url')).toBeInTheDocument()
    expect(screen.getByTestId('managed-save-blocked')).toHaveTextContent(
      '存在禁止远程写入的键，请先移除后再保存。',
    )
    expect(screen.getByTestId('managed-layer-save')).toBeDisabled()
  })

  it('实时校验失败时逐条列出后端原因并禁用保存', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints({
      validate: () =>
        HttpResponse.json({ Errors: ['键 "bridge.token" 属于禁止远程写入的监听面/凭证（整包拒绝）'] }),
    })

    renderWithProviders(<ManagedConfigPage />)

    const errors = await screen.findByTestId('managed-validate-errors')
    expect(
      within(errors).getByText('键 "bridge.token" 属于禁止远程写入的监听面/凭证（整包拒绝）'),
    ).toBeInTheDocument()
    expect(screen.getByTestId('managed-layer-save')).toBeDisabled()
  })

  it('该层尚未配置时以版本 0 起步（404 不是错误）', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints({ layer: null })

    renderWithProviders(<ManagedConfigPage />)

    expect(await screen.findByTestId('managed-layer-version')).toHaveTextContent('该层尚未配置')
    expect(screen.getByTestId('managed-layers-empty')).toBeInTheDocument()
  })

  it('节点作用域未选目标时不挂编辑器', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints()

    renderWithProviders(<ManagedConfigPage />)
    await screen.findByTestId('managed-layer-editor')

    await user.selectOptions(screen.getByTestId('managed-scope'), 'node')

    expect(await screen.findByTestId('managed-layer-need-target')).toBeInTheDocument()
    expect(screen.queryByTestId('managed-layer-editor')).not.toBeInTheDocument()
  })
})

describe('下发会话：推进 / 放行 / 目标 / 回滚', () => {
  /** 注册会话列表与目标端点。 */
  function usePushEndpoints(session: ManagedPushSession, targets: ManagedPushTarget[] = [targetOf()]) {
    // 会话详情取自列表端点，故列表要能演进：放行后必须让它返回推进后的会话，
    // 否则页面永远显示旧批次（假后端不会演进，断言就成了「实现坏了」的假象）。
    let current = session
    const advance = (next: ManagedPushSession) => {
      current = next
    }
    server.use(
      http.get(API('/managed-configs'), () => HttpResponse.json({ items: [] })),
      http.get(API('/managed-configs/layer'), () => HttpResponse.json({}, { status: 404 })),
      http.get(API('/managed-pushes'), () =>
        HttpResponse.json({ items: [current], total: 1, page: 1, pageSize: 20 }),
      ),
      http.post(API('/managed-pushes/:id/targets'), () =>
        HttpResponse.json({ items: targets, total: targets.length, page: 1, pageSize: 20 }),
      ),
    )
    return { advance }
  }

  it('等待放行时按「当前批 N/M，已生效 X 台」展示并可见', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const session = sessionOf({ status: 'awaiting_release', currentBatch: 1, totalBatches: 3, appliedCount: 2 })
    usePushEndpoints(session)

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))

    const release = await screen.findByTestId('managed-release')
    expect(release).toHaveTextContent('放行下一批（当前批 1/3，已生效 2 台）')
    expect(release).toBeEnabled()
  })

  it('放行成功后会话推进到下一批', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const session = sessionOf({ status: 'awaiting_release', currentBatch: 1, totalBatches: 3, appliedCount: 2 })
    const { advance } = usePushEndpoints(session)
    server.use(
      http.post(API('/managed-pushes/:id/release'), () => {
        // 放行成功后服务端把会话推进到第二批；列表端点随之返回新状态。
        const next = sessionOf({ status: 'running', currentBatch: 2, appliedCount: 2 })
        advance(next)
        return HttpResponse.json(next)
      }),
    )

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))
    // 先等放行按钮可用：详情数据到达前它是禁用的，点在禁用按钮上不会发请求。
    await waitFor(() => expect(screen.getByTestId('managed-release')).toBeEnabled())
    fireEvent.click(screen.getByTestId('managed-release'))

    await waitFor(() => expect(screen.getByTestId('managed-push-batch')).toHaveTextContent('第 2 / 3 批'))
  })

  it('状态不是「等待放行」时禁用放行并说明原因', async () => {
    loginMockUser()
    const user = userEvent.setup()
    usePushEndpoints(sessionOf({ status: 'running' }))

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))

    expect(await screen.findByTestId('managed-release')).toBeDisabled()
    expect(screen.getByTestId('managed-release-blocked')).toHaveTextContent(
      '当前批还没全部终结，暂不能放行下一批。',
    )
  })

  it('逐台目标列表显示实例、状态、错误，并可展开生效值', async () => {
    loginMockUser()
    const user = userEvent.setup()
    usePushEndpoints(sessionOf(), [
      targetOf({
        id: 1,
        instanceName: '生存服',
        state: 'applied',
        effectiveJson: '{"collect-period-ticks":10}',
      }),
      targetOf({
        id: 2,
        instanceName: '资源服',
        state: 'rejected',
        // 版本冲突是 rejected 的 error 前缀，不是 result 取值。
        errorKind: 'VERSION_CONFLICT',
        error: 'conflict: base 4 vs 5',
      }),
    ])

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))

    const rows = await screen.findAllByTestId('managed-target-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('生存服')).toBeInTheDocument()
    expect(within(rows[1]).getByText('资源服')).toBeInTheDocument()
    expect(within(rows[1]).getByText('整包被拒')).toBeInTheDocument()
    expect(within(rows[1]).getByText('conflict: base 4 vs 5')).toBeInTheDocument()

    // 生效值按需展开：默认不渲染整段 JSON。
    expect(screen.queryByText(/"collect-period-ticks": 10/)).not.toBeInTheDocument()
    await user.click(within(rows[0]).getByRole('button', { name: '详情' }))
    expect(await screen.findByText(/"collect-period-ticks": 10/)).toBeInTheDocument()
  })

  it('回滚需填原因并经二次确认，确认后带 confirmed=true 提交', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const session = sessionOf({ status: 'completed', currentBatch: 3, totalBatches: 3 })
    usePushEndpoints(session)
    let body: Record<string, unknown> | null = null
    server.use(
      http.post(API('/managed-pushes/rollback'), async (info) => {
        body = (await info.request.json()) as Record<string, unknown>
        return HttpResponse.json(sessionOf({ id: 10, kind: 'rollback', status: 'running' }))
      }),
    )

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))

    const rollback = await screen.findByTestId('managed-rollback')
    // 未填原因时不可提交：后端同样会拒（ErrManagedPushReasonRequired），前端先拦一道。
    expect(rollback).toBeDisabled()

    await user.type(screen.getByTestId('managed-rollback-reason'), '指标量暴涨，撤回')
    expect(rollback).toBeEnabled()
    await user.click(rollback)

    // DangerConfirm 二次确认：必须说清「重推冻结快照」而不是「按现在的层重新合并」。
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('确认回滚')).toBeInTheDocument()
    expect(
      within(dialog).getByText(/冻结快照不等于当前层配置/),
    ).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '回滚' }))

    await waitFor(() => expect(body).not.toBeNull())
    expect(body).toMatchObject({ sourceSessionId: 9, reason: '指标量暴涨，撤回', confirmed: true })
  })

  it('未完成的会话不允许回滚，并说明原因', async () => {
    loginMockUser()
    const user = userEvent.setup()
    usePushEndpoints(sessionOf({ status: 'running' }))

    renderWithProviders(<ManagedConfigPage />)
    await user.click(await screen.findByRole('tab', { name: '下发会话' }))
    await user.click(await screen.findByTestId('managed-push-open-9'))

    expect(await screen.findByTestId('managed-rollback')).toBeDisabled()
    expect(screen.getByTestId('managed-rollback-blocked')).toHaveTextContent('只有已完成的会话可以回滚。')
  })

  it('会话列表端点缺失时降级为本地会话并明确提示', async () => {
    loginMockUser()
    const user = userEvent.setup()
    server.use(
      http.get(API('/managed-configs'), () => HttpResponse.json({ items: [] })),
      http.get(API('/managed-configs/layer'), () => HttpResponse.json({}, { status: 404 })),
      http.get(API('/managed-pushes'), () =>
        HttpResponse.json({ error: 'NOT_FOUND', message: 'route not registered' }, { status: 404 }),
      ),
    )

    renderWithProviders(<ManagedConfigPage />)
    await userEvent.setup().click(await screen.findByRole('tab', { name: '下发会话' }))

    // 「列表接口没就绪」与「没下发过」是两件事，必须分开说。
    expect(await screen.findByTestId('managed-push-list-missing')).toBeInTheDocument()
  })
})

describe('纯函数：后端状态机到界面的映射', () => {
  it('releaseGuard：只有等待放行且非最后一批才可放行', () => {
    expect(releaseGuard({ status: 'awaiting_release', currentBatch: 1, totalBatches: 3 })).toEqual({
      allowed: true,
      reasonKey: '',
    })
    expect(releaseGuard({ status: 'awaiting_release', currentBatch: 3, totalBatches: 3 })).toEqual({
      allowed: false,
      reasonKey: 'releaseLastBatch',
    })
    expect(releaseGuard({ status: 'running', currentBatch: 1, totalBatches: 3 })).toEqual({
      allowed: false,
      reasonKey: 'releaseNotAwaiting',
    })
    expect(releaseGuard({ status: 'paused', currentBatch: 1, totalBatches: 3 }).allowed).toBe(false)
  })

  it('isPushTerminal 只认「已完成 / 已熔断 / 已取消」', () => {
    expect(isPushTerminal('completed')).toBe(true)
    expect(isPushTerminal('failed')).toBe(true)
    expect(isPushTerminal('cancelled')).toBe(true)
    // awaiting_release 不是终态：它还在等人工放行。
    expect(isPushTerminal('awaiting_release')).toBe(false)
    expect(isPushTerminal('paused')).toBe(false)
    expect(isPushTerminal('running')).toBe(false)
  })

  it('会话状态配色：等待放行与运行中必须不同色', () => {
    expect(pushStatusLevel('running')).toBe('info')
    expect(pushStatusLevel('awaiting_release')).toBe('warning')
    expect(pushStatusLevel('failed')).toBe('danger')
    expect(pushStatusLevel('completed')).toBe('success')
  })

  it('目标状态配色与失败判定：skipped 不算失败', () => {
    expect(targetStateLevel('applied')).toBe('success')
    expect(targetStateLevel('timeout')).toBe('danger')
    // 离线跳过的机器不该被算成「配置推坏了」。
    expect(isTargetFailure('skipped')).toBe(false)
    expect(isTargetFailure('timeout')).toBe(true)
    expect(isTargetFailure('rejected')).toBe(true)
  })

  it('校验结果兼容 Errors / errors 两种字段名', () => {
    expect(validateErrors(undefined)).toEqual([])
    expect(validateErrors({ Errors: ['a'] })).toEqual(['a'])
    expect(validateErrors({ errors: ['b'] })).toEqual(['b'])
  })

  it('effectiveJson 解析失败时原样回显，不静默丢内容', () => {
    expect(parseEffectiveJson(undefined)).toBeNull()
    expect(parseEffectiveJson('{"a":1}')).toEqual({ a: 1 })
    expect(parseEffectiveJson('not json')).toBe('not json')
  })
})

describe('实例作用域：只有一台实例时自动选中', () => {
  /** 覆盖实例搜索接口返回的实例集合。 */
  function useSingleInstance(total: number, items: Array<{ id: number; name: string }>) {
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: items.map((i) => ({
            id: i.id,
            name: i.name,
            nodeId: 1,
            status: 'running',
            type: 'minecraft_java',
            role: 'backend',
          })),
          total,
          page: 1,
          pageSize: 2,
        }),
      ),
    )
  }

  it('恰好一台时自动选中，不需要用户再点一次', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints()
    useSingleInstance(1, [{ id: 7, name: 'solo-probe' }])

    renderWithProviders(<ManagedConfigPage />)

    // 切到实例作用域：此时 scopeRefId 为空，正是自动选中的触发条件。
    await user.selectOptions(screen.getByTestId('managed-scope'), 'instance')

    // 单机运维时，选择一个已经确定的目标纯属多余——自动选中后编辑器应直接可用。
    await waitFor(() => {
      expect(screen.queryByText('选定目标后才会载入该层的编辑内容。')).not.toBeInTheDocument()
    })
    // 选中后目标名会出现在选择器上。
    await waitFor(() => {
      expect(screen.getAllByText('solo-probe').length).toBeGreaterThan(0)
    })
  })

  it('多台时保持未选中，把选择权留给用户', async () => {
    loginMockUser()
    const user = userEvent.setup()
    useLayerEndpoints()
    useSingleInstance(2, [
      { id: 1, name: 'probe-a' },
      { id: 2, name: 'probe-b' },
    ])

    renderWithProviders(<ManagedConfigPage />)
    await user.selectOptions(screen.getByTestId('managed-scope'), 'instance')

    // 自动选谁都可能选错，而配置会真的下发到那台探针——多台时必须留空。
    await waitFor(() => {
      expect(screen.getByText('选定目标后才会载入该层的编辑内容。')).toBeInTheDocument()
    })
  })
})
