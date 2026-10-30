package pipeline

import "github.com/wcpe/JianManager/internal/worker/logs/acquire"

// Cursor 返回本源当前采集游标快照（供 ingest 的游标表落盘）。
//
// 没有 tailer（STDIO_PRIMARY 且无 path）时返回 ok=false：那种源没有文件游标可言。
//
// **必须持 p.mu**（2026-10-30 实测修正，-race 可复现）：tailer.cursor / logicalPos 是
// 普通字段，由 Poll 在持 p.mu 的临界区内反复写（tailer.go 的 `t.cursor += ...`）。
// 不持锁读它们就是与采集轮的直接数据竞争（`WARNING: DATA RACE`，读 −写同地址）。
// 早先这里注释写着「只 stat 一次并读两个标量，与采集临界区无写冲突」——那句话是错的，
// 两个标量恰恰就是采集临界区的写对象。
//
// 拍锁的代价可控：本方法只在游标表刷盘时被逐源调一次，而刷盘点在 pollOnce 的
// wg.Wait() 之后（此时本轮的采集 worker 已全部退出），实际几乎不可能排队；
// 即使撞上另一路径的 Poll，等待上限也只是那一轮采集本身。
func (p *Pipeline) Cursor() (acquire.SourceCursor, bool) {
	if p == nil {
		return acquire.SourceCursor{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tailer == nil {
		return acquire.SourceCursor{}, false
	}
	return p.tailer.Cursor(), true
}
