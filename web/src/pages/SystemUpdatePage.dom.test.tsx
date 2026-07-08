import { afterEach, describe, it, expect, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { toast } from 'sonner'
import { renderWithProviders } from '@/test/render'
import { db } from '@/mocks/db'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { useAuthStore } from '@/stores/auth'
import { mockInject } from '@/mocks/inject'
import type { Session } from '@/mocks/handlers/domains/auth'
import i18n from '@/i18n'
import zh from '@/i18n/zh.json'
import en from '@/i18n/en.json'
import SystemUpdatePage from './SystemUpdatePage'

/**
 * SystemUpdatePage 强断言（FR-200）：①渲染 seed 版本对比（CP + 各节点）②升级节点→check 联动版本变化
 * ③注入 500→错误态。本页仅平台管理员可见，故需用解出 role=10 的 token 登录（store 从 JWT 解 role）。
 * 仅调本域 GET /self-update/check + POST /self-update/check/refresh + GET /self-update/rollout
 * + POST /self-update/nodes/:id/upgrade。
 */

/** 构造解码出 role=10 的假 JWT（payload 同 auth.ts fakeJwt 结构），并登记 session 供 requireAuth 放行。 */
function loginPlatformAdmin() {
  const payload = btoa(
    JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }),
  )
  const token = `mock.${payload}.sig`
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'r-admin', userId: 1 })
  useAuthStore.getState().login(token, 'r-admin')
}

const requiredSystemUpdateKeys = [
  'abortOnCanaryFailure',
  'batchAllPlaceholder',
  'batchSize',
  'cacheState',
  'cacheWorkerAsset',
  'cachedAt',
  'canaryConfigDesc',
  'canaryConfigTitle',
  'canaryNonePlaceholder',
  'canarySize',
  'checkFailed',
  'checkUpdate',
  'checking',
  'controlPlane',
  'cpRollbackConfirm',
  'cpRollbackConfirmDesc',
  'cpRollbackFailed',
  'cpRollbackStarted',
  'cpUpgradeConfirm',
  'cpUpgradeConfirmDesc',
  'cpUpgradeFailed',
  'cpUpgradeStarted',
  'current',
  'detail',
  'feedLatest',
  'forbidden',
  'lastChecked',
  'lastError',
  'latestVersion',
  'noArtifact',
  'noBackup',
  'noNodes',
  'nodeFailed',
  'nodePending',
  'nodeRollbackConfirm',
  'nodeRollbackConfirmDesc',
  'nodeRollbackFailed',
  'nodeRolledBack',
  'nodeSkipped',
  'nodeSucceeded',
  'nodeUpgradeConfirm',
  'nodeUpgradeConfirmDesc',
  'nodeUpgradeFailed',
  'nodeUpgraded',
  'nodeUpgrading',
  'nodes',
  'notCheckedYet',
  'notConfigured',
  'offline',
  'online',
  'phaseAborted',
  'phaseCanary',
  'phaseCompleted',
  'phaseRolling',
  'platform',
  'releaseNotes',
  'rollback',
  'rollbackTo',
  'rolloutCanary',
  'rolloutCurrentBatch',
  'rolloutFailedCount',
  'rolloutInProgress',
  'rolloutPending',
  'rolloutStartFailed',
  'rolloutStarted',
  'rolloutSucceeded',
  'rolloutTarget',
  'rolloutTitle',
  'rolloutTotal',
  'size',
  'stateCompleted',
  'stateIdle',
  'stateRunning',
  'subtitle',
  'title',
  'upToDate',
  'updatable',
  'updateState',
  'upgrade',
  'upgradeAll',
  'upgradeAllConfirm',
  'upgradeAllConfirmDesc',
  'version',
  'versionChange',
  'workerAssetCacheFailed',
  'workerAssetCached',
  'workerAssetCachedState',
  'workerAssetMissingState',
  'workerAssetsEmpty',
  'workerAssetsLoadFailed',
  'workerAssetsSubtitle',
  'workerAssetsTitle',
] as const

