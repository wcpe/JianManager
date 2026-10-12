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

func (g *lifecycleGate) Lock() { _ = g.LockContext(context.Background()) }

// Unlock 归还闸令牌。
//
// 同步保证（修 CP-10）：held 通道由 LockContext 内的 sync.Once 创建，而 Unlock 可能先于
// 任何 Lock 成功被调用（未来出现「先 Unlock 后 Lock」或异常路径跳过 Lock 的写法）。
// 若直接 <-g.held，一旦 Unlock 抢在 once.Do 之前执行，接收方会阻塞在 nil 通道上**永久挂起**
// （nil channel 的接收无超时）——症状是 goroutine 静默挂死而非 panic，极难排查。
// 这里先经同一个 once 确保通道存在，再用非阻塞接收：持有时取出令牌，未持有时为 no-op。
func (g *lifecycleGate) Unlock() {
	g.once.Do(func() { g.held = make(chan struct{}, 1) })
	select {
	case <-g.held:
	default:
	}
}
