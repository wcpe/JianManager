package ingest

// 2026-10-29：本文件原有两条用例（`TestBackfillBlockedCategoriesAreVisible` /
// `TestReclaimStallWarnsOnceThenThrottles`）随补段链一并删除。
//
// 它们验的是「补段受阻的四类静默 return 必须可观测（BlockedConfigHold / BlockedNoMaterial /
// BlockedWatermarkMoved 计数）+ 回收连续多轮无进展必须告警」。被测对象
// `backfillDeliveryConfirmedCoverage` 已随受管恢复分段机制删除（WAL 改纯内存后没有副本
// 需要接管）⇒ 那四类“补段被拒”的分类不复存在，“补段受阻可见性”也就无从谈起。
//
// 仍然活着的可见性面（未被本改动触及，由其它用例覆盖）：
//   - `noteReclaimBlocked("no-confirmed-copy", …)`：`releaseRecovery` 的 `Delivery >= to` 闸
//     未通过时打点——这条闸正是 2026-10-21 数据丢失事故的防线，仍然存在；
//   - `ReclaimRollingStats` 的 Rounds / Advanced / FirstAdvance：由
//     `sweep_lagging_rolling_test.go` 的投递步用例断言。
