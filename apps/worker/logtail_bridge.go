package main

import (
	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
)

// logTailCollector 把新采集器（logtail.Runner）接到旧链路的两个入口上。
//
// # 为什么要适配而不是让 logtail 直接实现接口
//
// `grpc.InstanceLogCollector` 的签名用的是 `logconf.InstanceRegistration`，
// 而 logtail 包**刻意不 import ingest**（它与旧链路平行存在，阶段三要删的正是 ingest）。
// 让它为了一条接口去依赖一个待删包，等于把两者重新绑在一起。
//
// 适配层放在 apps（同时依赖两边）正是本仓既有的跨包适配模式：
// `HitsSourceFunc` 也是这么桥接的。
type logTailCollector struct {
	runner *logtail.Runner
}

// RegisterInstance 把实例日志绑定写进新采集器的绑定表。
//
// # 为什么只做这一件事
//
// 旧链路登记还要建源、建账本、建 WAL。新采集器的模型是「扫描发现文件、按绑定归源」，
// 没有“注册一个源”这个动作——只需知道「这个实例的工作目录属于哪个 target」。
func (c logTailCollector) RegisterInstance(reg logconf.InstanceRegistration) error {
	if c.runner == nil {
		return nil
	}
	bindings := c.runner.Bindings()
	if bindings == nil {
		return nil
	}
	return bindings.Put(logtail.BindingEntry{
		UUID:     reg.UUID,
		TargetID: reg.TargetID,
		WorkDir:  reg.WorkDir,
		Mode:     reg.Mode,
	})
}

// AppendInstanceOutput 把进程输出写给 stdio 桥接器。
//
// # 为什么 FILE_PRIMARY 实例要直接返回
//
// 这类实例（Minecraft Java）自己把日志写进 `logs/latest.log`，而进程输出与
// 那个文件的内容**是同一份**。再落一个 stdio 文件就是重复采集：同一条日志
// 进 VL 两次（event_id 虽相同，但行数翻倍、查询侧去重不全时能看到双份）。
//
// 这个判断只能在这里做：桥接器本身不知道实例的采集模式，而旧链路的
// `AppendInstanceOutput` 正是按 mode 分流的（FILE_PRIMARY 返回 nil）。
func (c logTailCollector) AppendInstanceOutput(uuid, stream string, data []byte) error {
	if c.runner == nil {
		return nil
	}
	if b := c.runner.Bindings(); b != nil {
		if e, ok := b.Get(uuid); ok && e.Mode == logconf.ModeFilePrimary {
			return nil
		}
	}
	return c.runner.Stdio().Append(uuid, stream, data)
}
