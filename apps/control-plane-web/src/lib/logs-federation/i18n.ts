/**
 * logsFederation.* i18n 键与 zh/en 文案（FR-482 foundation）。
 *
 * **当前状态（2026-10-12 订正）**：{@link LOGS_FEDERATION_KEYS} 是 helper 产出的 key 的
 * **唯一真源**，仍在使用；而 {@link LOGS_FEDERATION_LOCALES}（zh/en 文案对象）的
 * **过渡期使命已完成**——`src/i18n/zh.json` / `en.json` 的顶层 `logsFederation` 段
 * **早已建立**（本文件头原先写的「正式接入 i18n 资源包时，把 LOGS_FEDERATION_LOCALES
 * 合并进去」那一步**已经做过**），渲染层读的是那两份真实包。
 *
 * 因此**不要**把本对象当作「当前生效的文案」读：它与真实包此后各自演化，已实测漂移
 * （桩 56 叶 / 真实 83 叶、27 个键真实包有而桩无、43 条同键文案不同）。
 *
 * 它现在只剩两个用途：
 *   1. 运行时**零消费**的兼容导出（`index.ts` re-export 供既有引用不破）；
 *   2. 测试里的**陈旧度哨兵**：`helpers.test.ts` 断言「桩的叶子必须是真实包的子集」，
 *      一旦真实包删键而桩还留着，该断言转红提示同步。
 *
 * **测试断言一律读真实包**（`import zh from '@/i18n/zh.json'`），不要读本对象——
 * 读桩等于桩 vs 桩自证：真实包漏键它不会红（该缺陷形态由 FE-8 回归锁定）。
 */

/** helper 可能产出的全部 i18n key（静态清单，供 missing-keys / 对账使用）。 */
export const LOGS_FEDERATION_KEYS = {
  coverageComplete: 'logsFederation.coverage.complete',
  coveragePartial: 'logsFederation.coverage.partial',
  coverageFailed: 'logsFederation.coverage.failed',
  coverageUnknown: 'logsFederation.coverage.unknown',

  viewReadyComplete: 'logsFederation.viewState.ready_complete',
  viewReadyCompleteEmpty: 'logsFederation.viewState.ready_complete_empty',
  viewVerifying: 'logsFederation.viewState.verifying',
  viewPartial: 'logsFederation.viewState.partial',
  viewRateLimited: 'logsFederation.viewState.rate_limited',
  viewOffline: 'logsFederation.viewState.offline',
  viewEngineNotReady: 'logsFederation.viewState.engine_not_ready',
  viewRehydrateFailed: 'logsFederation.viewState.rehydrate_failed',
  viewBacklog: 'logsFederation.viewState.backlog',
  viewTruncated: 'logsFederation.viewState.truncated',
  viewLegacy: 'logsFederation.viewState.legacy',
  viewGap: 'logsFederation.viewState.gap',
  viewArchiveMissing: 'logsFederation.viewState.archive_missing',
  viewStale: 'logsFederation.viewState.view_stale',
  viewQueryFailed: 'logsFederation.viewState.query_failed',
  viewUnknown: 'logsFederation.viewState.unknown',

  actionRetry: 'logsFederation.action.retry',
  actionOpenNodes: 'logsFederation.action.openNodes',
  actionStartRehydrate: 'logsFederation.action.startRehydrate',
  actionRequestOnline: 'logsFederation.action.requestOnline',
  actionReopenView: 'logsFederation.action.reopenView',
  actionContactAdmin: 'logsFederation.action.contactAdmin',

  exportAllowed: 'logsFederation.export.allowed',
  exportBlockedIncomplete: 'logsFederation.export.blockedIncomplete',
  exportBlockedFailed: 'logsFederation.export.blockedFailed',
  exportBlockedViewStale: 'logsFederation.export.blockedViewStale',
  exportBlockedUnresolved: 'logsFederation.export.blockedUnresolved',
  exportBlockedNotReady: 'logsFederation.export.blockedNotReady',
  exportBlockedPermission: 'logsFederation.export.blockedPermission',
  exportBlockedRowLimit: 'logsFederation.export.blockedRowLimit',
  exportBlockedByteBudget: 'logsFederation.export.blockedByteBudget',
  exportBlockedBudget: 'logsFederation.export.blockedBudget',
  exportPermissionRecheckHint: 'logsFederation.export.permissionRecheckHint',
  exportNextCursorHint: 'logsFederation.export.nextCursorHint',
} as const

