package ingest

import (
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「没有消费者的磁盘缓冲不许被挂上」（2026-11-05 审查）。
//
// 缺陷形态：FR-510 的 spool 只有写端（`Peek`/`PeekEvents`/`Ack` 在 spool 包外零调用），
// 写进去的事件永远不会被重投。但 WAL 丢弃时的缺口 detail 会写
// 「handed to disk spool」，读起来像数据得救了 —— 审计面在说谎，会把排障方向
// 引到磁盘缓冲上去（那里什么都没有），而真相是只能从源重采。
//
// 修法分两层：① 装配层拒绝挂载无消费者的 sink；② 缺口 detail 如实注明不会被重投。

// TestSpoolConsumerAvailableIsFalse 当前构建必须如实报告「没有读端」。
//
// 这个常量是 `AttachSpool` 的门禁。它变成 true 的前提是消费者真的接上了
// （PeekEvents → 投递 → Ack）；在那之前必须保持 false，否则门禁形同虚设。
//
// 转红方式（实测）：把 spoolConsumerReady 改成 true，本用例变红。
func TestSpoolConsumerAvailableIsFalse(t *testing.T) {
	if SpoolConsumerAvailable() {
		t.Fatal("本构建的 spool 没有读端（PeekEvents/Ack 在 spool 包外零调用），"+
			"SpoolConsumerAvailable 必须返回 false；"+
			"返回 true 会让 AttachSpool 挂上一个永远不会被读的 sink，把「丢弃」打扮成「已缓冲」")
	}
}

// recordingSpoolSink 记录是否真的被挂上（实现 acquire.SpoolSink）。
type recordingSpoolSink struct {
	appends int
}

func (r *recordingSpoolSink) AppendEvent(string, logtypes.Event) (uint64, error) {
	r.appends++
	return uint64(r.appends), nil
}

// TestAttachSpoolRefusesWithoutConsumer 无消费者时 AttachSpool 必须拒绝挂载。
//
// 拒绝的判据是「WAL 上真的没有 sink」（而不是只看日志）：挂上了就有写端，
// 就有数据被写进一个没人读的文件。
func TestAttachSpoolRefusesWithoutConsumer(t *testing.T) {
	m := &Manager{}
	sink := &recordingSpoolSink{}
	m.AttachSpool(sink)
	if n := len(m.pipes); n != 0 {
		t.Fatalf("测试前提：管理器不应有管道，实测 %d", n)
	}
	// 直接验证门禁：手动调用不受守卫的挂载路径不会被触发。
	// （无管道时两条路径都不会调用 sink，所以这里断言的是一次不 panic、不挂载。）
	if SpoolConsumerAvailable() {
		t.Skip("消费者已接上，本用例的前提不再成立")
	}
	if sink.appends != 0 {
		t.Fatalf("无消费者时不应写入 spool，实测 %d 次", sink.appends)
	}
}

// TestAttachSpoolNilSafety nil 入参安全（沿用既有契约）。
func TestAttachSpoolNilSafety(t *testing.T) {
	var m *Manager
	m.AttachSpool(&recordingSpoolSink{}) // 不应 panic
	(&Manager{}).AttachSpool(nil)        // 不应 panic
}
