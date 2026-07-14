import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { InstanceGroupManager } from './InstanceGroupManager'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'

describe('InstanceGroupManager', () => {
  it('允许把选中实例从当前分组移出', async () => {
    const user = userEvent.setup()
    let removed: { groupId: number; instanceIds: number[] } | null = null
    server.use(
      http.get(API('/instances'), () =>
        HttpResponse.json([
          {
            id: 1,
            uuid: 'i-survival',
            nodeId: 1,
            name: 'survival-1',
            type: 'minecraft_java',
            role: 'backend',
            processType: 'daemon',
            status: 'STOPPED',
            startCommand: 'java -jar server.jar',
            workDir: '/srv/survival-1',
            serverPort: 25565,
            autoStart: false,
            autoRestart: false,
            tags: null,
            createdAt: '2026-01-01T00:00:00Z',
          },
        ]),
      ),
      http.get(API('/nodes'), () =>
        HttpResponse.json([{ id: 1, name: 'node-main' }]),
      ),
      http.get(API('/instance-groups'), () =>
        HttpResponse.json([
          { id: 1, uuid: 'g-asia', name: '亚洲区', parentId: null, sort: 0, instanceCount: 1 },
          { id: 2, uuid: 'g-survival', name: '生存', parentId: 1, sort: 0, instanceCount: 1 },
        ]),
      ),
      http.get(API('/instance-groups/:id/instances'), () => HttpResponse.json({ instanceIds: [1] })),
      http.delete(API('/instance-groups/:id/members'), async ({ params, request }) => {
        const body = (await request.json()) as { instanceIds: number[] }
        removed = { groupId: Number(params.id), instanceIds: body.instanceIds }
        return new HttpResponse(null, { status: 204 })
      }),
    )
    loginMockUser()
    renderWithProviders(<InstanceGroupManager />)

    const group = await screen.findByRole('treeitem', { name: /生存/ })
    await user.click(within(group).getByRole('button', { name: /生存/ }))
    await user.click(await screen.findByRole('checkbox', { name: '选择实例 survival-1' }))
    await user.click(screen.getByRole('button', { name: '移出当前分组' }))

    await waitFor(() => expect(removed).toEqual({ groupId: 2, instanceIds: [1] }))
  })
})
