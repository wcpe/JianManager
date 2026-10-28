package stateindex

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// 恢复历史独立持久化，不进入活动状态的全量加载、差异计划或镜像。
const recoveryAuditDDL = `CREATE TABLE IF NOT EXISTS recovery_audit (
  source_key TEXT NOT NULL,
  ref_key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  payload BLOB NOT NULL,
  payload_sha TEXT NOT NULL,
  archive_id TEXT NOT NULL,
  PRIMARY KEY(source_key, ref_key)
) WITHOUT ROWID;`

// RecoveryAuditRow 的正文由领域适配层编码，存储层只校验身份、完整性与幂等性。
type RecoveryAuditRow struct {
	SourceKey   string
	RefKey      string
	Fingerprint string
	Payload     []byte
	ArchiveID   string
}

// AppendRecoveryAudit 仅在本批审计真实提交后返回回执；同一身份不同正文不得覆盖旧证据。
func (s *Store) AppendRecoveryAudit(ctx context.Context, records []RecoveryAuditRow) ([]RecoveryAuditRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly {
		return nil, errors.New("stateindex: 只读库不接受恢复审计写入")
	}
	if len(records) > s.budget.MaxRows || len(records) > s.budget.CycleMaxRows {
		return nil, errors.New("stateindex: 恢复审计批次超过提交预算")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	out := make([]RecoveryAuditRow, 0, len(records))
	err = s.runUnit(ctx, conn, func() error {
		for _, record := range records {
			if record.SourceKey == "" || record.RefKey == "" || record.Fingerprint == "" || len(record.Payload) == 0 || len(record.Payload) > 512<<10 {
				return errors.New("stateindex: 恢复审计记录无效或超出正文上限")
			}
			payloadSHA := sha256.Sum256(record.Payload)
			identitySHA := sha256.Sum256([]byte(mirrorKey(record.SourceKey, record.RefKey)))
			record.ArchiveID = "recovery:" + hex.EncodeToString(identitySHA[:])
			_, err := conn.ExecContext(ctx, `INSERT INTO recovery_audit(source_key,ref_key,fingerprint,payload,payload_sha,archive_id) VALUES(?,?,?,?,?,?) ON CONFLICT(source_key,ref_key) DO NOTHING`, record.SourceKey, record.RefKey, record.Fingerprint, record.Payload, hex.EncodeToString(payloadSHA[:]), record.ArchiveID)
			if err != nil {
				return err
			}
			existing, found, err := lookupRecoveryAudit(ctx, conn, record.SourceKey, record.RefKey)
			if err != nil {
				return err
			}
			if !found || existing.Fingerprint != record.Fingerprint || string(existing.Payload) != string(record.Payload) || existing.ArchiveID != record.ArchiveID {
				return errors.New("stateindex: 恢复审计身份冲突，保留原有证据")
			}
			out = append(out, existing)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func lookupRecoveryAudit(ctx context.Context, reader rowReader, source, ref string) (RecoveryAuditRow, bool, error) {
	var row RecoveryAuditRow
	var checksum string
	err := reader.QueryRowContext(ctx, "SELECT source_key,ref_key,fingerprint,payload,payload_sha,archive_id FROM recovery_audit WHERE source_key=? AND ref_key=?", source, ref).Scan(&row.SourceKey, &row.RefKey, &row.Fingerprint, &row.Payload, &checksum, &row.ArchiveID)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	actual := sha256.Sum256(row.Payload)
	if checksum != hex.EncodeToString(actual[:]) {
		return row, false, fmt.Errorf("stateindex: 恢复审计正文校验失败：%s/%s", source, ref)
	}
	return row, true, nil
}

func (s *Store) LookupRecoveryAudit(ctx context.Context, source, ref string) (RecoveryAuditRow, bool, error) {
	return lookupRecoveryAudit(ctx, s.db, source, ref)
}

// ListRecoveryAudit 使用稳定复合主键游标，单页最多二百条，不把历史重新装入活动账本。
func (s *Store) ListRecoveryAudit(ctx context.Context, source, afterRef string, limit int) ([]RecoveryAuditRow, error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("stateindex: 恢复审计分页上限须为1至200")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT source_key,ref_key,fingerprint,payload,payload_sha,archive_id FROM recovery_audit WHERE source_key=? AND ref_key>? ORDER BY ref_key LIMIT ?", source, afterRef, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]RecoveryAuditRow, 0, limit)
	for rows.Next() {
		var row RecoveryAuditRow
		var checksum string
		if err := rows.Scan(&row.SourceKey, &row.RefKey, &row.Fingerprint, &row.Payload, &checksum, &row.ArchiveID); err != nil {
			return nil, err
		}
		actual := sha256.Sum256(row.Payload)
		if checksum != hex.EncodeToString(actual[:]) {
			return nil, errors.New("stateindex: 恢复审计正文校验失败")
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
