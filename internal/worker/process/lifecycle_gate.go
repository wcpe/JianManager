package process

import (
	"context"
	"sync"
)

// lifecycleGate 是零值可用的生命周期闸；等待可取消，不为排队请求创建代等协程。
// 通道内的唯一令牌表示闸已被占用，同一实例的操作仍然严格串行。
type lifecycleGate struct {
	once sync.Once
	held chan struct{}
}

func (g *lifecycleGate) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	g.once.Do(func() { g.held = make(chan struct{}, 1) })
	select {
	case g.held <- struct{}{}:
		// 获得令牌和取消同时就绪时优先取消，并归还令牌。
		if err := ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *lifecycleGate) Lock()   { _ = g.LockContext(context.Background()) }
func (g *lifecycleGate) Unlock() { <-g.held }
