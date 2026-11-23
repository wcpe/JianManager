import { describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { BaselineDriftPanelView } from '@/components/views/config-baselines/ConfigBaselinesPageView'
import ConfigBaselinesPage from './ConfigBaselinesPage'

/** 渲染配置基线页（假后端种子：group:2 的 server.properties 基线，成员实例 1 与其不一致→漂移）。 */
function renderPage() {
  loginMockUser()
  const user = userEvent.setup()
  renderWithProviders(<ConfigBaselinesPage />)
  return { user }
}

describe('ConfigBaselinesPage（配置基线与漂移收敛）', () => {
  it('列出基线', async () => {
    renderPage()
    /* 
      生效范围列已人话化（`group:2` → 「分组 2」），原始 key 移入 title。
      这里同时断言两者：人话文案可见 + 原始值仍可取回（排查时要精确值）。
    */
    const scopeCell = await screen.findByText('分组 2')
    expect(scopeCell).toBeInTheDocument()
    expect(scopeCell.getAttribute('title')).toBe('group:2')
    expect(screen.getByText('server.properties')).toBeInTheDocument()
  })

  it('漂移检测：scope 内实例与基线不一致即标记漂移', async () => {
    const { user } = renderPage()
    await screen.findByText('server.properties')

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
    await screen.findByText('server.properties')

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
    await screen.findByText('server.properties')

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
    // 生效范围列已人话化，列表就绪锚点改用文件名（原始 scope 仍可从 title 取回）。
    await screen.findByText('server.properties')

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
    await screen.findByText('server.properties')

    await user.click(screen.getByTestId('baseline-drift-1'))
    expect(await screen.findByText('1 台读取失败，无法判定')).toBeInTheDocument()
    // 读取失败行 drifted=0，但不得显示「已全部一致」绿标。
    expect(screen.queryByText('已全部一致')).toBeNull()
  })

  it('列表取数失败时渲染错误态，而不是「尚无配置基线」', async () => {
    server.use(
      http.get(API('/config-baselines'), () =>
        HttpResponse.json({ error: 'INTERNAL', message: '数据库不可用' }, { status: 500 }),
      ),
    )
    renderPage()

    expect(await screen.findByText('读取失败')).toBeInTheDocument()
    // 失败被读成「没有基线」会让运维以为集群里根本没配过基线，故两者必须区分。
    expect(screen.queryByText('尚无配置基线')).toBeNull()
  })

  it('切换基线时不残留上一条的收敛汇总', async () => {
    // 两条基线 + 各自的漂移明细：A(#1) 收敛后显示汇总，切到 B(#2) 必须清空。
    server.use(
      http.get(API('/config-baselines'), () =>
        HttpResponse.json({
          baselines: [
            { id: 1, scopeKey: 'group:2', filePath: 'server.properties', content: 'a=1', contentHash: 'h1', message: '', authorId: 1, createdAt: '', updatedAt: '' },
            { id: 2, scopeKey: 'group:2', filePath: 'ops.properties', content: 'b=1', contentHash: 'h2', message: '', authorId: 1, createdAt: '', updatedAt: '' },
          ],
        }),
      ),
      http.get(API('/config-baselines/1/drift'), () =>
        HttpResponse.json({
          items: [{ instanceId: 1, instanceName: '生存服', drift: true, currentHash: 'x', baselineHash: 'h1', hasVersion: true }],
          drifted: 1,
        }),
      ),
      http.get(API('/config-baselines/2/drift'), () =>
        HttpResponse.json({
          items: [
            { instanceId: 2, instanceName: '创造服', drift: true, currentHash: 'y', baselineHash: 'h2', hasVersion: true },
            { instanceId: 3, instanceName: '空岛服', drift: false, currentHash: 'h2', baselineHash: 'h2', hasVersion: true },
          ],
          drifted: 1,
        }),
      ),
    )
    const { user } = renderPage()
    // 两条基线的 scopeKey 同为 group:2，故按文件路径等待列表就绪。
    await screen.findByText('server.properties')

    await user.click(screen.getByTestId('baseline-drift-1'))
    await screen.findByText('1/1 台漂移')
    await user.click(screen.getByTestId('baseline-converge'))
    expect(await screen.findByTestId('baseline-converge-result')).toBeInTheDocument()

    // 切到 B：标题变为 #2 后，A 的「目标 …/成功 …/失败 …」不得继续挂在 B 的面板里。
    await user.click(screen.getByTestId('baseline-drift-2'))
    await screen.findByText('漂移检测 · 基线 #2')
    await waitFor(() => expect(screen.queryByTestId('baseline-converge-result')).toBeNull())
  })

  it('漂移面板自身在切换基线时清空收敛汇总（不依赖调用方的 key）', async () => {
    const onConverge = vi.fn().mockResolvedValue({ targeted: 1, succeeded: 1, failed: 0 })
    const drift = {
      items: [{ instanceId: 1, instanceName: '生存服', drift: true, currentHash: 'x' }],
      drifted: 1,
    }
    /** 
     * 就地切换 baselineId 的宿主：面板必须在不重挂载的前提下自己清空汇总
     * （外壳传 `key` 能兜住，但面板被直接挂载时没人替它清）。
     */
    function Harness() {
      const [id, setId] = useState(1)
      return (
        <>
          <button type="button" onClick={() => setId(2)}>switch-baseline</button>
          <BaselineDriftPanelView
            baselineId={id}
            drift={drift}
            onRefresh={() => {}}
            onConverge={onConverge}
            onClose={() => {}}
          />
        </>
      )
    }
    const user = userEvent.setup()
    renderWithProviders(<Harness />)

    await user.click(screen.getByTestId('baseline-converge'))
    expect(await screen.findByTestId('baseline-converge-result')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'switch-baseline' }))
    await waitFor(() => expect(screen.queryByTestId('baseline-converge-result')).toBeNull())
  })

  it('漂移明细达千级时只渲染滚动窗口内的行（虚拟化）', async () => {
    const items = Array.from({ length: 200 }, (_, i) => ({
      instanceId: i + 1,
      instanceName: `inst-${i + 1}`,
      drift: true,
      currentHash: 'x',
      baselineHash: 'h',
      hasVersion: true,
    }))
    server.use(
      http.get(API('/config-baselines/1/drift'), () => HttpResponse.json({ items, drifted: items.length })),
    )
    const { user } = renderPage()
    await screen.findByText('server.properties')

    await user.click(screen.getByTestId('baseline-drift-1'))
    await screen.findByText('200/200 台漂移')

    const rendered = screen.getAllByTestId('baseline-drift-row')
    expect(rendered.length).toBeGreaterThan(0)
    expect(rendered.length).toBeLessThan(items.length)
    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪，而非整表进 DOM。
    expect(screen.getByText('inst-1')).toBeInTheDocument()
    expect(screen.queryByText('inst-200')).toBeNull()
    // 窗口外的高度由占位撑开，滚动条长度仍按全部行计算。
    expect(screen.getByTestId('baseline-drift-spacer-after')).toBeInTheDocument()
  })
})
