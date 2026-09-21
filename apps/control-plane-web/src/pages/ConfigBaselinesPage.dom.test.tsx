import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import ConfigBaselinesPage from './ConfigBaselinesPage'

/** 渲染配置基线页（假后端种子：group:2 的 server.properties 基线，成员实例 1 与其不一致→漂移）。 */
function renderPage() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(<ConfigBaselinesPage />)
  return { user }
}

describe('ConfigBaselinesPage（FR-458 配置基线与漂移收敛）', () => {
  it('列出基线', async () => {
    renderPage()
    expect(await screen.findByText('group:2')).toBeInTheDocument()
    expect(screen.getByText('server.properties')).toBeInTheDocument()
  })

  it('漂移检测：scope 内实例与基线不一致即标记漂移', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-drift-1'))
    expect(await screen.findByText('1/1 台漂移')).toBeInTheDocument()
    // 逐台明细：实例 1 标记为漂移。
    expect(screen.getByText('漂移')).toBeInTheDocument()
  })

  it('一键收敛：推送基线后复核残余漂移为 0', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-drift-1'))
    await screen.findByText('1/1 台漂移')

    await user.click(screen.getByTestId('baseline-converge'))
    await waitFor(() => expect(screen.getByTestId('baseline-converge-result')).toBeInTheDocument())
    // 收敛后复核：残余漂移归零，出现「已全部一致」。
    expect(await screen.findByText('已全部一致')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('0/1 台漂移')).toBeInTheDocument())
  })

  it('新建基线：保存后出现在列表', async () => {
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-create'))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText('文件路径'))
    await user.type(within(dialog).getByLabelText('文件路径'), 'custom.properties')
    await user.type(within(dialog).getByLabelText('基线内容'), 'a=1')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('custom.properties')).toBeInTheDocument()
  })

  it('新建基线：分组 scope 从组织树分组选择器选取，提交的是分组 id（非用户组 id）', async () => {
    const bodies: Array<Record<string, unknown>> = []
    server.use(
      http.post(API('/config-baselines'), async ({ request }) => {
        const body = (await request.json()) as Record<string, unknown>
        bodies.push(body)
        return HttpResponse.json({
          id: 99,
          scopeKey: body.scopeKey,
          filePath: body.filePath,
          content: body.content,
          contentHash: 'h',
          message: body.message ?? '',
          authorId: 1,
          createdAt: '',
          updatedAt: '',
        })
      }),
    )
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-create'))
    const dialog = await screen.findByRole('dialog')

    // 分组 scope 不再让用户手填数字：改为从组织树分组列表选择（假后端种子：亚洲区/生存/创造）。
    await user.selectOptions(within(dialog).getByLabelText('范围类型'), 'group')
    const scopeSelect = within(dialog).getByTestId('baseline-scope-value')
    await waitFor(() =>
      expect(within(scopeSelect).getByRole('option', { name: /生存/ })).toBeInTheDocument(),
    )
    expect(within(dialog).getByTestId('baseline-scope-group-hint')).toBeInTheDocument()

    await user.selectOptions(scopeSelect, '2')
    await user.clear(within(dialog).getByLabelText('文件路径'))
    await user.type(within(dialog).getByLabelText('文件路径'), 'ops.properties')
    await user.type(within(dialog).getByLabelText('基线内容'), 'x=1')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    // 提交的必须是所选组织树分组 id（2=生存），而不是任何手填的/用户组的数字。
    expect(bodies[0].scopeKey).toBe('group:2')
    expect(bodies[0].filePath).toBe('ops.properties')
  })

  it('编辑既有基线：分组已被删除时仍保留原 scope 取值（不静默丢分组）', async () => {
    // 造一条指向不存在分组的「脏数据」基线（分组被删后残留），先于首屏查询注册。
    server.use(
      http.get(API('/config-baselines'), () =>
        HttpResponse.json({
          baselines: [
            {
              id: 77,
              scopeKey: 'group:9999',
              filePath: 'server.properties',
              content: 'a=1',
              contentHash: 'h',
              message: '',
              authorId: 1,
              createdAt: '',
              updatedAt: '',
            },
          ],
        }),
      ),
    )
    const { user } = renderPage()
    await screen.findByText('group:9999')

    await user.click(screen.getByRole('button', { name: '编辑' }))
    const dialog = await screen.findByRole('dialog')
    const scopeSelect = within(dialog).getByTestId('baseline-scope-value') as HTMLSelectElement
    expect(scopeSelect.value).toBe('9999')
    expect(within(dialog).getByTestId('baseline-scope-group-hint')).toBeInTheDocument()
  })

  it('全部实例读取失败时不误报「已全部一致」，提示无法判定', async () => {
    server.use(
      http.get(API('/config-baselines/1/drift'), () =>
        HttpResponse.json({
          items: [
            { instanceId: 1, instanceName: 'a', drift: false, currentHash: '', baselineHash: 'h', hasVersion: false, error: '节点离线' },
          ],
          drifted: 0,
        }),
      ),
    )
    const { user } = renderPage()
    await screen.findByText('group:2')

    await user.click(screen.getByTestId('baseline-drift-1'))
    expect(await screen.findByText('1 台读取失败，无法判定')).toBeInTheDocument()
    // 读取失败行 drifted=0，但不得显示「已全部一致」绿标。
    expect(screen.queryByText('已全部一致')).toBeNull()
  })
})
