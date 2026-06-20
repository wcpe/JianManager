package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// archiveBatch 单轮归档从表中取出并落盘的批大小，避免一次性加载过多行进内存。
const archiveBatch = 1000

// runArchiveLoop 周期性执行归档与保留：先按保留天数滚动旧档，再按总量上限回收。
func (s *LogService) runArchiveLoop(interval time.Duration) {
	defer s.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动后先跑一轮，避免重启后久未清理。
	s.runRetentionOnce()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.runRetentionOnce()
		}
	}
}

// runRetentionOnce 执行一轮完整的归档+保留。错误仅记录不中断（下一轮重试）。
func (s *LogService) runRetentionOnce() {
	if n, err := s.archiveBeforeRetention(); err != nil {
		slog.Error("按保留天数归档日志失败", "err", err)
	} else if n > 0 {
		slog.Info("按保留天数归档日志", "count", n)
	}
	if n, err := s.archiveOverCapacity(); err != nil {
		slog.Error("按总量上限归档日志失败", "err", err)
	} else if n > 0 {
		slog.Info("按总量上限归档日志", "count", n)
	}
}

// archiveBeforeRetention 把早于「保留天数」的日志滚动落盘并从表中删除，返回归档条数。
// RetentionDays<=0 时跳过（不按时间清理）。
func (s *LogService) archiveBeforeRetention() (int, error) {
	if s.cfg.RetentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.cfg.RetentionDays)
	total := 0
	for {
		var batch []model.LogEntry
		if err := s.db.Where("time < ?", cutoff).
			Order("time ASC").Order("id ASC").
			Limit(archiveBatch).Find(&batch).Error; err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}
		if err := s.flushArchiveAndDelete(batch); err != nil {
			return total, err
		}
		total += len(batch)
		if len(batch) < archiveBatch {
			break
		}
	}
	return total, nil
}

// archiveOverCapacity 当表内日志条数超过总量上限折算的行数时，从最旧开始滚动落盘并删除，
// 直到回落到阈值内。MaxTotalMB<=0 时跳过（不按总量清理）。
func (s *LogService) archiveOverCapacity() (int, error) {
	if s.cfg.MaxTotalMB <= 0 {
		return 0, nil
	}
	maxRows := int64(s.cfg.MaxTotalMB) * 1024 * 1024 / singleEntryBytes
	if maxRows <= 0 {
		return 0, nil
	}

	var count int64
	if err := s.db.Model(&model.LogEntry{}).Count(&count).Error; err != nil {
		return 0, err
	}
	if count <= maxRows {
		return 0, nil
	}

	toRemove := count - maxRows
	total := 0
	for toRemove > 0 {
		limit := archiveBatch
		if int64(limit) > toRemove {
			limit = int(toRemove)
		}
		var batch []model.LogEntry
		if err := s.db.Order("time ASC").Order("id ASC").
			Limit(limit).Find(&batch).Error; err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}
		if err := s.flushArchiveAndDelete(batch); err != nil {
			return total, err
		}
		total += len(batch)
		toRemove -= int64(len(batch))
	}
	return total, nil
}

// flushArchiveAndDelete 把一批日志按 NDJSON 追加到当日归档文件，落盘成功后从表中删除其 ID。
// 落盘失败则不删除（宁可表内冗余，不丢日志）；root 为 nil 时跳过落盘直接删除（仅做保留）。
func (s *LogService) flushArchiveAndDelete(batch []model.LogEntry) error {
	if len(batch) == 0 {
		return nil
	}
	if s.root != nil {
		if err := s.appendArchive(batch); err != nil {
			return err
		}
	}
	ids := make([]uint, len(batch))
	for i, e := range batch {
		ids[i] = e.ID
	}
	if err := s.db.Where("id IN ?", ids).Delete(&model.LogEntry{}).Error; err != nil {
		return fmt.Errorf("删除已归档日志失败: %w", err)
	}
	return nil
}

// appendArchive 把一批日志以 NDJSON（每行一条 JSON）追加到数据根 var/log/logs-YYYY-MM-DD.ndjson。
// 按批次首条的日期分文件，便于按天清理旧档（FR-049：旧档可清理）。
func (s *LogService) appendArchive(batch []model.LogEntry) error {
	day := batch[0].Time.Format("2006-01-02")
	name := fmt.Sprintf("logs-%s.ndjson", day)
	path := filepath.Join(s.root.LogDir(), name)

	if err := os.MkdirAll(s.root.LogDir(), 0o755); err != nil {
		return fmt.Errorf("创建归档目录失败: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开归档文件失败: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := range batch {
		if err := enc.Encode(&batch[i]); err != nil {
			return fmt.Errorf("写归档记录失败: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("刷新归档文件失败: %w", err)
	}
	return nil
}

// PurgeArchivesOlderThan 删除数据根 var/log 下早于保留天数的归档文件（旧档清理）。
// 由保留巡检在归档之外可选调用；此处独立导出，便于测试与未来手动触发。
func (s *LogService) PurgeArchivesOlderThan(days int) (int, error) {
	if s.root == nil || days <= 0 {
		return 0, nil
	}
	dir := s.root.LogDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取归档目录失败: %w", err)
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}