describe('SystemUpdatePage（mock 假后端）', () => {
  afterEach(async () => {
    vi.restoreAllMocks()
    await i18n.changeLanguage('zh')
  })

  it('systemUpdate i18n 键在中英文环境完整，避免英文回落到中文默认值', () => {
    for (const [lang, dict] of Object.entries({ zh, en })) {
      const missing = requiredSystemUpdateKeys.filter((key) => !(key in dict.systemUpdate))
      expect(missing, `${lang} missing systemUpdate keys`).toEqual([])
    }
  })

  it('渲染 seed 版本对比（CP 控制台 + 各节点）', async () => {
    loginPlatformAdmin()
    renderWithProviders(<SystemUpdatePage />)

    expect(await screen.findByText('系统更新')).toBeInTheDocument()
    // CP 卡片来自 GET /check（异步缓存读取）→ await（不再靠进页自动刷新的时序垫，FIX-6）。
    expect(await screen.findByText('Control Plane（控制台）')).toBeInTheDocument()
    // 节点行（seed nodes alpha/beta）。
    expect(await screen.findByText('alpha')).toBeInTheDocument()
    expect(screen.getByText('beta')).toBeInTheDocument()
  })

  it('进页只读缓存，不自动触发 live 刷新 / 写缓存（FIX-6）', async () => {
    loginPlatformAdmin()
    let refreshCalls = 0
    server.use(
      http.post(API('/self-update/check/refresh'), () => {
        refreshCalls++
        return HttpResponse.json({
          configured: true,
          latestVersion: '0.12.0',
          notes: '',
          source: 'github:wcpe/JianManager@stable',
          controlPlane: { online: true, currentVersion: '0.12.0', os: 'linux', arch: 'amd64', updateAvailable: false, artifactAvailable: true },
          nodes: [],
          cached: true,
        })
      }),
    )
    renderWithProviders(<SystemUpdatePage />)

    // 缓存（GET /check）渲染出 CP 卡片即说明进页已读到缓存。
    await screen.findByText('Control Plane（控制台）')
    // 进页只读缓存，绝不自动 POST refresh（否则每次点开都 live 检查 + UPDATE self_update_check_caches）。
    expect(refreshCalls).toBe(0)
  })

  it('显示 Worker 二进制缓存状态', async () => {
    loginPlatformAdmin()
    renderWithProviders(<SystemUpdatePage />)

    const panel = await screen.findByRole('region', { name: 'Worker 二进制缓存' })
    expect(within(panel).getByText('linux/amd64')).toBeInTheDocument()
    expect(within(panel).getByText('windows/amd64')).toBeInTheDocument()
    expect(await within(panel).findByText('0123456789abcdef'.repeat(4))).toBeInTheDocument()
    expect(within(panel).getByText('1.0 MB')).toBeInTheDocument()
    expect(within(panel).getByText(/2026-07-06/)).toBeInTheDocument()
    expect(within(panel).getByText('sha256 校验失败')).toBeInTheDocument()
  })

  it('Worker 二进制缓存按版本匹配，旧版本缓存不遮住当前版本缺失', async () => {
    loginPlatformAdmin()
    server.use(
      http.get(API('/self-update/check'), () =>
        HttpResponse.json({
          configured: true,
          latestVersion: '0.13.0',
          notes: '',
          source: 'feed',
          controlPlane: { online: true, currentVersion: '0.12.0', os: 'linux', arch: 'amd64', updateAvailable: true, artifactAvailable: true },
          nodes: [{ nodeId: 1, name: 'alpha', online: true, currentVersion: '0.12.0', os: 'linux', arch: 'amd64', updateAvailable: false, artifactAvailable: true }],
          cached: true,
        }),
      ),
      http.get(API('/self-update/worker-assets'), () =>
        HttpResponse.json([
          { version: '0.11.0', os: 'linux', arch: 'amd64', cached: true, sha256: 'a'.repeat(64), size: 100, cachedAt: '2026-07-01T00:00:00Z' },
        ]),
      ),
    )
    renderWithProviders(<SystemUpdatePage />)

    const panel = await screen.findByRole('region', { name: 'Worker 二进制缓存' })
    const linuxRow = within(panel).getByText('linux/amd64').closest('tr') as HTMLElement
    expect(within(linuxRow).getByText('0.12.0')).toBeInTheDocument()
    expect(within(linuxRow).getByText('未缓存')).toBeInTheDocument()
    expect(within(linuxRow).queryByText('a'.repeat(64))).not.toBeInTheDocument()
  })

  it('点击预缓存后刷新 Worker 二进制缓存状态', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SystemUpdatePage />)

    const panel = await screen.findByRole('region', { name: 'Worker 二进制缓存' })
    const windowsRow = within(panel).getByText('windows/amd64').closest('tr') as HTMLElement
    expect(within(windowsRow).getByText('未缓存')).toBeInTheDocument()

    await user.click(within(windowsRow).getByRole('button', { name: '预缓存' }))

    await waitFor(() => {
      const refreshedRow = within(panel).getByText('windows/amd64').closest('tr') as HTMLElement
      expect(within(refreshedRow).getByText('已缓存')).toBeInTheDocument()
      expect(within(refreshedRow).getByText('fedcba9876543210'.repeat(4))).toBeInTheDocument()
      expect(within(refreshedRow).queryByText('sha256 校验失败')).not.toBeInTheDocument()
    })
  })

  it('CP 升级要求输入目标版本后才允许确认', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    let upgradeCalls = 0
    server.use(
      http.post(API('/self-update/control-plane/upgrade'), () => {
        upgradeCalls++
        return HttpResponse.json({ status: 'accepted', fromVersion: '0.9.0', toVersion: '0.10.0' }, { status: 202 })
      }),
    )
    renderWithProviders(<SystemUpdatePage />)

    await screen.findByText('Control Plane（控制台）')
    await user.click(screen.getAllByRole('button', { name: '升级' })[0]!)

    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: '升级' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText('0.10.0'), 'wrong')
    expect(confirm).toBeDisabled()

    await user.clear(within(dialog).getByPlaceholderText('0.10.0'))
    await user.type(within(dialog).getByPlaceholderText('0.10.0'), '0.10.0')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(upgradeCalls).toBe(1))
  })

  it('CP 回滚要求输入备份版本后才允许确认', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    let rollbackCalls = 0
    server.use(
      http.post(API('/self-update/control-plane/rollback'), () => {
        rollbackCalls++
        return HttpResponse.json({ status: 'accepted', fromVersion: '0.9.0', toVersion: '0.8.0' }, { status: 202 })
      }),
    )
    renderWithProviders(<SystemUpdatePage />)

    await screen.findByText('Control Plane（控制台）')
    await user.click(screen.getAllByRole('button', { name: '回滚 v0.8.0' })[0]!)

    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: '回滚' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText('0.8.0'), '0.8.0')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(rollbackCalls).toBe(1))
  })

  it('升级节点后，check 联动反映新版本', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SystemUpdatePage />)

    // alpha 当前 0.9.0（可升级）。定位 alpha 行。
    const alphaRow = (await screen.findByText('alpha')).closest('tr') as HTMLElement
    await waitFor(() => expect(within(alphaRow).getByText('0.9.0')).toBeInTheDocument())

    // 点该行「升级」→ 危险确认（平台管理员放行）→ 确认。
    await user.click(within(alphaRow).getByRole('button', { name: '升级' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '升级' }))

    // upgradeNode 写 workerVersion=0.10.0 + onUpgraded 触发 refresh → check 重取，alpha 行显示 0.10.0。
    await waitFor(() => {
      const row = (screen.getByText('alpha')).closest('tr') as HTMLElement
      expect(within(row).getByText('0.10.0')).toBeInTheDocument()
    })
  })

  it('回滚节点后，check 联动反映备份版本', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    renderWithProviders(<SystemUpdatePage />)

    const alphaRow = (await screen.findByText('alpha')).closest('tr') as HTMLElement
    await waitFor(() => expect(within(alphaRow).getByText('0.9.0')).toBeInTheDocument())

    await user.click(within(alphaRow).getByRole('button', { name: '回滚 v0.8.0' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '回滚' }))

    await waitFor(() => {
      const row = (screen.getByText('alpha')).closest('tr') as HTMLElement
      expect(within(row).getByText('0.8.0')).toBeInTheDocument()
    })
  })

  it('英文环境发起全网升级失败 toast 使用动作失败文案，而非失败数量文案', async () => {
    await i18n.changeLanguage('en')
    loginPlatformAdmin()
    const user = userEvent.setup()
    const toastError = vi.spyOn(toast, 'error')
    server.use(
      http.post(API('/self-update/nodes/upgrade-all'), () =>
        HttpResponse.json({}, { status: 500 }),
      ),
    )
    renderWithProviders(<SystemUpdatePage />)

    await screen.findByText('Control Plane')
    // FR-155：全网升级先开配置弹窗（金丝雀分批），点「Continue」再进 DangerConfirm 二次确认。
    await user.click(screen.getByRole('button', { name: 'Upgrade all' }))
    const configDialog = await screen.findByRole('dialog')
    await user.click(within(configDialog).getByRole('button', { name: 'Continue' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Upgrade all' }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith('Failed to start fleet-wide upgrade'))
    expect(toastError).not.toHaveBeenCalledWith(expect.stringContaining('{{n}}'))
  })

  it('全网升级配置金丝雀分批：弹窗有金丝雀/每批输入，发起后请求带参数且进度显示阶段（FR-155）', async () => {
    loginPlatformAdmin()
    const user = userEvent.setup()
    let sentBody: { canarySize?: number; batchSize?: number; abortOnCanaryFailure?: boolean } | null = null
    // 金丝雀阶段快照（upgrade-all 触发后 rollout GET 才返回它，避免进页即渲染进度面板致 alpha 重复）。
    const canarySnapshot = {
      rolloutId: 'r-canary',
      targetVersion: '0.10.0',
      state: 'running',
      startedAt: '2026-07-06T00:00:00Z',
      finishedAt: null,
      total: 2,
      succeeded: 1,
      failed: 0,
      pending: 1,
      nodes: [
        { nodeId: 1, name: 'alpha', state: 'succeeded', fromVersion: '0.9.0', toVersion: '0.10.0', error: '', attempts: 1 },
        { nodeId: 2, name: 'beta', state: 'pending', fromVersion: '', toVersion: '', error: '', attempts: 0 },
      ],
      phase: 'canary',
      canarySize: 1,
      batchSize: 0,
      currentBatch: 1,
    }
    let started = false
    server.use(
      http.post(API('/self-update/nodes/upgrade-all'), async (info) => {
        sentBody = (await info.request.json()) as typeof sentBody
        started = true
        return HttpResponse.json(canarySnapshot, { status: 202 })
      }),
      // 触发前 idle（不渲染进度面板），触发后回金丝雀阶段 → 进度面板渲染阶段徽章 + 金丝雀计数。
      http.get(API('/self-update/rollout'), () =>
        HttpResponse.json(started ? canarySnapshot : { rolloutId: '', targetVersion: '', state: 'idle', startedAt: '', finishedAt: null, total: 0, succeeded: 0, failed: 0, pending: 0, nodes: [], phase: '', canarySize: 0, batchSize: 0, currentBatch: 0 }),
      ),
    )
    renderWithProviders(<SystemUpdatePage />)

    // 打开配置弹窗（节点区「全网升级」按钮；alpha 可升级 → 未被禁用）。
    await screen.findByText('alpha')
    await user.click(screen.getByRole('button', { name: '全网升级' }))

    // 配置弹窗内应有金丝雀数与每批数输入、以及金丝雀失败即中止勾选。
    const configDialog = await screen.findByRole('dialog')
    const canaryInput = within(configDialog).getByLabelText('金丝雀节点数')
    const batchInput = within(configDialog).getByLabelText('每批节点数')
    expect(canaryInput).toBeInTheDocument()
    expect(batchInput).toBeInTheDocument()
    expect(within(configDialog).getByText('金丝雀失败即中止（不再升级剩余节点）')).toBeInTheDocument()

    await user.type(canaryInput, '1')
    await user.type(batchInput, '2')
    // 「继续」→ 关配置弹窗、开 DangerConfirm。
    await user.click(within(configDialog).getByRole('button', { name: '继续' }))

    const confirmDialog = await screen.findByRole('dialog')
    await user.click(within(confirmDialog).getByRole('button', { name: '全网升级' }))

    // 请求体应带金丝雀/分批参数。
    await waitFor(() => expect(sentBody).not.toBeNull())
    expect(sentBody!.canarySize).toBe(1)
    expect(sentBody!.batchSize).toBe(2)
    expect(sentBody!.abortOnCanaryFailure).toBe(true)

    // 进度面板显示金丝雀阶段徽章 + 金丝雀计数。
    expect(await screen.findByText('全网升级进度')).toBeInTheDocument()
    expect(await screen.findByText('金丝雀')).toBeInTheDocument()
    expect(screen.getByText('金丝雀 1')).toBeInTheDocument()
  })

  it('注入 500：检查失败显示错误态', async () => {
    loginPlatformAdmin()
    // check 与 refresh 同时注入 500，使页面无任何结果可用 → 渲染错误横幅。
    mockInject('get', '/self-update/check', { kind: 'status', status: 500, body: { message: '检查更新失败' } })
    mockInject('post', '/self-update/check/refresh', { kind: 'status', status: 500, body: { message: '检查更新失败' } })
    renderWithProviders(<SystemUpdatePage />)

    expect(await screen.findByText('检查更新失败')).toBeInTheDocument()
  })
})
