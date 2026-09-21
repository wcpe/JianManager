import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import type { InstanceInfo } from '@/api/instances'
import { InstanceRowMenu } from './InstancesPage'

/**
 * FR-445/FR-448 回归：行菜单「克隆」的显隐判据是能力画像里的 `clone` 能力——
 * **既不得**用 `role === 'backend'` 硬编码，**也不得**用 `mcSemantics`（MC 世界语义）当门控
 * （proxy 无世界语义 ≠ 不可克隆）。
 */
function inst(over: Partial<InstanceInfo>): InstanceInfo {
  return {
    id: 1,
    uuid: 'i-1',
    nodeId: 1,
    name: 'i-1',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'STOPPED',
    startCommand: '',
    workDir: '/s/i-1',
    serverPort: 25565,
    autoStart: false,
    autoRestart: true,
    tags: '[]',
    createdAt: '2026-01-01T00:00:00Z',
    ...over,
  }
}

async function openMenu() {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: '更多操作' }))
}

const noop = () => {}

describe('InstanceRowMenu 克隆项门控（FR-445）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('backend → 出现「复制」', async () => {
    renderWithProviders(
      <InstanceRowMenu inst={inst({ role: 'backend' })} onTags={noop} onEditConfig={noop} onLimits={noop} onProxy={noop} onClone={noop} onDelete={noop} />,
    )
    await openMenu()
    expect(screen.getByRole('menuitem', { name: '复制' })).toBeInTheDocument()
  })

  it('proxy（有 bcTopology、无 mcSemantics）→ 不出现「复制」，但出现「管理后端」', async () => {
    renderWithProviders(
      <InstanceRowMenu inst={inst({ role: 'proxy' })} onTags={noop} onEditConfig={noop} onLimits={noop} onProxy={noop} onClone={noop} onDelete={noop} />,
    )
    await openMenu()
    expect(screen.queryByRole('menuitem', { name: '复制' })).toBeNull()
    expect(screen.getByRole('menuitem', { name: '管理后端' })).toBeInTheDocument()
  })

  it('universal/generic 角色 → 不出现「复制」', async () => {
    renderWithProviders(
      <InstanceRowMenu inst={inst({ type: 'generic', role: 'universal' })} onTags={noop} onEditConfig={noop} onLimits={noop} onProxy={noop} onClone={noop} onDelete={noop} />,
    )
    await openMenu()
    expect(screen.queryByRole('menuitem', { name: '复制' })).toBeNull()
  })

  it('显隐由能力画像 `clone` 决定，而非 role：backend 但画像无 clone → 不出现「复制」', async () => {
    renderWithProviders(
      <InstanceRowMenu
        inst={inst({
          role: 'backend',
          capabilities: { type: 'minecraft_java', role: 'backend', mcSemantics: true, capabilities: ['overview', 'terminal', 'files'] },
        })}
        onTags={noop} onEditConfig={noop} onLimits={noop} onProxy={noop} onClone={noop} onDelete={noop}
      />,
    )
    await openMenu()
    expect(screen.queryByRole('menuitem', { name: '复制' })).toBeNull()
  })

  it('显隐由能力画像 `clone` 决定，而非 role：proxy 但画像含 clone → 出现「复制」', async () => {
    renderWithProviders(
      <InstanceRowMenu
        inst={inst({
          role: 'proxy',
          capabilities: { type: 'minecraft_java', role: 'proxy', mcSemantics: false, capabilities: ['overview', 'terminal', 'files', 'bcTopology', 'clone'] },
        })}
        onTags={noop} onEditConfig={noop} onLimits={noop} onProxy={noop} onClone={noop} onDelete={noop}
      />,
    )
    await openMenu()
    expect(screen.getByRole('menuitem', { name: '复制' })).toBeInTheDocument()
  })
})
