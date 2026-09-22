import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { mockInject } from '@jianmanager/devmock/inject'
import { db } from '@jianmanager/devmock/db'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import type { Session, User } from '@jianmanager/devmock/handlers/domains/auth'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { DEFAULT_ROLE_NODES } from '@/lib/roles'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * FR-466 / FR-467 / FR-468：实例控制台内的三个新面板（按能力画像落位）。
 * 数据来自 devmock：
 * - GET /instances/:id/snapshots（实例 3 两条：一条 manual + 一条 pre_rollback；实例 2 空）
 * - GET /instances/:id/quota（实例 3 有内存限额且为 docker；实例 1 为 daemon）
 * - GET /instances/:id/binary-version（实例 30 为 generic/beacon 且**预置可回滚点** → 已绑定；
 *   MC 实例未绑定）
 *
 * 面板落位（按能力画像；三种画像都命中，不产生幽灵页签）：
 * - 快照 → 「备份定时」页签（与备份同页签：归档 vs 时间点，单一归属）
 * - 配额 / 二进制版本 → 「概览」页签（overview 是所有画像都存在的唯一 Tab）
 */

/**
 * 授予组管理员角色：快照回滚/删除走 DangerConfirm `scope="group"`，需 role≥1 才放行确认。
 * loginMockUser 的默认 token 解不出 role（未登录态 → 门禁判拒绝）。
 */
function grantGroupAdmin(): void {
  useAuthStore.setState({ role: 1, isAuthenticated: true })
}

/** 等实例加载完成后切到指定页签（未加载时页面只渲染「服务器不存在或正在加载」）。 */
async function gotoTab(name: string) {
  const user = userEvent.setup()
  // 放宽超时：控制台首屏要拉实例 + 能力画像，默认 1s 在冷启动下偶发不足。
  const tab = await screen.findByRole('tab', { name }, { timeout: 8000 })
  await user.click(tab)
  return user
}

/** 取快照列表中某一行（按快照名定位），返回该行与其内的按钮。 */
function snapshotRow(panel: HTMLElement, name: string): HTMLElement {
  const nameEl = within(panel).getByText(name)
  const row = nameEl.closest('li')
  expect(row, `快照「${name}」应渲染在 li 行内`).not.toBeNull()
  return row as HTMLElement
}

/**
 * 模拟只读角色（group_viewer：持 `instance.read`，但**无** `instance.write` / `instance.delete`）。
 *
 * 必须**同时**改假后端用户表：页面挂载后会拉 `/auth/me` 并覆盖权限 store，
 * 只 setState 的话会被那次响应立刻冲回平台管理员（全开），门禁用例就成了假绿。
 * 构造一个 role=3 的用户并把当前会话指过去，使前后端一致解析为只读。
 */
function loginAsReadOnly(): void {
  const userId = 9003
  db<User>('users').insert({ id: userId, uuid: 'u-viewer', username: 'viewer', password: '', role: 3 })
  const sessions = db<Session>('sessions')
  const current = sessions.find((s) => s.accessToken === 'test-access-token')
  expect(current, '前置：loginMockUser 已写入会话').toBeDefined()
  sessions.update(current!.id, { userId })

  useAuthStore.setState({ role: 3, isAuthenticated: true })
  usePermissionsStore.setState({
    nodes: new Set(DEFAULT_ROLE_NODES[3]),
    roleKey: 'group_viewer',
    isPlatformAdmin: false,
    loaded: true,
  })
}

