import { describe, it, expect, beforeEach } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import { useState } from 'react'
import { renderWithProviders } from '@/test/render'
import CleanScopeEditor from './CleanScopeEditor'
import type { ManifestFileLike } from '@/lib/client-publish-wizard'

/**
 * CleanScopeEditor 组件测试（FR-262）：
 * 三态标记、右键菜单、多选、父子联动、产出正确、clean-all 全红。
 */

/** 构树文件 fixture：mods（含 sub）/ config（含 foo）/ 根散文件。 */
const FILES: ManifestFileLike[] = [
  { path: 'mods/a.jar', sync: 'strict', platform: '', size: 100 },
  { path: 'mods/sub/b.jar', sync: 'strict', platform: '', size: 200 },
  { path: 'config/foo/c.toml', sync: 'strict', platform: '', size: 50 },
  { path: 'options.txt', sync: 'strict', platform: '', size: 10 },
]

/** 最近一次 onChange 产出（模块级，便于断言）。 */
let lastChange: { managedDirs: string[]; cleanExclude: string[] } | null = null

/** 受控包装器：onChange 回写 state + 捕获产出。 */
function Harness({
  files = FILES,
  initialManagedDirs = [],
  initialCleanExclude = [],
  cleanAll = false,
}: {
  files?: ManifestFileLike[]
  initialManagedDirs?: string[]
  initialCleanExclude?: string[]
  cleanAll?: boolean
} = {}) {
  const [md, setMd] = useState(initialManagedDirs)
  const [ce, setC] = useState(initialCleanExclude)
  return (
    <CleanScopeEditor
      files={files}
      managedDirs={md}
      cleanExclude={ce}
      onChange={(managedDirs, cleanExclude) => {
        lastChange = { managedDirs, cleanExclude }
        setMd(managedDirs)
        setC(cleanExclude)
      }}
      cleanAll={cleanAll}
    />
  )
}

/** 取目录行 by path。 */
function dirRow(container: HTMLElement, path: string): HTMLElement {
  const el = container.querySelector(`[data-testid="clean-scope-dir-row"][data-dir-path="${path}"]`)
  if (!el) throw new Error(`未找到目录行: ${path}`)
  return el as HTMLElement
}

/** 右键目录 → 等待菜单出现。 */
async function openMenu(path: string, container: HTMLElement) {
  fireEvent.contextMenu(dirRow(container, path), { button: 2, clientX: 100, clientY: 100 })
  await screen.findByTestId('clean-scope-context-menu')
}

/** 点菜单项。 */
function clickMenuItem(menuId: string) {
  const btn = screen.getByTestId(menuId)
  fireEvent.click(btn)
}

