package service

// 可重启后台巡检的公共生命周期辅助（R22/R30）。
//
// 背景：本包的多个后台服务（快照保留裁剪 / 配额强制巡检 / 崩溃统计裁剪）都用
// 「`running bool` + `stopCh chan struct{}`」这对字段表达生命周期。该模式的
// 隐藏不变量是：**`Stop` 会 `close` 掉 `stopCh`，故每次 `Start` 必须换一个新
// channel**，否则 Stop→Start 会 panic 在 close of closed channel。
//
// 缺陷现场：三处同形代码原先只有 `SnapshotService.Start` 写了重建，另两处沿用
// 构造期 channel——同一不变量三种写法，后续任一处补热重启即 panic。收敛为本函数：
// 调用方（持锁）传 `&s.stopCh`，取回本次循环应监听的局部引用。
//
// 调用约定：必须在服务自身的 mutex 内调用（与 `running` 的判定同一临界区），
// 使「置 running=true」与「换取新 channel」对 `Stop` 原子可见。
func restartableStop(ch *chan struct{}) chan struct{} {
	*ch = make(chan struct{})
	return *ch
}
