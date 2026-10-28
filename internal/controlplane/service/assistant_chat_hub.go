package service

// FR-515 Phase D（补充）：运行期事件广播。
//
// 为什么需要它：Phase C 的 sink 只把事件写进库，SSE 端点若只靠轮询事件表，
// 每个打开的聊天页面每秒要查 2.5 次（400ms 间隔），且首字延迟被拉长到一个轮询周期。
// 广播把「事件刚刚产生」这一事实直接推给订阅者，SSE 因此可以即时收帧；
// **轮询仍然保留**作为兜底（进程重启、跨实例、订阅晚于事件产生都能靠它补齐）。
//
// 边界（有意为之，不要扩展成通用消息总线）：
//   - 只做**进程内**扇出。V1 是单 CP 实例（spec §8.1），不需要跨进程。
//   - 订阅者慢就丢弃，不阻塞写入方：事件已在库中，订阅者靠 seq 补读即可自愈。
//     这条是硬约束——若在这里阻塞，一个卡住的浏览器会把整个 run 的执行体拖死。
//   - 不做重放：补历史是 `ListRunEvents(after)` 的职责，hub 只管「此刻之后」。
import (
	"sync"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// assistantEventHub 按 runId 扇出事件。
type assistantEventHub struct {
	mu   sync.Mutex
	subs map[uint]map[*assistantEventSub]struct{}
}

// assistantEventSub 单个订阅者。
//
// ch 有缓冲：模型流式分片成簇到达，缓冲太小会让「稍慢但仍正常」的订阅者被误判为卡死。
type assistantEventSub struct {
	ch chan model.AssistantEvent
}

// assistantEventHubBuffer 单订阅者的缓冲深度。
//
// 取 64：一次模型请求通常产生几十个文本分片加少量工具事件，64 足以吃掉一整簇突发；
// 再大则只是把内存换成「更晚才发现订阅者已死」，没有实际收益。
const assistantEventHubBuffer = 64

func newAssistantEventHub() *assistantEventHub {
	return &assistantEventHub{subs: make(map[uint]map[*assistantEventSub]struct{})}
}

// subscribe 订阅某个 run 的后续事件。
//
// 返回的 cancel 必须被调用（defer），否则 map 里会留下永远不释放的条目。
func (h *assistantEventHub) subscribe(runID uint) (<-chan model.AssistantEvent, func()) {
	sub := &assistantEventSub{ch: make(chan model.AssistantEvent, assistantEventHubBuffer)}
	h.mu.Lock()
	if h.subs[runID] == nil {
		h.subs[runID] = make(map[*assistantEventSub]struct{})
	}
	h.subs[runID][sub] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if set, ok := h.subs[runID]; ok {
			if _, exists := set[sub]; exists {
				delete(set, sub)
				close(sub.ch)
			}
			if len(set) == 0 {
				delete(h.subs, runID)
			}
		}
		h.mu.Unlock()
	}
	return sub.ch, cancel
}

// publish 把事件推给该 run 的所有订阅者。
//
// **非阻塞**：缓冲满即丢弃这一条，由订阅者按 seq 补读。丢弃是有意的取舍——
// 在写入方（模型流消费协程）里等一个慢客户端，会让整个 run 的预算被别人的浏览器吃掉。
func (h *assistantEventHub) publish(ev model.AssistantEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[ev.RunID] {
		select {
		case sub.ch <- ev:
		default:
			// 慢订阅者：丢弃。它下次按 seq 补读时会发现空洞并追平。
		}
	}
}
