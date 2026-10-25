package stateindex

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
)

const materializationDDL = `CREATE TABLE IF NOT EXISTS materialization_attempt (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_key TEXT NOT NULL,
  utc_day TEXT NOT NULL,
  window_key TEXT NOT NULL,
  generation TEXT NOT NULL,
  state TEXT NOT NULL,
  class TEXT NOT NULL DEFAULT 'rebuild',
  reserved_bytes INTEGER NOT NULL,
  written_bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  verified INTEGER NOT NULL DEFAULT 0,
  published INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  UNIQUE(source_key,utc_day,window_key,generation)
);
CREATE INDEX IF NOT EXISTS idx_materialization_hour ON materialization_attempt(source_key,utc_day,created_at);
CREATE INDEX IF NOT EXISTS idx_materialization_window ON materialization_attempt(source_key,utc_day,window_key,id);`

// MaterializationRequest 绑定源、物理日与稳定内容窗口，候选代次仅标记本次副本身份。
type MaterializationRequest struct {
	SourceKey, UTCDay, WindowKey, Generation string
	Class                                    string
	EstimatedBytes                           int64
	Now                                      time.Time
}

type MaterializationLimits struct {
	WindowCopies, HourlyCopies int
	HourlyBytes                int64
	CompactionReserveBytes     int64
}

type MaterializationReservation struct {
	ID                                              int64
	SourceKey, UTCDay, WindowKey, Generation, State string
	Class                                           string
	ReservedBytes, WrittenBytes                     int64
	CreatedAt                                       time.Time
	Verified, Published                             bool
	Detail                                          string
}

// WrittenBytes 是已观察的累计写字节数而不是增量；未知写入至少按预留计费。
type MaterializationUpdate struct {
	State, Generation, Detail string
	WrittenBytes              int64
	Verified, Published       bool
}

const materializationColumns = "id,source_key,utc_day,window_key,generation,state,reserved_bytes,written_bytes,created_at,verified,published,detail,class"

func scanMaterialization(row interface{ Scan(...any) error }) (MaterializationReservation, error) {
	var out MaterializationReservation
	var created int64
	err := row.Scan(&out.ID, &out.SourceKey, &out.UTCDay, &out.WindowKey, &out.Generation, &out.State, &out.ReservedBytes, &out.WrittenBytes, &created, &out.Verified, &out.Published, &out.Detail, &out.Class)
	out.CreatedAt = time.Unix(0, created).UTC()
	return out, err
}

