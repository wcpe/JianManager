import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { requireAuth } from '@/mocks/auth-middleware'

/**
 * 供给与模板域 mock handler（FR-202）。
 * 覆盖三个 api 模块的端点：
 *  - templates.ts：GET/POST /templates、DELETE /templates/:id
 *  - provision.ts：GET /cores（版本列表 / 解析下载信息）、POST /instances/provision/bukkit、POST /instances/provision/proxy
 *  - clone.ts：POST /instances/:id/clone
 * 结构照 spec §7：domainRoute 注册每个端点、db('<集合>') 读写、受保护端点首行 requireAuth。
 */

/** 假后端模板行。字段严格匹配 web/src/api/templates.ts 的 TemplateInfo（含 createdAt/updatedAt）。 */
export interface Template {
  id: number
  uuid: string
  name: string
  type: string
  description: string
  startCommand: string
  defaultWorkDir: string
  downloadUrl: string
  createdAt: string
  updatedAt: string
}

const NOW = '2026-06-01T08:00:00Z'

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const templates = db<Template>('templates', () => [
  {
    id: 1,
    uuid: 't-paper',
    name: 'Paper 1.21',
    type: 'minecraft_java',
    description: '高性能 Bukkit 服务端，适合生存/小游戏后端',
    startCommand: 'java -Xmx{{ram}}G -jar paper.jar nogui',
    defaultWorkDir: '',
    downloadUrl: 'https://example.com/paper-1.21.jar',
    createdAt: NOW,
    updatedAt: NOW,
  },
  {
    id: 2,
    uuid: 't-velocity',
    name: 'Velocity 代理',
    type: 'generic',
    description: '现代化群组服代理，统一入口与转发',
    startCommand: 'java -Xmx512M -jar velocity.jar',
    defaultWorkDir: '/srv/velocity',
    downloadUrl: 'https://example.com/velocity.jar',
    createdAt: NOW,
    updatedAt: NOW,
  },
  {
    id: 3,
    uuid: 't-vanilla',
    name: '原版 1.21',
    type: 'minecraft_java',
    description: 'Mojang 原版服务端',
    startCommand: 'java -jar server.jar nogui',
    defaultWorkDir: '',
    downloadUrl: 'https://example.com/server.jar',
    createdAt: NOW,
    updatedAt: NOW,
  },
])

/** 显式播种入口（与 auth.ts 一致：集合声明已在 import 期完成，本函数仅为契约对齐 / 幂等保险）。 */
export function seed(): void {
  db<Template>('templates', () => templates.list())
}

/** 假核心版本表：按核心类型给出新→旧版本，供 GET /cores 的版本列表分支返回。 */
const CORE_VERSIONS: Record<string, string[]> = {
  paper: ['1.21.1', '1.21', '1.20.6'],
  velocity: ['3.3.0-SNAPSHOT', '3.2.0-SNAPSHOT'],
  waterfall: ['1.21', '1.20'],
  bungeecord: ['latest'],
}