describe('CleanScopeEditor（FR-262）', () => {
  beforeEach(() => {
    lastChange = null
  })

  it('空文件 → 显示空态提示', () => {
    renderWithProviders(<Harness files={[]} />)
    expect(screen.getByTestId('clean-scope-empty')).toBeInTheDocument()
  })

  it('渲染目录树（含深层嵌套）', () => {
    const { container } = renderWithProviders(<Harness />)
    expect(dirRow(container, 'mods')).toBeInTheDocument()
    expect(dirRow(container, 'mods/sub')).toBeInTheDocument()
    expect(dirRow(container, 'config')).toBeInTheDocument()
    expect(dirRow(container, 'config/foo')).toBeInTheDocument()
  })

  it('初始无标记 → 所有目录 data-mark=none', () => {
    const { container } = renderWithProviders(<Harness />)
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'none')
    expect(dirRow(container, 'config')).toHaveAttribute('data-mark', 'none')
  })

  it('右键标记为清理 → onChange 产出 managedDirs', async () => {
    const { container } = renderWithProviders(<Harness />)
    await openMenu('mods', container)
    clickMenuItem('clean-scope-mark-clean')
    expect(lastChange?.managedDirs).toContain('mods')
    // 标记后视觉变 clean
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'clean')
  })

  it('右键标记为排除 → onChange 产出 cleanExclude', async () => {
    const { container } = renderWithProviders(<Harness />)
    await openMenu('config', container)
    clickMenuItem('clean-scope-mark-exclude')
    expect(lastChange?.cleanExclude).toContain('config')
    expect(dirRow(container, 'config')).toHaveAttribute('data-mark', 'exclude')
  })

  it('取消标记 → onChange 产出不含该目录', async () => {
    const { container } = renderWithProviders(<Harness initialManagedDirs={['mods']} />)
    // 初始 mods 已标记 clean
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'clean')
    await openMenu('mods', container)
    clickMenuItem('clean-scope-unmark')
    expect(lastChange?.managedDirs).not.toContain('mods')
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'none')
  })

  it('父子联动：标记父 clean → 子继承 clean 视觉', async () => {
    const { container } = renderWithProviders(<Harness />)
    await openMenu('mods', container)
    clickMenuItem('clean-scope-mark-clean')
    // mods/sub 继承 clean
    expect(dirRow(container, 'mods/sub')).toHaveAttribute('data-mark', 'clean')
    // 产出去子：只产出 mods，不产出 mods/sub
    expect(lastChange?.managedDirs).toEqual(['mods'])
  })

  it('父子联动：子单独标记 exclude → 父变 mixed', async () => {
    const { container } = renderWithProviders(<Harness initialManagedDirs={['mods']} />)
    // 先标记 mods 为 clean
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'clean')
    // 标记 mods/sub 为 exclude
    await openMenu('mods/sub', container)
    clickMenuItem('clean-scope-mark-exclude')
    // 父 mods 变 mixed
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'mixed')
    // 子 mods/sub 为 exclude
    expect(dirRow(container, 'mods/sub')).toHaveAttribute('data-mark', 'exclude')
    // 产出 managedDirs 含 mods，cleanExclude 含 mods/sub
    expect(lastChange?.managedDirs).toContain('mods')
    expect(lastChange?.cleanExclude).toContain('mods/sub')
  })

  it('Ctrl+点击多选 → 右键批量标记', async () => {
    const { container } = renderWithProviders(<Harness />)
    const mods = dirRow(container, 'mods')
    const config = dirRow(container, 'config')
    // Ctrl+click 追加选
    fireEvent.click(mods)
    fireEvent.click(config, { ctrlKey: true })
    // 两个都选中
    expect(mods).toHaveAttribute('data-selected', 'true')
    expect(config).toHaveAttribute('data-selected', 'true')
    // 右键 mods（选中集中）→ 批量标记为清理
    await openMenu('mods', container)
    clickMenuItem('clean-scope-mark-clean')
    // 两个目录都被标记 clean
    expect(lastChange?.managedDirs).toContain('mods')
    expect(lastChange?.managedDirs).toContain('config')
  })

  it('Shift+点击连选 → 选中范围内所有目录', () => {
    const { container } = renderWithProviders(<Harness />)
    const mods = dirRow(container, 'mods')
    const config = dirRow(container, 'config')
    // click mods 设锚点
    fireEvent.click(mods)
    // Shift+click config → 连选 mods 和 config（可见目录范围内）
    fireEvent.click(config, { shiftKey: true })
    expect(mods).toHaveAttribute('data-selected', 'true')
    expect(config).toHaveAttribute('data-selected', 'true')
  })

  it('cleanAll=true → 全目录标记为清理红色', () => {
    const { container } = renderWithProviders(<Harness cleanAll />)
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'clean')
    expect(dirRow(container, 'mods/sub')).toHaveAttribute('data-mark', 'clean')
    expect(dirRow(container, 'config')).toHaveAttribute('data-mark', 'clean')
    // 根容器标注 clean-all
    expect(screen.getByTestId('clean-scope-tree')).toHaveAttribute('data-clean-all', 'true')
  })

  it('cleanAll=true → 右键不弹菜单', async () => {
    const { container } = renderWithProviders(<Harness cleanAll />)
    fireEvent.contextMenu(dirRow(container, 'mods'), { button: 2, clientX: 100, clientY: 100 })
    // 菜单不应出现
    expect(screen.queryByTestId('clean-scope-context-menu')).not.toBeInTheDocument()
  })

  it('初始 managedDirs + cleanExclude → 正确回显标记', () => {
    const { container } = renderWithProviders(
      <Harness initialManagedDirs={['mods']} initialCleanExclude={['config']} />,
    )
    expect(dirRow(container, 'mods')).toHaveAttribute('data-mark', 'clean')
    expect(dirRow(container, 'mods/sub')).toHaveAttribute('data-mark', 'clean')
    expect(dirRow(container, 'config')).toHaveAttribute('data-mark', 'exclude')
    expect(dirRow(container, 'config/foo')).toHaveAttribute('data-mark', 'exclude')
  })

  it('折叠/展开目录', () => {
    const { container } = renderWithProviders(<Harness />)
    // 默认展开——mods/sub 可见
    expect(dirRow(container, 'mods/sub')).toBeInTheDocument()
    // 点击折叠箭头
    const toggle = container.querySelector('[data-testid="clean-scope-toggle"][data-dir-path="mods"]') as HTMLElement
    fireEvent.click(toggle)
    // mods/sub 不再可见（被折叠）
    expect(container.querySelector('[data-dir-path="mods/sub"]')).toBeNull()
    // 再点击展开
    fireEvent.click(toggle)
    expect(dirRow(container, 'mods/sub')).toBeInTheDocument()
  })

  it('颜色图例显示四态标签', () => {
    renderWithProviders(<Harness />)
    expect(screen.getByText('清理')).toBeInTheDocument()
    expect(screen.getByText('排除')).toBeInTheDocument()
    expect(screen.getByText('混合')).toBeInTheDocument()
    expect(screen.getByText('不管理')).toBeInTheDocument()
  })
})
