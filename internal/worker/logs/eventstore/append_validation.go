package eventstore

import (
	"encoding/json"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// validateAppendEvents 在任何目录、段或工作清单变更前检查整批，但不保留整批编码结果。
// 编码前先用原文和字段结构的最低字节数挡住巨型输入，使单条编码分配受行限与最多六倍转义约束。
func validateAppendEvents(events []logtypes.Event) error {
	for _, event := range events {
		remaining := maxEventLineBytes - 1
		for _, value := range []string{
			event.EventID, event.Source.LogSourceID, event.Source.SourceGeneration, event.Source.ParserVersion,
			event.EventTimeUTC, event.IngestTimeUTC, event.Level, event.Stream, event.Message, event.CanonicalHash,
		} {
			if len(value) > remaining {
				return appendLineLimitError()
			}
			remaining -= len(value)
		}
		// 每个字符串字段至少需要双引号、冒号及分隔符，避免空字段大映射绕过预编码字节界。
		if len(event.Fields) > remaining/6 {
			return appendLineLimitError()
		}
		remaining -= len(event.Fields) * 6
		for key, value := range event.Fields {
			if len(key) > remaining {
				return appendLineLimitError()
			}
			remaining -= len(key)
			if len(value) > remaining {
				return appendLineLimitError()
			}
			remaining -= len(value)
		}
		data, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("eventstore: marshal event: %w", err)
		}
		if len(data)+1 > maxEventLineBytes {
			return appendLineLimitError()
		}
	}
	return nil
}

func appendLineLimitError() error {
	return fmt.Errorf("eventstore: event exceeds %d-byte line limit", maxEventLineBytes)
}