export const handlers = [
  // ── 模板 ──────────────────────────────────────────────
  domainRoute('get', '/templates', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(templates.list())
  }),

  domainRoute('post', '/templates', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      name: string
      type: string
      description?: string
      startCommand: string
      downloadUrl?: string
      defaultWorkDir?: string
    }
    const ts = new Date().toISOString()
    const row = templates.insert({
      uuid: `t-${body.name}-${templates.list().length + 1}`,
      name: body.name,
      type: body.type,
      description: body.description ?? '',
      startCommand: body.startCommand,
      defaultWorkDir: body.defaultWorkDir ?? '',
      downloadUrl: body.downloadUrl ?? '',
      createdAt: ts,
      updatedAt: ts,
    })
    return HttpResponse.json(row, { status: 201 })
  }),

  domainRoute('delete', '/templates/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { id } = info.params as { id: string }
    if (!templates.get(Number(id))) {
      return HttpResponse.json({ error: 'NOT_FOUND', message: '模板不存在' }, { status: 404 })
    }
    templates.remove(Number(id))
    return HttpResponse.json({ message: '已删除' })
  }),

  // ── 供给：核心查询 ────────────────────────────────────
  // 无 mcVersion → 版本列表；带 mcVersion → 解析下载信息（对应 useCoreVersions / useResolvedCore）。
  domainRoute('get', '/cores', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const type = url.searchParams.get('type') || 'paper'
    const mcVersion = url.searchParams.get('mcVersion')
    if (!mcVersion) {
      return HttpResponse.json({ type, versions: CORE_VERSIONS[type] ?? ['1.21.1', '1.20.6'] })
    }
    const buildParam = url.searchParams.get('build')
    const build = buildParam ? Number(buildParam) : 196
    return HttpResponse.json({
      type,
      mcVersion,
      build,
      filename: `${type}-${mcVersion}-${build}.jar`,
      downloadUrl: `https://example.com/${type}/${mcVersion}/${build}.jar`,
      sha256: 'a'.repeat(64),
    })
  }),

  // ── 供给：一键搭建 Paper 后端 ────────────────────────
  domainRoute('post', '/instances/provision/bukkit', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      nodeId: number
      name: string
      coreType: string
      mcVersion: string
      groupId?: number
    }
    // 写入共享 instances 集合（与实例域联动：provision 后实例列表应出现该实例）。
    const inst = db<{ id: number; [k: string]: unknown }>('instances').insert({
      uuid: `i-${body.name}-${Date.now()}`,
      nodeId: body.nodeId,
      name: body.name,
      type: 'minecraft_java',
      role: 'backend',
      processType: 'daemon',
      status: 'STOPPED',
      startCommand: `java -jar ${body.coreType}.jar nogui`,
      workDir: `/srv/instances/${body.name}`,
      serverPort: 25565,
      autoStart: false,
      autoRestart: true,
      tags: '',
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(inst, { status: 201 })
  }),

  // ── 供给：一键搭建代理 ───────────────────────────────
  domainRoute('post', '/instances/provision/proxy', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      nodeId: number
      name: string
      proxyType: string
    }
    const inst = db<{ id: number; [k: string]: unknown }>('instances').insert({
      uuid: `i-${body.name}-${Date.now()}`,
      nodeId: body.nodeId,
      name: body.name,
      type: 'generic',
      role: 'proxy',
      processType: 'daemon',
      status: 'STOPPED',
      startCommand: `java -jar ${body.proxyType}.jar`,
      workDir: `/srv/instances/${body.name}`,
      serverPort: 25577,
      autoStart: false,
      autoRestart: true,
      tags: '',
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(
      {
        instance: inst,
        // Velocity 才返回一次性 forwarding secret。
        forwardingSecret: body.proxyType === 'velocity' ? 'mock-fwd-secret' : undefined,
        registrations: [],
        warnings: [],
      },
      { status: 201 },
    )
  }),

  // ── 复制子服 ─────────────────────────────────────────
  domainRoute('post', '/instances/:id/clone', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // 源实例 id 在 :id 路径参数中；mock 不校验源存在，直接为副本分配新资源。
    const body = (await info.request.json()) as {
      name: string
      motd?: string
      levelName?: string
      registerToProxyIds?: number[]
      dryRun?: boolean
    }
    const allocated = { workDir: `/srv/instances/${body.name}`, serverPort: 25566, queryPort: 25566 }
    if (body.dryRun) {
      // 预检：不落库、不返回 instance，仅给出将分配的资源。
      return HttpResponse.json({
        allocated,
        excluded: ['world/session.lock', 'logs/'],
        registrations: [],
        warnings: [],
        dryRun: true,
      })
    }
    const inst = db<{ id: number; [k: string]: unknown }>('instances').insert({
      uuid: `i-${body.name}-${Date.now()}`,
      nodeId: 1,
      name: body.name,
      type: 'minecraft_java',
      role: 'backend',
      processType: 'daemon',
      status: 'STOPPED',
      startCommand: `java -jar paper.jar nogui`,
      workDir: allocated.workDir,
      serverPort: allocated.serverPort,
      autoStart: false,
      autoRestart: true,
      tags: '',
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(
      {
        instance: inst,
        allocated,
        excluded: ['world/session.lock', 'logs/'],
        registrations: (body.registerToProxyIds ?? []).map((pid) => ({ proxyId: pid, alias: body.name })),
        warnings: [],
        dryRun: false,
      },
      { status: 201 },
    )
  }),
]
