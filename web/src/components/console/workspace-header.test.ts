import { describe, it, expect } from 'vitest'
import { Box, Boxes, Route } from 'lucide-react'
import {
  roleKind,
  roleMeta,
  headerStatusLevel,
  headerIconTone,
  isTransitioning,
  isRunning,
  metaLine,
} from './workspace-header'

describe('roleKind', () => {
  it('归一三态角色', () => {
    expect(roleKind('proxy')).toBe('proxy')
    expect(roleKind('backend')).toBe('backend')
    expect(roleKind('universal')).toBe('universal')
  })
  it('未知/空值按 universal 处理', () => {
    expect(roleKind('')).toBe('universal')
    expect(roleKind('whatever')).toBe('universal')
  })
})

describe('roleMeta', () => {
  it('proxy 走主色 + 路由图标 + 代理文案键', () => {
    const m = roleMeta('proxy')
    expect(m.kind).toBe('proxy')
    expect(m.icon).toBe(Route)
    expect(m.labelKey).toBe('networks.role_proxy')
    expect(m.badgeClass).toContain('text-primary')
  })
  it('backend 走 info 次色 + 方块图标', () => {
    const m = roleMeta('backend')
    expect(m.kind).toBe('backend')
    expect(m.icon).toBe(Box)
    expect(m.labelKey).toBe('networks.role_backend')
    expect(m.badgeClass).toContain('text-status-info')
  })
  it('universal 中性 + 多方块图标', () => {
    const m = roleMeta('universal')
    expect(m.kind).toBe('universal')
    expect(m.icon).toBe(Boxes)
    expect(m.labelKey).toBe('networks.role_universal')
    expect(m.badgeClass).toContain('text-muted-foreground')
  })
})

describe('headerStatusLevel', () => {
  it('状态映射到 FR-061 等级', () => {
    expect(headerStatusLevel('RUNNING')).toBe('success')
    expect(headerStatusLevel('STARTING')).toBe('warning')
    expect(headerStatusLevel('STOPPING')).toBe('warning')
    expect(headerStatusLevel('CRASHED')).toBe('danger')
    expect(headerStatusLevel('STOPPED')).toBe('neutral')
    expect(headerStatusLevel('')).toBe('neutral')
  })
})

describe('headerIconTone', () => {
  it('运行态走主色块', () => {
    expect(headerIconTone('RUNNING')).toBe('primary')
  })
  it('其余态沿用状态等级', () => {
    expect(headerIconTone('STARTING')).toBe('warning')
    expect(headerIconTone('CRASHED')).toBe('danger')
    expect(headerIconTone('STOPPED')).toBe('neutral')
  })
})

describe('isTransitioning / isRunning', () => {
  it('过渡态识别', () => {
    expect(isTransitioning('STARTING')).toBe(true)
    expect(isTransitioning('STOPPING')).toBe(true)
    expect(isTransitioning('RUNNING')).toBe(false)
    expect(isTransitioning('STOPPED')).toBe(false)
  })
  it('运行态识别', () => {
    expect(isRunning('RUNNING')).toBe(true)
    expect(isRunning('STOPPED')).toBe(false)
    expect(isRunning('CRASHED')).toBe(false)
  })
})

describe('metaLine', () => {
  it('类型 · 节点:端口 拼装', () => {
    expect(metaLine('PaperMC', 'node-a', 25565)).toBe('PaperMC · node-a:25565')
  })
  it('端口 ≤0 时省略端口段', () => {
    expect(metaLine('PaperMC', 'node-a', 0)).toBe('PaperMC · node-a')
    expect(metaLine('PaperMC', 'node-a', -1)).toBe('PaperMC · node-a')
  })
  it('空类型不留多余分隔符', () => {
    expect(metaLine('', 'node-a', 25565)).toBe('node-a:25565')
  })
})
