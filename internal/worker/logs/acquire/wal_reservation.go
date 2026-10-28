package acquire

import "fmt"

// 预留跟随条目而非投递请求，避免排空结束就虚报 WAL 已释放。
type walByteReservation struct {
	release func()
}

func (r *walByteReservation) close() {
	if r == nil || r.release == nil {
		return
	}
	r.release()
	r.release = nil
}

// SetByteReservation 为既存条目也取得真实长期配额；失败保留原预算，不把现存占用当作零。
// 回调只应访问独立资源预算，不能重入同一 WAL。释放回调必须幂等或只依赖本方法的一次释放保证。
func (w *WAL) SetByteReservation(reserve func(int64) (func(), error)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	leases, err := reserveWALEntries(w.entries, reserve)
	if err != nil {
		return err
	}
	for i := range w.entries {
		w.entries[i].reservation.close()
		w.entries[i].reservation = leases[i]
	}
	w.reserveBytes, w.reservationErr = reserve, nil
	return nil
}

func reserveWALEntries(entries []WALEntry, reserve func(int64) (func(), error)) ([]*walByteReservation, error) {
	leases := make([]*walByteReservation, len(entries))
	if reserve == nil {
		return leases, nil
	}
	for i, entry := range entries {
		release, err := reserve(walEntryBytes(entry))
		if err != nil {
			for _, lease := range leases {
				lease.close()
			}
			return nil, fmt.Errorf("acquire: reserve WAL bytes before append: %w", err)
		}
		leases[i] = &walByteReservation{release: release}
	}
	return leases, nil
}

// ReservationError 返回最近一次取预算失败的原因，阻止未取得预算的条目被继续提交或投递。
func (w *WAL) ReservationError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reservationErr
}
