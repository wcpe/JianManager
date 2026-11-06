package main

// legacyInstanceCollector 把旧 `ingest.Manager` 适配到新的中立接口。
//
// # 为何需要这个适配器
//
// `grpc.InstanceLogCollector` 的参数类型已从 `ingest.InstanceRegistration`
// 改成中立的 `logconf.InstanceRegistration`——gRPC 接口不能因为“旧链路 someday 要删”
// 就一直绑在一个 23k 行的实现包上。
//
// 而旧 Manager 仍要作为 `log_tailer.enabled: false` 的**回退路径**可用，
// 所以在这里做一次字段逐项转换。适配层放在 apps（同时依赖两边）正是本仓的
// 既有模式（`HitsSourceFunc` / `logTailCollector` 都是这么桥接的）。
//
// **生命周期**：旧链路删除后本文件随之删除。

import (
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
)

type legacyInstanceCollector struct {
	m *ingest.Manager
}

// RegisterInstance 把中立登记结构体转成旧 Manager 的入参。
func (c legacyInstanceCollector) RegisterInstance(reg logconf.InstanceRegistration) error {
	if c.m == nil {
		return nil
	}
	return c.m.RegisterInstance(ingest.InstanceRegistration{
		UUID:         reg.UUID,
		TargetID:     reg.TargetID,
		Generation:   reg.Generation,
		Mode:         reg.Mode,
		WorkDir:      reg.WorkDir,
		RuleKey:      reg.RuleKey,
		RelativePath: reg.RelativePath,
	})
}

// AppendInstanceOutput 直接转交。
func (c legacyInstanceCollector) AppendInstanceOutput(uuid, stream string, data []byte) error {
	if c.m == nil {
		return nil
	}
	return c.m.AppendInstanceOutput(uuid, stream, data)
}
