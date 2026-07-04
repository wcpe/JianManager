import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import InstanceWizardPage from './InstanceWizardPage'

beforeEach(() => {
  loginMockUser()
})

async function pickOption(user: ReturnType<typeof userEvent.setup>, triggerName: RegExp | string, optionName: RegExp | string) {
  await user.click(screen.getByRole('button', { name: triggerName }))
  await user.click(await screen.findByRole('button', { name: optionName }))
}

describe('InstanceWizardPage Docker 创建向导（FR-078）', () => {
  it('一键 Minecraft 预设提交 docker 镜像、空启动命令与 EULA 环境变量', async () => {
    const user = userEvent.setup()
    let payload: Record<string, unknown> | undefined
    server.use(
      http.post(API('/instances'), async ({ request }) => {
        payload = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 99, uuid: 'i-docker-new', status: 'STOPPED', ...payload }, { status: 201 })
      }),
    )

    renderWithProviders(<InstanceWizardPage />, { route: '/instances/new' })

    await user.type(screen.getByPlaceholderText('Survival Server'), 'docker-mc')
    await pickOption(user, '选择节点', /alpha/)
    await user.click(screen.getByRole('button', { name: '下一步' }))

    await pickOption(user, /daemon/, 'docker')
    await user.click(screen.getByRole('button', { name: '下一步' }))

    expect(await screen.findByText('Docker 29.4.1 可用')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /一键填充/ }))
    expect(screen.getByDisplayValue('itzg/minecraft-server:latest')).toBeInTheDocument()
    expect(screen.getByDisplayValue('EULA')).toBeInTheDocument()
    expect(screen.getByDisplayValue('TRUE')).toBeInTheDocument()
    await user.type(screen.getByPlaceholderText('1.5'), '1.5')
    await user.type(screen.getByPlaceholderText('2048'), '2048')
    await user.type(screen.getByPlaceholderText('10240'), '10240')

    await user.click(screen.getByRole('button', { name: '下一步' }))
    await user.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(payload).toBeDefined())
    expect(payload).toMatchObject({
      nodeId: 1,
      name: 'docker-mc',
      type: 'minecraft_java',
      processType: 'docker',
      startCommand: '',
      image: 'itzg/minecraft-server:latest',
      cpuLimit: 1.5,
      memLimitMb: 2048,
      diskLimitMb: 10240,
      envVars: { EULA: 'TRUE' },
    })
  })

  it('Docker 检测 HTTP 失败时阻止继续创建', async () => {
    const user = userEvent.setup()
    server.use(
      http.post(API('/nodes/:id/docker/check'), () => HttpResponse.json({ error: 'NODE_OFFLINE', message: '节点未连接' }, { status: 503 })),
    )

    renderWithProviders(<InstanceWizardPage />, { route: '/instances/new' })

    await user.type(screen.getByPlaceholderText('Survival Server'), 'docker-mc')
    await pickOption(user, '选择节点', /alpha/)
    await user.click(screen.getByRole('button', { name: '下一步' }))

    await pickOption(user, /daemon/, 'docker')
    await user.click(screen.getByRole('button', { name: '下一步' }))

    expect(await screen.findByText(/该节点未检测到可用的 Docker/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /一键填充/ }))
    expect(screen.getByRole('button', { name: '下一步' })).toBeDisabled()
  })
})
