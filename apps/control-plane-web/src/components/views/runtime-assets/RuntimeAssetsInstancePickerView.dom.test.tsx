import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { RuntimeAssetsInstancePickerView } from './RuntimeAssetsPageView'

/** 
 * 批量部署右栏的实例候选选择器：候选窗口上限 200（容器 CANDIDATE_LIMIT），
 * 故必须按滚动窗口裁剪 + 在服务端截断时给出提示（此前整列进 DOM、无任何截断提示）。
 */
describe('RuntimeAssetsInstancePickerView（批量部署候选）', () => {
  it('候选达窗口上限时只渲染滚动窗口内的行，并提示服务端截断', () => {
    const items = Array.from({ length: 120 }, (_, i) => ({
      id: i + 1,
      name: `inst-${i + 1}`,
      status: 'RUNNING',
      nodeId: 1,
    }))
    renderWithProviders(
      <RuntimeAssetsInstancePickerView
        items={items}
        total={200}
        selected={[]}
        onToggle={() => {}}
        onQueryChange={() => {}}
      />,
    )

    // 窗口首行在、远处的行不在：确实按滚动窗口裁剪，而非整列进 DOM。
    expect(screen.getByText('inst-1')).toBeInTheDocument()
    expect(screen.queryByText('inst-120')).toBeNull()
    expect(screen.getByTestId('runtime-assets-candidates')).toBeInTheDocument()
    // 服务端窗口 120 / 总数 200：给出截断提示引导继续键入。
    expect(screen.getByText('已显示前 120 项，共 200 项；继续输入可缩小范围')).toBeInTheDocument()
  })
})
