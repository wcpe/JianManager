package logcoord

import (
	"encoding/json"
	"fmt"
)

// PageCursor binds federation pagination to one immutable CP Query View and
// the last globally emitted composite sort key.
type PageCursor struct {
	ViewID       string  `json:"view_id"`
	OrderVersion string  `json:"order_version"`
	SortKey      SortKey `json:"sort_key"`
	PageLimit    int     `json:"page_limit"`
}

func EncodePageCursor(cursor PageCursor) string {
	if cursor.OrderVersion == "" {
		cursor.OrderVersion = OrderVersion
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return string(data)
}

func DecodePageCursor(raw string) (PageCursor, error) {
	if raw == "" {
		return PageCursor{}, fmt.Errorf("logcoord: empty cursor")
	}
	var cursor PageCursor
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
		return PageCursor{}, fmt.Errorf("logcoord: decode cursor: %w", err)
	}
	if cursor.ViewID == "" || cursor.OrderVersion != OrderVersion || cursor.PageLimit <= 0 {
		return PageCursor{}, fmt.Errorf("logcoord: invalid cursor binding")
	}
	return cursor, nil
}
