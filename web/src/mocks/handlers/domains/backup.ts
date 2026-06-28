import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { requireAuth } from '@/mocks/auth-middleware'
import type { BackupInfo, CreateBackupBody } from '@/api/backups'
import type { BackupStorage, CreateBackupStorageBody } from '@/api/backupStorages'
import type { ScheduleInfo, ScheduleLogInfo, CreateScheduleBody, UpdateScheduleBody } from '@/api/schedules'

/**
 * 备份与计划域 mock handler（FR-207）。
 * 覆盖 web/src/api/{backups,backupStorages,schedules}.ts 的全部 endpoint；
 * 字段严格匹配各自 interface（含后端 model 对齐的数值枚举），照 spec §7 范式办理。
 */

// db.Entity 要求 id 为 number | string；各 interface 的 id 均为 number，直接复用类型。
// 集合在本域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。

const backups = db<BackupInfo>('backups', () => [
  {
    id: 1,
    uuid: 'bk-full-1',
    instanceId: 1,
    name: 'full-2026-06-01T02:00:00',
    filePath: '/data/backups/inst-1/full-1.tar.zst',
    fileSizeMb: 128.5,
    type: 1, // 定时触发
    mode: 0, // 全量
    status: 2, // 已完成
    createdAt: '2026-06-01T02:00:00Z',
  },
  {
    id: 2,
    uuid: 'bk-inc-2',
    instanceId: 1,
    name: 'inc-2026-06-02T02:00:00',
    filePath: '/data/backups/inst-1/inc-2.tar.zst',
    fileSizeMb: 12.3,
    type: 0, // 手动触发
    mode: 1, // 增量
    status: 2, // 已完成
    parentId: 1, // 挂在全量备份 #1 后形成链
    createdAt: '2026-06-02T02:00:00Z',
  },
  {
    id: 3,
    uuid: 'bk-s3-3',
    instanceId: 2,
    name: 'full-2026-06-03T02:00:00',
    filePath: '',
    fileSizeMb: 256.0,
    type: 0,
    mode: 0,
    status: 2,
    storageId: 1, // 远程 S3 存储
    storageKey: 'inst-2/full-3.tar.zst',
    createdAt: '2026-06-03T02:00:00Z',
  },
])

const backupStorages = db<BackupStorage>('backupStorages', () => [
  {
    id: 1,
    name: 's3-primary',
    type: 's3',
    endpoint: 's3.amazonaws.com',
    bucket: 'jm-backups',
    region: 'us-east-1',
    prefix: 'prod/',
    accessKeyEnv: '${JIANMANAGER_BACKUP_S3_AK}',
    secretKeyEnv: '${JIANMANAGER_BACKUP_S3_SK}',
    useSsl: true,
    createdAt: '2026-05-20T08:00:00Z',
  },
  {
    id: 2,
    name: 'sftp-offsite',
    type: 'sftp',
    endpoint: 'backup.example.com:22',
    bucket: '',
    region: '',
    prefix: 'jianmanager/',
    accessKeyEnv: '${JIANMANAGER_BACKUP_SFTP_USER}',
    secretKeyEnv: '${JIANMANAGER_BACKUP_SFTP_PASS}',
    useSsl: false,
    createdAt: '2026-05-21T08:00:00Z',
  },
])

const schedules = db<ScheduleInfo>('schedules', () => [
  {
    id: 1,
    uuid: 'sch-restart-1',
    instanceId: 1,
    name: '每晚重启',
    cronExpr: '0 4 * * *',
    action: 'restart',
    payload: '',
    enabled: true,
    lastRun: '2026-06-27T04:00:00Z',
    createdAt: '2026-05-01T00:00:00Z',
  },
  {
    id: 2,
    uuid: 'sch-backup-2',
    instanceId: 1,
    name: '每日备份',
    cronExpr: '0 2 * * *',
    action: 'backup',
    payload: '',
    enabled: false,
    lastRun: null,
    createdAt: '2026-05-02T00:00:00Z',
  },
])

const scheduleLogs = db<ScheduleLogInfo>('scheduleLogs', () => [
  {
    id: 1,
    scheduleId: 1,
    action: 'restart',
    status: 'success',
    error: '',
    startedAt: '2026-06-27T04:00:00Z',
    finishedAt: '2026-06-27T04:00:05Z',
  },
  {
    id: 2,
    scheduleId: 1,
    action: 'restart',
    status: 'failed',
    error: '实例未在运行',
    startedAt: '2026-06-26T04:00:00Z',
    finishedAt: '2026-06-26T04:00:01Z',
  },
])