describe('实例控制台 快照 / 配额 / 二进制版本（FR-466/467/468）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('快照面板：列出快照并标注「回滚前」类型，一键回滚需二次确认', async () => {
    grantGroupAdmin()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    const user = await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    const list = await within(panel).findByTestId('snapshot-list')
    expect(within(list).getByText('升级前整机快照')).toBeInTheDocument()
    // pre_rollback 单独标注为「回滚前」——这是「回滚仍可退回」的可视依据。
    expect(within(list).getByText('回滚前')).toBeInTheDocument()

    // 点击「一键回滚」先弹二次确认，不直接提交。
    const manualRow = snapshotRow(panel, '升级前整机快照')
    await user.click(within(manualRow).getByRole('button', { name: /一键回滚/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/确认回滚到该快照/)).toBeInTheDocument()
    // 确认文案必须讲清「会先建回滚前快照 + 运行中会先停服 + 停在已停止」，否则用户无法判断风险。
    expect(within(dialog).getByText(/回滚前/)).toBeInTheDocument()
    expect(within(dialog).getByText(/停在已停止/)).toBeInTheDocument()
    // W-18：断言**插值后的快照名**，而不是固定文案里本来就有的「回滚前」。
    // 必须指向被点击的那条（升级前整机快照）；插值成别的名字则此处变红。
    expect(within(dialog).getByText(/将把工作目录回放到「升级前整机快照」的状态/)).toBeInTheDocument()
    // W-04：与 CP 自身升级/回滚同口径——逐字输入快照名才能确认。
    expect(within(dialog).getByLabelText('请输入「升级前整机快照」以确认此操作')).toBeInTheDocument()
    const confirmBtn = within(dialog).getByRole('button', { name: '一键回滚' })
    expect(confirmBtn).toBeDisabled()
  })

  it('快照回滚：回滚在途时禁用其他快照的回滚按钮并显示「回滚中…」', async () => {
    // W-02：原先 onConfirm 里同步清空 pendingRollback，使在途态恒 false ——
    // 「回滚中…」是死键、其他行的回滚按钮在途期间仍可点。此用例锁死这条互斥与反馈。
    grantGroupAdmin()
    mockInject('post', '/snapshots/:sid/rollback', { kind: 'delay', ms: 1500 })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    const user = await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    const list = await within(panel).findByTestId('snapshot-list')
    const rollbackButtons = within(list).getAllByRole('button', { name: /一键回滚/ })
    expect(rollbackButtons.length).toBeGreaterThanOrEqual(2)

    const manualRow = snapshotRow(panel, '升级前整机快照')
    await user.click(within(manualRow).getByRole('button', { name: /一键回滚/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('请输入「升级前整机快照」以确认此操作'), '升级前整机快照')
    await user.click(within(dialog).getByRole('button', { name: '一键回滚' }))

    // 在途：文案**只出现在被回滚的那一行**（不是多行都显示）。
    await waitFor(() => expect(within(manualRow).getByText('回滚中…')).toBeInTheDocument())
    expect(within(panel).getAllByText('回滚中…')).toHaveLength(1)
    // 全局互斥：所有回滚按钮（含未被点击的那一行）都禁用。
    // hidden:true —— 弹窗仍开着时 Radix 会把背景整体标 aria-hidden，默认 role 查询会跳过它。
    const allRollback = within(list).getAllByRole('button', { name: /一键回滚/, hidden: true })
    expect(allRollback.length).toBeGreaterThanOrEqual(2)
    for (const btn of allRollback) {
      expect(btn).toBeDisabled()
    }
    // 删除按钮同样被互斥（回滚在途不应能删掉正在回滚的目标）。
    for (const btn of within(list).getAllByRole('button', { name: '删除', hidden: true })) {
      expect(btn).toBeDisabled()
    }
  })

  it('快照删除：需二次确认且逐字输入快照名，并说明会连带删除底层备份', async () => {
    // W-03：删除此前无确认、直连 mutation，而它连带删除底层全量归档（后端要求 instance.delete）。
    grantGroupAdmin()
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    const user = await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    const list = await within(panel).findByTestId('snapshot-list')
    const manualRow = snapshotRow(panel, '升级前整机快照')
    await user.click(within(manualRow).getByRole('button', { name: '删除' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/确认删除快照「升级前整机快照」/)).toBeInTheDocument()
    expect(within(dialog).getByText(/底层全量备份/)).toBeInTheDocument()
    // 逐字确认：未输入前确认按钮禁用；输入正确值才放行。
    const confirmBtn = within(dialog).getByRole('button', { name: '删除' })
    expect(confirmBtn).toBeDisabled()
    await user.type(within(dialog).getByLabelText('请输入「升级前整机快照」以确认此操作'), '升级前整机快照')
    expect(confirmBtn).toBeEnabled()
    // 未提交（仅输入并取消）：快照仍在列表里 —— 证明「无确认不删除」。
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(within(list).getByText('升级前整机快照')).toBeInTheDocument()
  })

  it('快照空态：无快照实例给出创建引导', async () => {
    // 实例 2（lobby-proxy）种子无整机快照。
    renderWithProviders(<InstanceConsolePage instanceId={2} />, { route: '/instances/2' })
    await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    expect(await within(panel).findByTestId('snapshot-empty')).toHaveTextContent('暂无快照')
  })

  it('快照查询失败：显示错误与重试，绝不谎报为「暂无快照」', async () => {
    // W-01：原先 `data: snapshots = []` 把读失败吞成空数组 → 渲染「暂无快照」，
    // 让运维以为数据没丢（与事实相反）。错误态必须独立成态。
    mockInject('get', '/instances/:id/snapshots', { kind: 'status', status: 500 })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    const err = await within(panel).findByTestId('snapshot-error')
    expect(err).toHaveTextContent('加载快照列表失败')
    expect(within(panel).queryByTestId('snapshot-empty')).not.toBeInTheDocument()
    expect(within(panel).queryByTestId('snapshot-list')).not.toBeInTheDocument()
    expect(within(err).getByRole('button', { name: '刷新' })).toBeEnabled()
  })

  it('快照运行态提示：实例运行中创建快照会明确警示世界文件可能不一致', async () => {
    // W-11：同屏的「备份恢复」要求停服，而快照创建允许运行态——必须解释差异并提示风险。
    // 实例 1（survival-1）种子 RUNNING。
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })
    await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    expect(await within(panel).findByTestId('snapshot-running-warning')).toHaveTextContent('非一致')
  })

  it('快照类型兜底：未知 kind 显示原值而非误标「手动」', async () => {
    // W-13：KIND_KEY 兜底原先回退到 snapshotKindManual，后端新增 kind 时会被静默标成「手动」，
    // 与同文件 stateLabel 的「回退原值」策略相反。此处注入一个未知 kind 锁死新策略。
    const raw = db<{ id: number; instanceId: number; kind: string }>('instanceSnapshots')
    raw.insert({
      id: 900,
      uuid: 'snap-unknown-kind',
      instanceId: 9,
      name: '未来类型快照',
      kind: 'cold_archive',
      state: 'completed',
      rootBackupId: 990,
      binaryName: '',
      binarySha256: '',
      configHash: '',
      configSummary: '',
      triggeredBy: 1,
      triggeredByRollbackId: 0,
      sizeMb: 0,
      failureReason: '',
      note: '',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    })
    renderWithProviders(<InstanceConsolePage instanceId={9} />, { route: '/instances/9' })
    await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    const kind = await within(panel).findByText('cold_archive')
    expect(kind).toHaveAttribute('data-kind', 'cold_archive')
    expect(within(panel).queryByText('手动')).not.toBeInTheDocument()
  })

  it('配额面板：显示限额、来源与强制档位；docker 模式不标注降级', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('quota-panel')
    // 实例 3 种子 memLimitMb=2048 → 内存行显示实例级来源（限额与「/」同处一个元素）。
    expect(await within(panel).findByText(/\/ 2048 MiB/)).toBeInTheDocument()
    expect(within(panel).getAllByText('实例级').length).toBeGreaterThan(0)
    expect(within(panel).getByTestId('quota-mode')).toHaveTextContent('超限只告警')
    // 实例 3 种子 processType=docker → 支持内核级限流，不标注降级提示。
    expect(within(panel).queryByTestId('quota-throttle-unsupported')).not.toBeInTheDocument()
  })

  it('配额面板：非 docker 模式如实标注无法内核级限流', async () => {
    // 实例 1（survival-1）种子 processType=daemon。
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('quota-panel')
    expect(await within(panel).findByTestId('quota-throttle-unsupported')).toHaveTextContent('非 docker')
  })

  it('配额面板：无组实例的磁盘来源为「不限」，不得谎报「组派生」', async () => {
    // W-17：mock 原先无条件回 diskSource='group'。实例 9（server-0009）不属于任何实例分组。
    renderWithProviders(<InstanceConsolePage instanceId={9} />, { route: '/instances/9' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('quota-panel')
    const diskRow = (await within(panel).findByText('磁盘')).closest('li')
    expect(diskRow, '磁盘行应存在').not.toBeNull()
    expect(diskRow as HTMLElement).toHaveTextContent('不限')
    expect(diskRow as HTMLElement).not.toHaveTextContent('组派生')
  })

  it('配额面板：展示待收紧限额与强制状态作用域', async () => {
    // W-09：DTO 原先漏 enforceStateScope / throttleCpuLimit / throttleMemLimitMb，
    // 使 spec §2.4 要求的「待收紧限额」（R7 恢复即清零）在 UI 上不可见。
    db<MockInstance>('instances').update(3, { throttleCpuLimit: 0.75, throttleMemLimitMb: 1024 })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('quota-panel')
    const pending = await within(panel).findByTestId('quota-throttle-pending')
    expect(pending).toHaveTextContent('已登记待收紧限额（下次启动生效）')
    expect(pending).toHaveTextContent('0.75 核')
    expect(pending).toHaveTextContent('1024 MiB')
    expect(await within(panel).findByTestId('quota-enforce-scope')).toHaveTextContent('in_process')
  })

  it('配额查询失败：显示错误与重试，不永久停在「加载中」', async () => {
    // W-01：原先 `isLoading || !quota` 在请求失败后恒真 → 永久「加载中…」。
    mockInject('get', '/instances/:id/quota', { kind: 'status', status: 500 })
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('quota-panel')
    const err = await within(panel).findByTestId('quota-error')
    expect(err).toHaveTextContent('加载配额失败')
    expect(within(panel).queryByText('加载中...')).not.toBeInTheDocument()
    expect(within(err).getByRole('button', { name: '刷新' })).toBeEnabled()
  })

  it('二进制版本面板：已绑定实例显示当前版本与可升级候选', async () => {
    // 实例 30（beacon-1）：type=generic → 已登记版本绑定，当前 1.1.0、可回滚到 1.0.0。
    // 实例 30 种子 status=RUNNING → 先停服，否则按钮被运行态守卫禁用（W-06 的另一条用例覆盖）。
    db<MockInstance>('instances').update(30, { status: 'STOPPED' })
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    const current = await within(panel).findByTestId('binary-version-current')
    expect(current).toHaveTextContent('1.1.0')
    // 候选下拉含可升级版本。
    expect(within(panel).getByRole('option', { name: /1\.2\.0/ })).toBeInTheDocument()
    // W-07：mock 现预置回滚点 → 回滚按钮**可用**（原先 hasRollback 恒 false、按钮永远禁用，
    // 回滚的二次确认路径在 mock 下完全不可达）。
    expect(within(panel).getByRole('button', { name: /回滚上一版本/ })).toBeEnabled()
  })

  it('二进制版本面板：实例运行中禁用升级与回滚并给出原因', async () => {
    // W-06：原先只看绑定与 pending，运行态仍可点、靠后端 409 兜；同页签备份恢复已按状态禁用。
    // 实例 30 种子 RUNNING。
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    await within(panel).findByTestId('binary-version-current')
    expect(within(panel).getByRole('button', { name: /回滚上一版本/ })).toBeDisabled()
    expect(within(panel).getByRole('button', { name: '升级' })).toBeDisabled()
    expect(within(panel).getByRole('button', { name: /回滚上一版本/ })).toHaveAttribute(
      'title',
      '升级与回滚要求实例已停止；变更完成后需手动启动',
    )
  })

  it('二进制版本面板：升级与回滚需逐字确认目标版本', async () => {
    // W-04：替换可执行文件与 CP 自身升级同量级，SystemUpdatePage 用 confirmText={latest}。
    grantGroupAdmin()
    db<MockInstance>('instances').update(30, { status: 'STOPPED' })
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    const user = await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    await within(panel).findByTestId('binary-version-current')
    // 升级按钮在未选目标版本时即禁用，故先等下拉可用。
    await waitFor(() => expect(within(panel).getByRole('combobox')).toBeEnabled())
    await user.selectOptions(within(panel).getByRole('combobox'), within(panel).getByRole('option', { name: /1\.2\.0/ }))
    await user.click(within(panel).getByRole('button', { name: '升级' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/替换为「1\.2\.0」/)).toBeInTheDocument()
    // 逐字确认：未输入前禁用；输入错误值仍禁用；输入正确值才放行。
    const confirmBtn = within(dialog).getByRole('button', { name: '升级' })
    expect(confirmBtn).toBeDisabled()
    await user.type(within(dialog).getByLabelText('请输入「1.2.0」以确认此操作'), '1.1.0')
    expect(confirmBtn).toBeDisabled()
    await user.clear(within(dialog).getByLabelText('请输入「1.2.0」以确认此操作'))
    await user.type(within(dialog).getByLabelText('请输入「1.2.0」以确认此操作'), '1.2.0')
    expect(confirmBtn).toBeEnabled()
  })

  it('二进制版本面板：回滚成功后面板刷新为上一版本，且可再次回滚', async () => {
    // W-07：mock 现真正交换 Current/Previous，使「回滚」这条路径在 mock 下可演练且可断言结果。
    grantGroupAdmin()
    db<MockInstance>('instances').update(30, { status: 'STOPPED' })
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    const user = await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    expect(await within(panel).findByTestId('binary-version-current')).toHaveTextContent('1.1.0')
    await user.click(within(panel).getByRole('button', { name: /回滚上一版本/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('请输入「1.0.0」以确认此操作'), '1.0.0')
    await user.click(within(dialog).getByRole('button', { name: '回滚上一版本' }))

    // 回滚后当前版本变为 1.0.0，上一版本变为 1.1.0（语义对称：回滚本身也可再回滚）。
    await waitFor(() =>
      expect(within(panel).getByTestId('binary-version-current')).toHaveTextContent('1.0.0'),
    )
    expect(within(panel).getByTestId('binary-version-previous')).toHaveTextContent('1.1.0')
  })

  it('二进制版本查询失败：显示错误与重试，不永久停在「加载中」', async () => {
    // W-01：原先 `isLoading || !view` 在请求失败后恒真 → 永久「加载中…」。
    mockInject('get', '/instances/:id/binary-version', { kind: 'status', status: 500 })
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    const err = await within(panel).findByTestId('binary-version-error')
    expect(err).toHaveTextContent('加载二进制版本信息失败')
    expect(within(panel).queryByText('加载中...')).not.toBeInTheDocument()
    expect(within(err).getByRole('button', { name: '刷新' })).toBeEnabled()
  })

  it('二进制版本面板：非二进制实例给出明确的「未登记绑定」提示', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    expect(await within(panel).findByTestId('binary-version-unbound')).toHaveTextContent('未登记二进制版本绑定')
  })
})

/**
 * W-10：前端权限门禁（与后端 RBAC 同口径的**提前提示**，最终拒绝仍由 CP 强制）。
 * 只读角色持 `instance.read`（能看到面板），但不持 `instance.write` / `instance.delete`。
 * 原先三个面板的按钮不看权限，只读用户看到的是「可点击的破坏性按钮」，点下去只得一个 403。
 */
describe('实例控制台新面板的前端权限门禁（W-10，只读角色）', () => {
  beforeEach(() => {
    loginMockUser()
    loginAsReadOnly()
  })

  it('快照面板：无 instance.write / instance.delete 时创建、回滚、删除按钮均禁用并给出原因', async () => {
    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })
    await gotoTab('备份定时')

    const panel = await screen.findByTestId('snapshot-panel')
    // 只读角色仍**看得到**快照列表（持 instance.read）。
    const list = await within(panel).findByTestId('snapshot-list')

    const createBtn = within(panel).getByRole('button', { name: /创建快照/ })
    expect(createBtn).toBeDisabled()
    expect(createBtn).toHaveAttribute('title', '当前角色无实例写入权限，无法创建或回滚快照')

    for (const btn of within(list).getAllByRole('button', { name: /一键回滚/ })) {
      expect(btn).toBeDisabled()
      expect(btn).toHaveAttribute('title', '当前角色无实例写入权限，无法创建或回滚快照')
    }
    for (const btn of within(list).getAllByRole('button', { name: '删除' })) {
      expect(btn).toBeDisabled()
      // 删除的权限门槛更高（instance.delete）：提示文案独立，不被写成 write 的。
      expect(btn).toHaveAttribute('title', '当前角色无实例删除权限，无法删除快照（删除会连带清掉底层归档）')
    }
  })

  it('二进制版本面板：无 instance.write 时升级与回滚按钮禁用并给出原因', async () => {
    db<MockInstance>('instances').update(30, { status: 'STOPPED' })
    renderWithProviders(<InstanceConsolePage instanceId={30} />, { route: '/instances/30' })
    await gotoTab('概览')

    const panel = await screen.findByTestId('binary-version-panel')
    // 只读角色仍看得到版本信息（持 instance.read）。
    await within(panel).findByTestId('binary-version-current')
    const upgradeBtn = within(panel).getByRole('button', { name: '升级' })
    const rollbackBtn = within(panel).getByRole('button', { name: /回滚上一版本/ })
    expect(upgradeBtn).toBeDisabled()
    expect(rollbackBtn).toBeDisabled()
    expect(rollbackBtn).toHaveAttribute('title', '当前角色无实例写入权限，无法升级或回滚版本')
  })
})
