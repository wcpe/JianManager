package process

import "context"

// dockerStrategy 是 docker 启动方式的占位实现。
// docker 模式（通过 Docker API 管理容器化游戏服）尚未落地，本批不做，
// 所有操作返回 ErrNotImplemented 以避免误用未实现能力。
// 参见 ADR-003（docker 不在其范围内，后续版本规划）。
type dockerStrategy struct{}

func newDockerStrategy(_ *Manager, _ CommandSpec) *dockerStrategy {
	return &dockerStrategy{}
}

func (d *dockerStrategy) Start(context.Context) error { return ErrNotImplemented }
func (d *dockerStrategy) Stop() error                 { return ErrNotImplemented }
func (d *dockerStrategy) Kill() error                 { return ErrNotImplemented }
func (d *dockerStrategy) SendCommand(string) error    { return ErrNotImplemented }
func (d *dockerStrategy) State() InstanceState        { return StateStopped }
func (d *dockerStrategy) Close() error                { return nil }
