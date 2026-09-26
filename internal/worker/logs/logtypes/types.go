// Package logtypes 映射 FR-473 Shared Contracts 的共享身份与状态类型。
// 字段语义以 docs/specs/worker-log-platform-contract/spec.md 为准，本包不另立契约。
package logtypes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// StreamFields 白名单（契约 §6.5）：低基数路由字段。
// 禁止：event_id、message、_time、任意用户字段。
var StreamFields = []string{
	"worker_id",
	"instance_id",
	"log_source_id",
	"source_generation",
	"level",
	"stream",
}

// Source 列标识日志来源类别。
type Source string

const (
	SourceInstance Source = "instance"
	SourceWorker   Source = "worker"
	SourceNode     Source = "node"
)

// DeliveryState 批次投递状态（契约 §4.1）。
type DeliveryState string

const (
	DeliveryNotSent         DeliveryState = "NOT_SENT"
	DeliveryUnknown         DeliveryState = "UNKNOWN"
	DeliveryRequestDone     DeliveryState = "REQUEST_DONE"
	DeliveryReplayRequired  DeliveryState = "REPLAY_REQUIRED"
)

// Reclaim 与恢复分段责任状态（契约 §4.3）。
type RecoverySegmentState string

const (
	RecoveryStaged                   RecoverySegmentState = "STAGED"
	RecoveryDurableVerified          RecoverySegmentState = "DURABLE_VERIFIED"
	RecoveryWALResponsibilityXfer    RecoverySegmentState = "WAL_RESPONSIBILITY_TRANSFERRED"
	RecoveryReleased                 RecoverySegmentState = "RELEASED"
	RecoveryCleaned                  RecoverySegmentState = "CLEANED"
)

// ReleaseReason 恢复分段释放证明；进入 RELEASED 前必须登记其一。
type ReleaseReason string

const (
	ReleaseProjectionBacked      ReleaseReason = "PROJECTION_BACKED"
	ReleaseNextCopyVerified      ReleaseReason = "NEXT_COPY_VERIFIED"
	ReleaseRetentionExpiredNoHold ReleaseReason = "RETENTION_EXPIRED_WITHOUT_HOLDS"
)

// Positions 四水位；位置与投递状态分离保存。
type Positions struct {
	Read      uint64 `json:"read_position"`
	Durable   uint64 `json:"durable_position"`
	Delivery  uint64 `json:"delivery_position"`
	Reclaim   uint64 `json:"reclaim_position"`
}

// SourceIdentity 逻辑日志源分段身份。
type SourceIdentity struct {
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
	ParserVersion    string `json:"parser_version"`
}

// RecordRange 事件在源分段中的字节/行位置范围（契约使用 record_start/end）。
type RecordRange struct {
	Start uint64 `json:"record_start"`
	End   uint64 `json:"record_end"`
}

// Event 标准规范事件（查询与账本共用身份字段）。
type Event struct {
	EventID            string            `json:"event_id"`
	Source             SourceIdentity    `json:"source"`
	Record             RecordRange       `json:"record"`
	EventTimeUTC       string            `json:"event_time_utc"`
	IngestTimeUTC      string            `json:"ingest_time_utc"`
	Level              string            `json:"level"`
	Stream             string            `json:"stream"`
	Message            string            `json:"message"`
	CanonicalHash      string            `json:"canonical_content_hash"`
	Fields             map[string]string `json:"fields,omitempty"`
}

// DeliveryRecord 单事件投递/核验状态。
type DeliveryRecord struct {
	EventID           string         `json:"event_id"`
	DeliveryState     DeliveryState  `json:"delivery_state"`
	ValidationState   string         `json:"validation_state"`
	VerificationState string         `json:"verification_state"`
}

// EventID 按契约：hash(log_source_id, source_generation, record_start, record_end, parser_version)。
func EventID(src SourceIdentity, rec RecordRange) string {
	payload := fmt.Sprintf("%s|%s|%d|%d|%s",
		src.LogSourceID, src.SourceGeneration, rec.Start, rec.End, src.ParserVersion)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// CanonicalContentHash 规范内容哈希：用于重复对账，不作为 VL 唯一键。
func CanonicalContentHash(eventTimeUTC, level, stream, message string) string {
	payload := strings.Join([]string{eventTimeUTC, level, stream, message}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// BuildEvent 组装规范事件并计算 event_id 与 canonical hash。
func BuildEvent(src SourceIdentity, rec RecordRange, eventTimeUTC, ingestTimeUTC, level, stream, message string) Event {
	ev := Event{
		Source:        src,
		Record:        rec,
		EventTimeUTC:  eventTimeUTC,
		IngestTimeUTC: ingestTimeUTC,
		Level:         level,
		Stream:        stream,
		Message:       message,
	}
	ev.EventID = EventID(src, rec)
	ev.CanonicalHash = CanonicalContentHash(eventTimeUTC, level, stream, message)
	return ev
}

// ValidReleaseReason 报告 reason 是否为契约 §4.3 登记的释放证明。
func ValidReleaseReason(r ReleaseReason) bool {
	switch r {
	case ReleaseProjectionBacked, ReleaseNextCopyVerified, ReleaseRetentionExpiredNoHold:
		return true
	default:
		return false
	}
}

// CanReclaim 评估 reclaim_position 是否可推进（契约 §4.3）。
//
// 回收前置：WAL 前缀已有可靠恢复责任（恢复分段到 WAL_RESPONSIBILITY_TRANSFERRED
// 或其后），且无 hold。HTTP 2xx / REQUEST_DONE 单独不得回收。
// release_reason 仅在转入 RELEASED 时登记，不作为 reclaim 的前置。
func CanReclaim(pos Positions, seg RecoverySegmentState, reason ReleaseReason, hasHold bool) bool {
	if hasHold {
		return false
	}
	switch seg {
	case RecoveryWALResponsibilityXfer, RecoveryReleased, RecoveryCleaned:
		// 责任已转移/已释放/已清理：允许推进 reclaim（与 reason 无关）。
		return true
	default:
		// STAGED / DURABLE_VERIFIED：责任尚未转移，禁止 reclaim。
		_ = reason
		return false
	}
}

// StreamAllowed 报告字段是否允许进入 stream identity。
func StreamAllowed(field string) bool {
	for _, f := range StreamFields {
		if f == field {
			return true
		}
	}
	return false
}
