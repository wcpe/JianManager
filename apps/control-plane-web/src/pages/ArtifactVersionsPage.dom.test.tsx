import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
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
})
