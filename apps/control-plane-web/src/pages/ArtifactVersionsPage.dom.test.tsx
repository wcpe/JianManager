import { describe, expect, it } from 'vitest'
import {screen, waitFor, within} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ArtifactVersionsPage from './ArtifactVersionsPage'

const githubSource = {
  id: 1,
  packageId: 1,
  provider: 'github-release',
  name: '官方 GitHub Releases',
  enabled: true,
  lastSyncedAt: '2026-08-24T00:00:00Z',
  lastError: '',
}

const localSource = {
  id: 2,
  packageId: 1,
  provider: 'local-upload',
  name: '本地上传',
  enabled: true,
  lastSyncedAt: null,
  lastError: '',
}

const githubVersion = {
  id: 1,
  packageId: 1,
  sourceId: 1,
  version: '0.2.0',
  releaseRef: 'v0.2.0',
  assetName: 'ServerProbe-0.2.0.jar',
  expectedSha256: '9332ef4d9fdbc371ea60f250bb983a0f6973ae8116e1f437adcecaaaf8aa159a',
  assetId: 1,
  cachedAt: '2026-08-24T00:00:00Z',
  lastError: '',
}

describe('ArtifactVersionsPage 本地上传', () => {
  it('标记线上与本地来源，仅线上来源提供同步，并以 multipart 上传后刷新目录', async () => {
    loginMockUser()
    const user = userEvent.setup()
    let uploaded = false

    server.use(
      http.get(API('/artifact-packages/serverprobe'), () => HttpResponse.json({
        package: { id: 1, key: 'serverprobe', name: 'ServerProbe', assetType: 'server-probe', defaultVersionId: 1 },
        sources: [githubSource, localSource],
        versions: uploaded
          ? [githubVersion, { ...githubVersion, id: 2, sourceId: 2, version: '0.1.0', releaseRef: 'local-upload', assetName: 'ServerProbe-0.1.0.jar' }]
          : [githubVersion],
      })),
      http.post(API('/artifact-packages/serverprobe/versions/upload'), () => {
        uploaded = true
        return HttpResponse.json({ ...githubVersion, id: 2, sourceId: 2, version: '0.1.0', releaseRef: 'local-upload', assetName: 'ServerProbe-0.1.0.jar' }, { status: 201 })
      }),
    )

    renderWithProviders(<ArtifactVersionsPage />)

    expect(await screen.findAllByText('GitHub Releases（线上拉取）')).toHaveLength(2)
    expect(screen.getAllByText('本地上传')).toHaveLength(1)
    expect(screen.getAllByRole('button', { name: '同步新版本' })).toHaveLength(1)

    await user.type(screen.getByLabelText('版本号'), '0.1.0')
    const jar = new File(['server-probe'], 'ServerProbe-0.1.0.jar', { type: 'application/java-archive' })
    await user.upload(screen.getByLabelText('JAR 文件'), jar)
    await user.click(screen.getByRole('button', { name: '上传到 CP' }))

    expect(await screen.findByText('0.1.0')).toBeInTheDocument()
    expect(screen.getAllByText('本地上传')).toHaveLength(2)
    const localVersion = screen.getByText('0.1.0').closest('div[class*="p-3"]') as HTMLElement
    expect(within(localVersion).getByText('本地上传')).toBeInTheDocument()
  })

  it('取数失败仍保留页面外壳（PageShell/PageHeader 与 data-page 就绪信号）', async () => {
    loginMockUser()
    server.use(
      http.get(API('/artifact-packages/serverprobe'), () =>
        HttpResponse.json({ error: 'INTERNAL' }, { status: 500 }),
      ),
    )
    const { container } = renderWithProviders(<ArtifactVersionsPage />)

    expect(await screen.findByTestId('artifact-versions-error')).toBeInTheDocument()
    // 早退分支此前只剩一行红字：页名消失、e2e 依赖的 data-page 也不在。
    expect(container.firstElementChild).toHaveAttribute('data-page', 'artifact-versions')
    expect(screen.getByRole('heading', { name: 'ServerProbe 制品版本库' })).toBeInTheDocument()
  })

  it('已缓存但无 sha256 时只显示「已缓存到 CP」，不留尾随分隔符', async () => {
    loginMockUser()
    server.use(
      http.get(API('/artifact-packages/serverprobe'), () =>
        HttpResponse.json({
          package: { id: 1, key: 'serverprobe', name: 'ServerProbe', assetType: 'server-probe', defaultVersionId: 1 },
          sources: [githubSource],
          versions: [{ ...githubVersion, expectedSha256: '' }],
        }),
      ),
    )
    renderWithProviders(<ArtifactVersionsPage />)

    expect(await screen.findByText('已缓存到 CP')).toBeInTheDocument()
    expect(screen.queryByText('已缓存到 CP ·')).not.toBeInTheDocument()
  })

  it('设为全局默认在途时给出转圈与文案反馈', async () => {
    loginMockUser()
    const user = userEvent.setup()
    // 用手动闸门把请求钉在「在途」：断言完瞬时态再放行（fireEvent 观察不到，
    // 因为 act() 会把整个 mock 请求周期一起冲掉）。
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => { release = resolve })
    server.use(
      http.get(API('/artifact-packages/serverprobe'), () =>
        HttpResponse.json({
          package: { id: 1, key: 'serverprobe', name: 'ServerProbe', assetType: 'server-probe', defaultVersionId: 0 },
          sources: [githubSource],
          versions: [githubVersion],
        }),
      ),
      http.put(API('/artifact-packages/serverprobe/default-version'), async () => {
        await gate
        return HttpResponse.json({ defaultVersionId: 1 }, { status: 200 })
      }),
    )
    renderWithProviders(<ArtifactVersionsPage />)
    await screen.findByText('0.2.0')

    await user.click(screen.getByRole('button', { name: '设为全局默认' }))
    // 在途：此前只有禁用（无转圈无文案），点了没反应会被当成没点上。
    expect(await screen.findByRole('button', { name: '保存中...' })).toBeDisabled()

    release()
    await waitFor(() => expect(screen.queryByRole('button', { name: '保存中...' })).not.toBeInTheDocument())
  })
})
