package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadRunIntent 是 handler 下发的运行意图。
type BotLoadRunIntent string

const (
	BotLoadIntentPreflight BotLoadRunIntent = "preflight"
	BotLoadIntentReady     BotLoadRunIntent = "ready"
	BotLoadIntentStart     BotLoadRunIntent = "start"
	BotLoadIntentRunning   BotLoadRunIntent = "running"
	BotLoadIntentDegrade   BotLoadRunIntent = "degrade"
	BotLoadIntentRecover   BotLoadRunIntent = "recover"
	BotLoadIntentStop      BotLoadRunIntent = "stop"
	BotLoadIntentCancel    BotLoadRunIntent = "cancel"
	BotLoadIntentComplete  BotLoadRunIntent = "complete"
	BotLoadIntentFail      BotLoadRunIntent = "fail"
	BotLoadIntentCancelled BotLoadRunIntent = "cancelled"
)

// MapRunStateToLegacyStatus 将 V2 run_state 映射到 V1 status 列（同事务写入）。
func MapRunStateToLegacyStatus(state model.BotLoadRunState) model.BotStressSessionStatus {
	switch state {
	case model.BotLoadRunPending, model.BotLoadRunPreflighting, model.BotLoadRunReady, model.BotLoadRunStarting:
		return model.BotStressSessionPending
	case model.BotLoadRunRunning, model.BotLoadRunDegraded, model.BotLoadRunStopping, model.BotLoadRunCancelling:
		return model.BotStressSessionRunning
	case model.BotLoadRunCompleted, model.BotLoadRunCancelled:
		return model.BotStressSessionStopped
	case model.BotLoadRunFailed:
		return model.BotStressSessionError
	default:
		return model.BotStressSessionPending
	}
}

// IsTerminalRunState 判断是否为终态。
func IsTerminalRunState(state model.BotLoadRunState) bool {
	switch state {
	case model.BotLoadRunCompleted, model.BotLoadRunFailed, model.BotLoadRunCancelled:
		return true
	default:
		return false
	}
}

// TransitionRunState 计算合法状态转换；非法时返回 error。
// 幂等：stopping 上 stop、cancelling 上 cancel 返回当前状态且 unchanged=true。
func TransitionRunState(from model.BotLoadRunState, intent BotLoadRunIntent) (to model.BotLoadRunState, unchanged bool, err error) {
	if IsTerminalRunState(from) {
		switch intent {
		case BotLoadIntentStop, BotLoadIntentCancel:
			return from, false, fmt.Errorf("%w: 终态 %s 不允许 %s", ErrBotLoadInvalidState, from, intent)
		default:
			return from, false, fmt.Errorf("%w: 终态 %s 不允许 %s", ErrBotLoadInvalidState, from, intent)
		}
	}

	switch intent {
	case BotLoadIntentPreflight:
		if from == model.BotLoadRunPending {
			return model.BotLoadRunPreflighting, false, nil
		}
	case BotLoadIntentReady:
		if from == model.BotLoadRunPreflighting || from == model.BotLoadRunPending {
			return model.BotLoadRunReady, false, nil
		}
		// ready → ready：重新预检。
		//
		// 预检每次都会重新规划并落库新 token，token 有有效期，过期后就靠「再次预检」重取。
		// 原状态机不接受 ready→ready，于是第二次预检在**新 plan 已落库之后**报状态冲突，
		// 客户端拿不到 token，只能另建一个运行——从事务上看，它做的事都一样，只是没重取。
		// 这里判为幂等：状态不变，也不当作「发生转移」。
		if from == model.BotLoadRunReady {
			return model.BotLoadRunReady, true, nil
		}
	case BotLoadIntentStart:
		if from == model.BotLoadRunReady {
			return model.BotLoadRunStarting, false, nil
		}
	case BotLoadIntentRunning:
		if from == model.BotLoadRunStarting || from == model.BotLoadRunDegraded {
			return model.BotLoadRunRunning, false, nil
		}
	case BotLoadIntentDegrade:
		if from == model.BotLoadRunRunning {
			return model.BotLoadRunDegraded, false, nil
		}
	case BotLoadIntentRecover:
		if from == model.BotLoadRunDegraded {
			return model.BotLoadRunRunning, false, nil
		}
	case BotLoadIntentStop:
		switch from {
		case model.BotLoadRunStarting, model.BotLoadRunRunning, model.BotLoadRunDegraded:
			return model.BotLoadRunStopping, false, nil
		case model.BotLoadRunStopping:
			return model.BotLoadRunStopping, true, nil
		}
	case BotLoadIntentCancel:
		switch from {
		case model.BotLoadRunPending, model.BotLoadRunPreflighting, model.BotLoadRunReady:
			return model.BotLoadRunCancelling, false, nil
		case model.BotLoadRunStarting, model.BotLoadRunRunning, model.BotLoadRunDegraded, model.BotLoadRunStopping:
			return model.BotLoadRunCancelling, false, nil
		case model.BotLoadRunCancelling:
			return model.BotLoadRunCancelling, true, nil
		}
	case BotLoadIntentComplete:
		if from == model.BotLoadRunStopping {
			return model.BotLoadRunCompleted, false, nil
		}
	case BotLoadIntentCancelled:
		if from == model.BotLoadRunCancelling {
			return model.BotLoadRunCancelled, false, nil
		}
	case BotLoadIntentFail:
		// 任一非终态可进入 failed。
		return model.BotLoadRunFailed, false, nil
	default:
		return from, false, fmt.Errorf("%w: 未知意图 %s", ErrBotLoadInvalidState, intent)
	}
	return from, false, fmt.Errorf("%w: 状态 %s 不允许意图 %s", ErrBotLoadInvalidState, from, intent)
}
