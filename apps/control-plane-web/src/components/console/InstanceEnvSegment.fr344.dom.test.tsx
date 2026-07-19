import { beforeEach, describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { useQueryClient } from '@tanstack/react-query'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import InstanceEnvSegment from './InstanceEnvSegment'

function PollHarness({ instanceId }: { instanceId: number }) {
  const queryClient = useQueryClient()
  return (
    <>
      <button type="button" onClick={() => queryClient.invalidateQueries({ queryKey: ['instance-env', instanceId] })}>
        模拟轮询
      </button>
      <InstanceEnvSegment instanceId={instanceId} />
    </>
  )
}

beforeEach(() => {
  loginMockUser()
})

describe('InstanceEnvSegment（FR-344 当前 SHA 契约）', () => {
  it('分区展示可编辑 configured 与只读 runtime，并保存 envVars', async () => {
    const user = userEvent.setup()
    let savedBody: Record<string, unknown> | null = null
    server.use(
      http.get(API('/instances/:id/env'), () => HttpResponse.json({
        configured: { FOO: 'configured' },
        runtime: {
          FOO: 'runtime',
          JAVA_HOME: '/opt/jdk-21',
          PATH: '/opt/jdk-21/bin:/usr/bin',
        },
        runtimeAvailable: true,
        note: '',
      })),
      http.put(API('/instances/:id'), async ({ request }) => {
        savedBody = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 7 })
      }),
    )

    renderWithProviders(<InstanceEnvSegment instanceId={7} />)

    expect(await screen.findByText('自定义启动环境变量')).toBeInTheDocument()
    expect(screen.getByText('运行时实际环境（只读）')).toBeInTheDocument()
    expect(await screen.findByText('/opt/jdk-21')).toBeInTheDocument()
    expect(screen.getByText('/opt/jdk-21/bin:/usr/bin')).toBeInTheDocument()
    expect(screen.queryByDisplayValue('/opt/jdk-21')).not.toBeInTheDocument()

    const configuredValue = screen.getByDisplayValue('configured')
    await user.clear(configuredValue)
    await user.type(configuredValue, 'edited')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(savedBody).toEqual({ envVars: { FOO: 'edited' } }))
  })

  it('runtime unavailable 时展示后端原因，configured 仍可编辑', async () => {
    server.use(
      http.get(API('/instances/:id/env'), () => HttpResponse.json({
        configured: { OFFLINE_EDITABLE: 'yes' },
        runtime: null,
        runtimeAvailable: false,
        note: '节点离线，无法读取运行时环境',
      })),
    )

    renderWithProviders(<InstanceEnvSegment instanceId={8} />)

    expect(await screen.findByDisplayValue('OFFLINE_EDITABLE')).toBeEnabled()
    expect(screen.getByDisplayValue('yes')).toBeEnabled()
    expect(screen.getByText('节点离线，无法读取运行时环境')).toBeInTheDocument()
  })

  it('运行时轮询返回相同 configured 时不覆盖未保存草稿', async () => {
    const user = userEvent.setup()
    let envReads = 0
    server.use(
      http.get(API('/instances/:id/env'), () => {
        envReads += 1
        return HttpResponse.json({
          configured: { DRAFT_KEY: 'server-value' },
          runtime: { DRAFT_KEY: 'runtime-value' },
          runtimeAvailable: true,
          note: '',
        })
      }),
    )

    renderWithProviders(<PollHarness instanceId={9} />)
    const valueInput = await screen.findByDisplayValue('server-value')
    await user.clear(valueInput)
    await user.type(valueInput, 'unsaved-draft')

    await user.click(screen.getByRole('button', { name: '模拟轮询' }))
    await waitFor(() => expect(envReads).toBeGreaterThanOrEqual(2))
    expect(screen.getByDisplayValue('unsaved-draft')).toBeInTheDocument()
    expect(screen.queryByDisplayValue('server-value')).not.toBeInTheDocument()
  })
})
