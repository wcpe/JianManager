import { afterEach, describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { clearInjections, mockInject } from '@jianmanager/devmock/inject'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import AuditPage from './AuditPage'
import TasksPage from './TasksPage'
import InstancesPage from './InstancesPage'

/**
 * 页内数据区骨架（FR-496 阶段 6 补丁）。
 *
 * 契约是「**页头/筛选条先渲染，只有数据区转骨架**」：切页时用户立刻看到这一页的布局，
 * 数据到了原地填充。此前这几页是 `isLoading ? <p>加载中</p>`——整页只剩一行字，
 * 数据到达才一次性出现，视觉上等同空白 + 跳变。
 *
 * 用 devmock 的延迟注入把请求挂在「未返回」状态（本项目既有的错误/延迟注入框架），
 * 因此可以稳定断言加载中的那一帧，而不是去赌真实请求的时序。
 */
const NEVER_MS = 30_000

/** 断言数据区骨架已就位且旧的「一行加载中」不再出现。 */
function expectDataSkeleton(): void {
  expect(document.querySelectorAll('[data-slot="skeleton-row"]').length).toBeGreaterThan(0)
  expect(screen.queryByText('加载中...')).not.toBeInTheDocument()
}

describe('加载中：页头/筛选条先渲染，数据区转骨架', () => {
  afterEach(() => clearInjections())

  it('审计页：标题、筛选条、列头都在，仅数据行是骨架', () => {
    loginMockUser()
    mockInject('get', '/audit', { kind: 'delay', ms: NEVER_MS })
    renderWithProviders(<AuditPage />)

    // 页头与筛选条不依赖数据 → 立即就是真实内容。
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('审计日志')
    expect(screen.getByPlaceholderText(/操作/)).toBeInTheDocument()
    // 列头是静态列名，也应先就位（骨架期列名/列宽已定，数据到达只是行长出来）。
    expect(screen.getByText('时间')).toBeInTheDocument()
    expect(screen.getByText('用户')).toBeInTheDocument()
    expectDataSkeleton()
  })

  it('任务中心：标题与筛选条先渲染，数据区是骨架', () => {
    loginMockUser()
    mockInject('get', '/tasks', { kind: 'delay', ms: NEVER_MS })
    renderWithProviders(<TasksPage />)

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('任务中心')
    // 筛选条（静态控件）已渲染。
    expect(screen.getByPlaceholderText(/搜索标题/)).toBeInTheDocument()
    expect(screen.getByText('任务')).toBeInTheDocument()
    expectDataSkeleton()
  })

  it('实例管理：标题与工具条先渲染，数据区是骨架', () => {
    loginMockUser()
    mockInject('get', '/instances/search', { kind: 'delay', ms: NEVER_MS })
    mockInject('get', '/instances/aggregate', { kind: 'delay', ms: NEVER_MS })
    renderWithProviders(<InstancesPage />)

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('实例管理')
    expect(screen.getByLabelText('搜索实例')).toBeInTheDocument()
    // 实例列表页的数据区骨架自带列表外壳（计数行 + 列表容器），不是裸行。
    expect(document.querySelector('[data-slot="data-skeleton"]')).toBeInTheDocument()
    expectDataSkeleton()
  })

  it('数据到达后骨架被真实内容替换，不残留', async () => {
    loginMockUser()
    renderWithProviders(<AuditPage />)

    // 种子审计数据到达 → 行渲染，骨架消失（骨架与真实内容是互斥分支）。
    expect(await screen.findByText('user.login')).toBeInTheDocument()
    expect(document.querySelector('[data-slot="skeleton-row"]')).toBeNull()
    expect(document.querySelector('[data-slot="data-skeleton"]')).toBeNull()
  })
})
