package logtail

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/tailer"
)

// fileDayPattern 从文件名里抽日期（如 `2026-11-05-1.log`、`2026-11-05.log`）。
//
// # 为什么要从文件名抽
//
// MC 日志行只有 `[HH:MM:SS]`，没有日期。回补历史文件时若用「当前日期」，
// 一个月前的日志会被全标成今天——时间窗、排序、归档分区全错。
// 而轮转文件名里恰好带着日期，那是**唯一可靠**的日期来源。
var fileDayPattern = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`)

// eventTime 把一行的时间归一成完整 RFC3339。
//
// # 三层依据（优先级从高到低）
//
//  1. 行内 `[HH:MM:SS]` + 文件名里的日期（历史文件回补的正确形态）
//  2. 行内 `[HH:MM:SS]` + 同源上一条事件的日期（当前日志文件的常态）
//  3. 都拿不到 ⇒ 采集时刻（宁可时间不准也不要丢掉整行）
//
// # 跨午夜
//
// `latest.log` 会跨午夜：上一条是 23:59、下一条是 00:01。假定日期与上一条相同
// 会把 00:01 放到「昨天」，于是时间序列在那一刻倒退 24 小时。
// 判据：新时刻比上一条早超过 12 小时 ⇒ 视为跨天了。
// （12 小时这个阈值对齐旧 normalize.applyClock 的跨午夜回拨口径，也远大于
// 正常日志的乱序幅度；用 24 小时会漏掉「回拨后又写了 23:59」这种形态。）
func (s *Sink) eventTime(l tailer.Line, logSourceID string) string {
	clock, _ := parseLogLine(string(l.Content))
	now := time.Now().In(s.loc)
	if clock == "" {
		// 无法解析行内时间：用采集时刻。这是有意的降级——宁可时间不准也不要丢行。
		return now.UTC().Format(time.RFC3339Nano)
	}
	hh, mm, ss := parseClock(clock)

	s.lastMu.Lock()
	prev, hasPrev := s.lastEvent[logSourceID]
	s.lastMu.Unlock()

	// 日期基准：文件名的日期优先（历史文件），否则上一条事件的日期，否则今天。
	base := now
	if day, ok := dayFromFileName(l.Path); ok {
		base = day
	} else if hasPrev {
		base = prev.In(s.loc)
	}
	t := time.Date(base.Year(), base.Month(), base.Day(), hh, mm, ss, 0, s.loc)
	if hasPrev && !dayFromFileNameOK(l.Path) {
		// 跨午夜回拨：新时刻比上一条早超过 12 小时 ⇒ 日期加一天。
		if t.Before(prev.In(s.loc).Add(-12 * time.Hour)) {
			t = t.AddDate(0, 0, 1)
		}
	}

	s.lastMu.Lock()
	s.lastEvent[logSourceID] = t
	s.lastMu.Unlock()
	return t.UTC().Format(time.RFC3339Nano)
}

// dayFromFileNameOK 报告文件名里是否带有日期（带日期时不做跨午夜推断）。
func dayFromFileNameOK(path string) bool {
	_, ok := dayFromFileName(path)
	return ok
}

// dayFromFileName 从文件名里抽出日期（本地时区零点）。
func dayFromFileName(path string) (time.Time, bool) {
	base := filepath.Base(path)
	m := fileDayPattern.FindStringSubmatch(base)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", m[1])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseClock 把 `HH:MM:SS` 拆成三个整数；调用方已确保格式合法。
func parseClock(clock string) (hh, mm, ss int) {
	parts := strings.Split(clock, ":")
	if len(parts) != 3 {
		return 0, 0, 0
	}
	hh, _ = strconv.Atoi(parts[0])
	mm, _ = strconv.Atoi(parts[1])
	ss, _ = strconv.Atoi(parts[2])
	return hh, mm, ss
}