// ReserveMaterialization 在任何外部写入前提交预留，次数与字节不因超时、失败或发布冲突退款。
func (s *Store) ReserveMaterialization(ctx context.Context, request MaterializationRequest, limits MaterializationLimits) (MaterializationReservation, bool, time.Time, error) {
	var reservation MaterializationReservation
	var permitted bool
	var retryAt time.Time
	if request.Class == "" {
		request.Class = "rebuild"
	}
	if request.Class != "rebuild" && request.Class != "live_compaction" {
		return reservation, false, retryAt, errors.New("stateindex: 物化类别无效")
	}
	if limits.CompactionReserveBytes < 0 || limits.CompactionReserveBytes > limits.HourlyBytes {
		return reservation, false, retryAt, errors.New("stateindex: 必要压缩预留超出总物化预算")
	}
	day, err := time.Parse("2006-01-02", request.UTCDay)
	if err != nil || day.Format("2006-01-02") != request.UTCDay || request.SourceKey == "" || request.WindowKey == "" || request.Generation == "" || request.EstimatedBytes < 1 || limits.WindowCopies < 1 || limits.HourlyCopies < 1 || limits.HourlyBytes < 1 {
		return reservation, false, retryAt, errors.New("stateindex: 物化窗口或预算无效")
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly || s.migrationPending {
		return reservation, false, retryAt, errors.New("stateindex: 当前索引不接受物化预留")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return reservation, false, retryAt, err
	}
	defer func() { _ = conn.Close() }()
	err = s.runUnit(ctx, conn, func() error {
		// 同一候选不得再次得到写许可；调用方只能只读取证或使用原先的持久预留。
		existing, err := scanMaterialization(conn.QueryRowContext(ctx, "SELECT "+materializationColumns+" FROM materialization_attempt WHERE source_key=? AND utc_day=? AND window_key=? AND generation=?", request.SourceKey, request.UTCDay, request.WindowKey, request.Generation))
		if err == nil {
			reservation = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var copies, hourlyCopies int64
		var hourlyBytes, rebuildBytes int64
		var oldest sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM materialization_attempt WHERE source_key=? AND utc_day=? AND window_key=? AND class=?", request.SourceKey, request.UTCDay, request.WindowKey, request.Class).Scan(&copies); err != nil {
			return err
		}
		if copies >= int64(limits.WindowCopies) {
			return nil
		}
		cutoff := request.Now.Add(-time.Hour).UnixNano()
		if err := conn.QueryRowContext(ctx, "SELECT coalesce(sum(CASE WHEN class=? THEN 1 ELSE 0 END),0),coalesce(sum(max(reserved_bytes,written_bytes)),0),coalesce(sum(CASE WHEN class='rebuild' THEN max(reserved_bytes,written_bytes) ELSE 0 END),0),min(created_at) FROM materialization_attempt WHERE source_key=? AND utc_day=? AND created_at>?", request.Class, request.SourceKey, request.UTCDay, cutoff).Scan(&hourlyCopies, &hourlyBytes, &rebuildBytes, &oldest); err != nil {
			return err
		}
		// 后台重建只能使用扣除压缩保留后的类别额度；两类仍共用同一源日总字节硬顶。
		rebuildLimit := limits.HourlyBytes - limits.CompactionReserveBytes
		rebuildBlocked := request.Class == "rebuild" && (request.EstimatedBytes > rebuildLimit || rebuildBytes > rebuildLimit-request.EstimatedBytes)
		if hourlyCopies >= int64(limits.HourlyCopies) || request.EstimatedBytes > limits.HourlyBytes || hourlyBytes > limits.HourlyBytes-request.EstimatedBytes || rebuildBlocked {
			if oldest.Valid {
				retryAt = time.Unix(0, oldest.Int64).Add(time.Hour).UTC()
			}
			return nil
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO materialization_attempt(source_key,utc_day,window_key,generation,state,class,reserved_bytes,created_at) VALUES(?,?,?,?,'reserved',?,?,?)`, request.SourceKey, request.UTCDay, request.WindowKey, request.Generation, request.Class, request.EstimatedBytes, request.Now.UnixNano())
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		reservation = MaterializationReservation{ID: id, SourceKey: request.SourceKey, UTCDay: request.UTCDay, WindowKey: request.WindowKey, Generation: request.Generation, State: "reserved", Class: request.Class, ReservedBytes: request.EstimatedBytes, CreatedAt: request.Now.UTC()}
		permitted = true
		return nil
	})
	if err != nil {
		return MaterializationReservation{}, false, time.Time{}, err
	}
	return reservation, permitted, retryAt, nil
}

func materializationStateRank(state string) int {
	switch state {
	case "reserved":
		return 1
	case "unknown":
		return 2
	case "written":
		return 3
	case "verified":
		return 4
	case "published":
		return 5
	case "failed":
		return 6
	default:
		return 0
	}
}

func (s *Store) UpdateMaterialization(ctx context.Context, id int64, update MaterializationUpdate) error {
	if id < 1 || materializationStateRank(update.State) == 0 || update.WrittenBytes < 0 || update.WrittenBytes > math.MaxInt64/2 || len(update.Detail) > 4096 {
		return errors.New("stateindex: 物化回执无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly || s.migrationPending {
		return errors.New("stateindex: 当前索引不接受物化回执")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return s.runUnit(ctx, conn, func() error {
		stored, err := scanMaterialization(conn.QueryRowContext(ctx, "SELECT "+materializationColumns+" FROM materialization_attempt WHERE id=?", id))
		if err != nil {
			return err
		}
		if update.Generation != "" && update.Generation != stored.Generation {
			return errors.New("stateindex: 物化候选代次不可变更")
		}
		if materializationStateRank(update.State) < materializationStateRank(stored.State) {
			update.State = stored.State
		}
		verified := stored.Verified || update.Verified || update.State == "verified"
		published := stored.Published || update.Published || update.State == "published"
		if published && !verified {
			return errors.New("stateindex: 未验证候选不得记为已发布")
		}
		if update.WrittenBytes < stored.WrittenBytes {
			update.WrittenBytes = stored.WrittenBytes
		}
		_, err = conn.ExecContext(ctx, "UPDATE materialization_attempt SET state=?,written_bytes=?,verified=?,published=?,detail=? WHERE id=?", update.State, update.WrittenBytes, verified, published, strings.TrimSpace(update.Detail), id)
		return err
	})
}

// ListMaterializations 对固定源、日和内容窗口按递增编号分页，可查未发布候选而不加载历史到活动状态。
func (s *Store) ListMaterializations(ctx context.Context, source, day, window string, afterID int64, limit int) ([]MaterializationReservation, error) {
	if source == "" || day == "" || afterID < 0 || limit < 1 || limit > 200 {
		return nil, errors.New("stateindex: 物化分页参数无效")
	}
	query := "SELECT " + materializationColumns + " FROM materialization_attempt WHERE source_key=? AND utc_day=? AND id>?"
	args := []any{source, day, afterID}
	if window != "" {
		query += " AND window_key=?"
		args = append(args, window)
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]MaterializationReservation, 0, limit)
	for rows.Next() {
		row, err := scanMaterialization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
