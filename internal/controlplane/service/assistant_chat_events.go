package service

// Phase D：运行事件与 run 的**只读**访问器。
//
// 为什么单独开一个文件而不写进 assistant_chat.go：那份文件是 Phase C 的运行时本体，
// 已经不变量密集（见其文件头注释）。Phase D 是传输层，只需要「按 seq 读事件」「按归属读 run」
// 两条纯读路径；把它塞进运行时文件会让「读路径」与「状态机写入」在同一文件里交织，
// 后来者很难判断某段代码是否参与状态迁移。这里只做读，绝不写库、绝不推进状态。
//
// 与 spec §6.2 的对应关系：事件**永不因保留策略被删**，因此补读游标只有一种失效情形——
// after 大于该 run 的 maxSeq（伪造或跨 run 误用游标），此时返回 ErrAssistantInvalidCursor（400），
// 不是 410：410 会让前端以为「事件被清理了，历史不可恢复」，而事实是游标本身不合法。

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// assistantEventPageMax 单次事件读取的条数上限。
//
// 为什么要有界：补读端点与 SSE 轮询共用这条读路径，无界读取在一个跑到预算上限的 run 上
// 会把整张事件表倒进一个 HTTP 响应。取 500 是因为它远大于一次轮询间隔内可能产生的事件数
// （模型流式分片按秒计），同时又不至于让单个响应体失控。
const assistantEventPageMax = 500

// GetRun 读取一轮运行（仅限所有者）。
//
// 越权与不存在共用 ErrAssistantChatNotFound（§6：RUN_NOT_FOUND 含越权，不区分），
// 分开回 403 与 404 等于给攻击者一个「这个 runId 存在」的探测接口。
func (s *AssistantChatService) GetRun(owner, runID uint) (*model.AssistantRun, error) {
	var run model.AssistantRun
	err := s.db.Where("id = ? AND owner_user_id = ?", runID, owner).First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssistantChatNotFound
		}
		return nil, fmt.Errorf("查询 run 失败: %w", err)
	}
	return &run, nil
}

// ListRunEvents 读取某 run 中 seq > after 的事件，按 seq 升序返回（§6.2）。
//
// after 的合法性由**该 run 自身的 maxSeq**判定：
//   - after < 0 或不是整数（调用方解析阶段已挡）→ 非法游标；
//   - after > maxSeq → 非法游标（400 INVALID_CURSOR），而不是空数组。
//
// 为什么 after==maxSeq 必须返回空数组而不是报错：这是「我已经收到最后一条，继续等新事件」
// 的正常状态，SSE 轮询每次都会这样问一次。把它当成非法游标会让每轮空轮询都失败。
//
// run 的归属先校验再读事件：事件表里没有 owner_user_id，若不先校验 run 归属，
// 任何人都能拿别人的 runId 读走他的对话过程流。
func (s *AssistantChatService) ListRunEvents(owner, runID uint, after int) ([]model.AssistantEvent, error) {
	if _, err := s.GetRun(owner, runID); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, fmt.Errorf("%w: after 不能为负数", ErrAssistantInvalidCursor)
	}
	maxSeq, err := s.MaxRunEventSeq(runID)
	if err != nil {
		return nil, err
	}
	if after > maxSeq {
		// 伪造的游标或跨 run 误用：明确拒绝而不是回空数组——空数组会让前端以为
		// 「这一轮没有任何事件」，从而永久停在错误的游标上。
		return nil, fmt.Errorf("%w: after=%d 超出该运行的最大 seq=%d", ErrAssistantInvalidCursor, after, maxSeq)
	}
	rows := make([]model.AssistantEvent, 0, 16)
	if err := s.db.Where("run_id = ? AND seq > ?", runID, after).
		Order("seq asc").Limit(assistantEventPageMax).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询运行事件失败: %w", err)
	}
	return rows, nil
}

// MaxRunEventSeq 返回该 run 已落库的最大事件 seq（无事件时为 0）。
//
// 单独导出是给 SSE 的「终态且已补齐」判定用：轮询到空页时还要区分「暂时没新事件」与
// 「已经读到这条流的末尾」。seq 每 run 从 1 连续、不允许空洞（§5.3），所以「已读到的 lastSeq
// 等于 maxSeq」即表示补齐。
func (s *AssistantChatService) MaxRunEventSeq(runID uint) (int, error) {
	var maxSeq int
	if err := s.db.Model(&model.AssistantEvent{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(seq), 0)").Scan(&maxSeq).Error; err != nil {
		return 0, fmt.Errorf("查询运行事件序号失败: %w", err)
	}
	return maxSeq, nil
}

// SubscribeRunEvents 订阅某个 run 的**后续**事件（进程内广播）。
//
// 用途：SSE 端点在补完历史后用它即时收帧，把首字延迟从「一个轮询周期」压到「事件产生即达」。
// 返回的 cancel 必须被调用（defer），否则订阅表会留下永久条目。
//
// **契约（调用方必须遵守）**：广播是尽力而为的——订阅者太慢时该条会被丢弃（见 hub.publish）。
// 因此收到事件后不能假设「没有空洞」，仍须以 seq 连续性自检，发现跳号就回落到
// ListRunEvents(after) 补齐。hub 负责「快」，补读负责「全」，两者不可互相替代。
func (s *AssistantChatService) SubscribeRunEvents(runID uint) (<-chan model.AssistantEvent, func()) {
	if s == nil || s.hub == nil {
		// 未装配 hub（理论上不会发生，构造时总会建）：返回一个永不发送的通道，
		// 让调用方走轮询路径，而不是拿到 nil 去 select 上永久阻塞。
		ch := make(chan model.AssistantEvent)
		close(ch)
		return ch, func() {}
	}
	return s.hub.subscribe(runID)
}
