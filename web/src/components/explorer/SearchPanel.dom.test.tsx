import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import SearchPanel from './SearchPanel'
import { searchFiles } from '@/api/files'

vi.mock('@/api/files', () => ({
  searchFiles: vi.fn(),
}))

const mockedSearchFiles = vi.mocked(searchFiles)

describe('SearchPanel', () => {
  beforeEach(() => {
    mockedSearchFiles.mockReset()
  })

  it('提交目录与扩展名范围并高亮片段命中', async () => {
    const user = userEvent.setup()
    mockedSearchFiles.mockResolvedValue({
      hits: [
        {
          path: 'plugins/Essentials/config.yml',
          line: 2,
          snippet: 'enableFeature: true',
        },
      ],
      truncated: false,
      indexing: false,
    })

    renderWithProviders(<SearchPanel instanceId={1} onOpenHit={vi.fn()} onClose={vi.fn()} />)

    await user.type(screen.getByPlaceholderText('输入关键字搜索文件内容…'), 'feature')
    await user.type(screen.getByLabelText('目录范围'), 'plugins')
    await user.type(screen.getByLabelText('扩展名范围'), '.yml,.yaml')

    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
    const snippet = screen.getByText((_, node) => node?.textContent === 'enableFeature: true')
    expect(within(snippet).getByText('Feature')).toBeInTheDocument()
    await waitFor(() => {
      expect(mockedSearchFiles).toHaveBeenLastCalledWith(1, 'feature', 'content', 200, {
        rootPath: 'plugins',
        extensions: ['.yml', '.yaml'],
      })
    })
  })
})

describe('SearchPanel 索引中态（FR-113）', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mockedSearchFiles.mockReset()
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it('首次返回 indexing=true 时显示索引中，并自动重试后展示结果', async () => {
    mockedSearchFiles
      .mockResolvedValueOnce({ hits: [], truncated: false, indexing: true })
      .mockResolvedValueOnce({
        hits: [{ path: 'server.properties', line: 1, snippet: 'online-mode=false' }],
        truncated: false,
        indexing: false,
      })

    renderWithProviders(<SearchPanel instanceId={1} onOpenHit={vi.fn()} onClose={vi.fn()} />)
    fireEvent.change(screen.getByPlaceholderText('输入关键字搜索文件内容…'), {
      target: { value: 'online-mode' },
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(300)
    })
    expect(mockedSearchFiles).toHaveBeenCalledTimes(1)
    expect(screen.getByText('索引中，首次建立索引，请稍候…')).toBeInTheDocument()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(mockedSearchFiles).toHaveBeenCalledTimes(2)
    expect(screen.getByText('server.properties')).toBeInTheDocument()
    expect(screen.getByText('online-mode=false')).toBeInTheDocument()
  })

  it('用户修改 query 后，旧的索引中重试不会覆盖新查询结果', async () => {
    let oldQueryCalls = 0
    mockedSearchFiles.mockImplementation(async (_instanceId, query) => {
      if (query === 'old') {
        oldQueryCalls += 1
        if (oldQueryCalls === 1) return { hits: [], truncated: false, indexing: true }
        return {
          hits: [{ path: 'old.yml', line: 1, snippet: 'old result' }],
          truncated: false,
          indexing: false,
        }
      }
      return {
        hits: [{ path: 'new.yml', line: 2, snippet: 'new result' }],
        truncated: false,
        indexing: false,
      }
    })

    renderWithProviders(<SearchPanel instanceId={1} onOpenHit={vi.fn()} onClose={vi.fn()} />)
    const input = screen.getByPlaceholderText('输入关键字搜索文件内容…')
    fireEvent.change(input, { target: { value: 'old' } })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(300)
    })
    expect(mockedSearchFiles).toHaveBeenCalledTimes(1)
    expect(screen.getByText('索引中，首次建立索引，请稍候…')).toBeInTheDocument()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(900)
    })
    fireEvent.change(input, { target: { value: 'new' } })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(mockedSearchFiles).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(200)
    })
    expect(mockedSearchFiles).toHaveBeenCalledTimes(2)
    expect(mockedSearchFiles).toHaveBeenLastCalledWith(1, 'new', 'content', 200, {
      rootPath: '',
      extensions: [],
    })
    expect(screen.getByText('new.yml')).toBeInTheDocument()
    expect(screen.queryByText('old.yml')).not.toBeInTheDocument()
  })
})