export const handlers = [
  // ---- 备份（FR-013/056/057）----
  domainRoute('get', '/instances/:id/backups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number(info.params.id)
    return HttpResponse.json(backups.list((b) => b.instanceId === instanceId))
  }),

  domainRoute('post', '/instances/:id/backups', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const instanceId = Number(info.params.id)
    const body = ((await info.request.json().catch(() => ({}))) ?? {}) as CreateBackupBody
    const now = new Date().toISOString()
    // 增量须有可作基准的已完成全量/任意备份，否则后端回 422（与 API.md 对齐）。
    if (body.incremental) {
      const base = backups.find((b) => b.instanceId === instanceId && b.status === 2)
      if (!base) {
        return HttpResponse.json(
          { error: 'BUSINESS_ERROR', message: '无可作基准的已完成备份' },
          { status: 422 },
        )
      }
    }
    const created = backups.insert({
      uuid: `bk-${Date.now()}`,
      instanceId,
      name: body.name ?? `${body.incremental ? 'inc' : 'full'}-${now.slice(0, 19)}`,
      filePath: body.storageId ? '' : `/data/backups/inst-${instanceId}/${Date.now()}.tar.zst`,
      fileSizeMb: body.incremental ? 8.0 : 64.0,
      type: 0, // 手动触发
      mode: body.incremental ? 1 : 0,
      status: 2, // mock 立即完成，便于联动断言
      storageId: body.storageId,
      createdAt: now,
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('post', '/backups/:id/restore', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const target = backups.get(Number(info.params.id))
    if (!target) return HttpResponse.json({ error: 'NOT_FOUND', message: '备份不存在' }, { status: 404 })
    return HttpResponse.json({ message: 'restore started' })
  }),

  domainRoute('delete', '/backups/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    // 被增量子备份依赖时拒绝（422），避免割裂备份链（与 API.md 对齐）。
    if (backups.find((b) => b.parentId === id)) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '存在依赖此备份的增量备份' }, { status: 422 })
    }
    backups.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  // ---- 备份存储后端（FR-057，平台管理员）----
  domainRoute('get', '/backup-storages', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(backupStorages.list())
  }),

  domainRoute('post', '/backup-storages', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as CreateBackupStorageBody
    const created = backupStorages.insert({
      name: body.name,
      type: body.type,
      endpoint: body.endpoint ?? '',
      bucket: body.bucket ?? '',
      region: body.region ?? '',
      prefix: body.prefix ?? '',
      accessKeyEnv: body.accessKeyEnv ?? '',
      secretKeyEnv: body.secretKeyEnv ?? '',
      useSsl: body.useSsl ?? true,
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('delete', '/backup-storages/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    // 被备份引用时拒绝（422）。
    if (backups.find((b) => b.storageId === id)) {
      return HttpResponse.json({ error: 'BUSINESS_ERROR', message: '该后端被备份引用，无法删除' }, { status: 422 })
    }
    backupStorages.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  // ---- 定时任务（FR-012/153）----
  domainRoute('get', '/schedules', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const instanceId = url.searchParams.get('instanceId')
    const rows = instanceId
      ? schedules.list((s) => s.instanceId === Number(instanceId))
      : schedules.list()
    return HttpResponse.json(rows)
  }),

  domainRoute('post', '/schedules', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as CreateScheduleBody
    const created = schedules.insert({
      uuid: `sch-${Date.now()}`,
      instanceId: body.instanceId,
      name: body.name,
      cronExpr: body.cronExpr,
      action: body.action,
      payload: body.payload ?? '',
      enabled: true,
      lastRun: null,
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('put', '/schedules/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = schedules.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '任务不存在' }, { status: 404 })
    const body = (await info.request.json()) as UpdateScheduleBody
    const patch: Partial<ScheduleInfo> = {}
    if (body.cronExpr !== undefined) patch.cronExpr = body.cronExpr
    if (body.action !== undefined) patch.action = body.action
    if (body.enabled !== undefined) patch.enabled = body.enabled
    if (body.payload !== undefined) patch.payload = body.payload
    const updated = schedules.update(id, patch)
    return HttpResponse.json(updated)
  }),

  domainRoute('delete', '/schedules/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    schedules.remove(Number(info.params.id))
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('get', '/schedules/:id/logs', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const scheduleId = Number(info.params.id)
    const url = new URL(info.request.url)
    const page = Number(url.searchParams.get('page') ?? '1')
    const pageSize = Number(url.searchParams.get('pageSize') ?? '20')
    const items = scheduleLogs.list((l) => l.scheduleId === scheduleId)
    return HttpResponse.json({ items, total: items.length, page, pageSize })
  }),
]
