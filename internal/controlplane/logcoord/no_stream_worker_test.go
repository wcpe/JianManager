package logcoord

import "context"

// noStreamWorker 给测试替身提供 stream 三方法的最小实现。
//
// # 为什么要单独一个文件
//
// WorkerClient 接口新增了 Streams / StreamFieldNames / StreamFieldValues。
// 包里原有十几个手工 fake（cursorWorker / pagedWorker / pr61WindowWorker …），
// 它们只关心自己那个场景，根本不会调 stream 接口——逐个补 3 个方法既咀噪
// 又容易在下次接口扩展时再漏一遍。
//
// 做法：让这些 fake **嵌入** noStreamWorker，得到「未实现」语义
// （Unsupported=true），与真实老 Worker 的行为一致：
// CP 会将其归入「本次不提供该能力」而不是报错，与生产上混版节点的处理相同。
//
// fakeWorker（fake_test.go）**不**用这个：它有真实实现片段，用来测合并逻辑。
type noStreamWorker struct{}

func (noStreamWorker) Streams(context.Context, WorkerSearchRequest) (*WorkerStreamsResponse, error) {
	return &WorkerStreamsResponse{Unsupported: true, Error: "streams unimplemented in test fake"}, nil
}

func (noStreamWorker) StreamFieldNames(context.Context, WorkerSearchRequest) (*WorkerStreamFieldNamesResponse, error) {
	return &WorkerStreamFieldNamesResponse{Unsupported: true, Error: "stream field names unimplemented in test fake"}, nil
}

func (noStreamWorker) StreamFieldValues(context.Context, WorkerSearchRequest, string) (*WorkerStreamFieldValuesResponse, error) {
	return &WorkerStreamFieldValuesResponse{Unsupported: true, Error: "stream field values unimplemented in test fake"}, nil
}