/** reason 码 → i18n key 前缀下的叶子名（未知码回退 unknown）。 */
export function reasonI18nKey(reason: string): string {
  const leaf = reason in REASON_LEAF ? REASON_LEAF[reason as keyof typeof REASON_LEAF] : 'unknown'
  return `logsFederation.reasons.${leaf}`
}

const REASON_LEAF = {
  RATE_LIMITED: 'rateLimited',
  ENGINE_NOT_READY: 'engineNotReady',
  LOG_UNSUPPORTED: 'logUnsupported',
  LEGACY: 'legacy',
  REHYDRATE_FAILED: 'rehydrateFailed',
  BACKLOG: 'backlog',
  TRUNCATED: 'truncated',
  OFFLINE: 'offline',
  ARCHIVE_MISSING: 'archiveMissing',
  GAP: 'gap',
  VIEW_STALE: 'viewStale',
  DUPLICATE_UNRESOLVED: 'duplicateUnresolved',
  PERMISSION_REVOKED: 'permissionRevoked',
  CANCELLED: 'cancelled',
  BUDGET_EXCEEDED: 'budgetExceeded',
  EXPORT_INCOMPLETE: 'exportIncomplete',
  RECOVERY_REQUIRED: 'recoveryRequired',
} as const

/** zh / en 文案对象（顶层 key 与 i18n 资源包一致）。 */
export const LOGS_FEDERATION_LOCALES = {
  zh: {
    logsFederation: {
      coverage: {
        complete: '覆盖完整',
        partial: '覆盖不完整（部分结果）',
        failed: '查询失败（非空结果）',
        unknown: '覆盖状态未知',
      },
      viewState: {
        ready_complete: '结果完整',
        ready_complete_empty: '所选范围内无日志（覆盖完整）',
        verifying: '正在校验完整覆盖，先展示已取到的结果；导出时将做完整校验',
        partial: '部分结果：覆盖不完整',
        rate_limited: '请求过于频繁，本次查询未取到数据；已保留上一次结果，请稍后重试',
        offline: '部分节点离线，结果不完整',
        engine_not_ready: '日志查询引擎未就绪',
        rehydrate_failed: '归档回灌失败，历史层结果不完整',
        backlog: '采集积压，最新日志可能尚未入库',
        truncated: '结果已截断',
        legacy: '含 Legacy 数据源（不在联邦视图内）',
        gap: '存在数据缺口',
        archive_missing: '归档层缺失',
        view_stale: '查询视图已失效，请重新查询',
        query_failed: '查询失败，请重试',
        unknown: '无法解析覆盖状态',
      },
      reasons: {
        engineNotReady: '查询引擎未就绪',
        rateLimited: '请求过于频繁',
        logUnsupported: '节点日志能力未启用',
        legacy: 'Legacy 数据源',
        rehydrateFailed: '回灌失败',
        backlog: '采集积压',
        truncated: '结果截断',
        offline: '节点离线',
        archiveMissing: '归档缺失',
        gap: '数据缺口',
        viewStale: '视图已失效',
        duplicateUnresolved: '重复事件未对账',
        permissionRevoked: '权限已撤销',
        cancelled: '查询已取消',
        budgetExceeded: '资源预算不足',
        exportIncomplete: '导出不完整',
        recoveryRequired: '启动恢复中',
        unknown: '未知原因',
      },
      action: {
        retry: '重试',
        openNodes: '查看节点状态',
        startRehydrate: '发起回灌',
        requestOnline: '仅查在线节点',
        reopenView: '重新打开视图',
        contactAdmin: '联系管理员',
      },
      export: {
        allowed: '覆盖完整且校验通过，可下载',
        blockedIncomplete: '覆盖不完整，不可下载导出附件',
        blockedFailed: '查询失败，不可下载导出附件',
        blockedViewStale: '视图已失效，请重新查询后再导出',
        blockedUnresolved: '重复事件未对账，导出不发布成功附件',
        blockedNotReady: '导出尚未就绪或校验未通过',
        blockedPermission: '权限不足或已撤销，无法下载',
        blockedRowLimit: '导出数据超过单次行数上限，请缩小时间范围或调大服务端 log_export.max_rows 配置',
        blockedByteBudget: '导出数据超过单次体积上限，请缩小时间范围或调大服务端 log_export.max_bytes 配置',
        blockedBudget: '导出数据超过服务端单次上限，请缩小时间范围或调大 log_export 配置',
        permissionRecheckHint: '下载前将再次复核权限',
        nextCursorHint: '游标结束仅表示当前视图枚举完毕，不代表全部历史完整。',
      },
    },
  },
  en: {
    logsFederation: {
      coverage: {
        complete: 'Coverage complete',
        partial: 'Coverage incomplete (partial results)',
        failed: 'Query failed (not an empty result)',
        unknown: 'Coverage status unknown',
      },
      viewState: {
        ready_complete: 'Complete results',
        ready_complete_empty: 'No logs in selected range (coverage complete)',
        verifying: 'Verifying full coverage; showing available results (export performs a full verification)',
        partial: 'Partial results: coverage incomplete',
        rate_limited: 'Too many requests; this query returned no data. Previous results are kept; retry shortly',
        offline: 'Some nodes offline; results incomplete',
        engine_not_ready: 'Log query engine not ready',
        rehydrate_failed: 'Rehydrate failed; archive layer incomplete',
        backlog: 'Ingest backlog; newest logs may be missing',
        truncated: 'Results truncated',
        legacy: 'Includes legacy sources (outside federation view)',
        gap: 'Data gap detected',
        archive_missing: 'Archive layer missing',
        view_stale: 'Query view is stale; re-run the query',
        query_failed: 'Query failed; retry',
        unknown: 'Unable to parse coverage status',
      },
      reasons: {
        engineNotReady: 'Query engine not ready',
        rateLimited: 'Too many requests',
        logUnsupported: 'Node log platform not enabled',
        legacy: 'Legacy data source',
        rehydrateFailed: 'Rehydrate failed',
        backlog: 'Ingest backlog',
        truncated: 'Results truncated',
        offline: 'Node offline',
        archiveMissing: 'Archive missing',
        gap: 'Data gap',
        viewStale: 'View stale',
        duplicateUnresolved: 'Duplicates unresolved',
        permissionRevoked: 'Permission revoked',
        cancelled: 'Query cancelled',
        budgetExceeded: 'Resource budget exceeded',
        exportIncomplete: 'Export incomplete',
        recoveryRequired: 'Startup recovery in progress',
        unknown: 'Unknown reason',
      },
      action: {
        retry: 'Retry',
        openNodes: 'View node status',
        startRehydrate: 'Start rehydrate',
        requestOnline: 'Query online nodes only',
        reopenView: 'Reopen view',
        contactAdmin: 'Contact administrator',
      },
      export: {
        allowed: 'Coverage complete and verified; download available',
        blockedIncomplete: 'Coverage incomplete; export download is not available',
        blockedFailed: 'Query failed; export download is not available',
        blockedViewStale: 'View is stale; re-query before exporting',
        blockedUnresolved: 'Duplicates unresolved; export will not publish a success artifact',
        blockedNotReady: 'Export not ready or not verified',
        blockedPermission: 'Insufficient or revoked permission; cannot download',
        blockedRowLimit:
          'Export exceeds the per-request row limit; narrow the time range or raise the server log_export.max_rows setting',
        blockedByteBudget:
          'Export exceeds the per-request size limit; narrow the time range or raise the server log_export.max_bytes setting',
        blockedBudget:
          'Export exceeds the server per-request limit; narrow the time range or raise the log_export setting',
        permissionRecheckHint: 'Permission is rechecked before download',
        nextCursorHint:
          'A null cursor only ends enumeration of the current view; it does not mean full history is complete.',
      },
    },
  },
} as const
