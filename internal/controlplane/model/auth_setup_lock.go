package model

import "time"

// AuthSetupLock 是首次管理员初始化的数据库互斥锁。
// 单行主键约束保证并发请求或多进程部署中只有一个请求能完成初始化。
type AuthSetupLock struct {
	Key       string    `gorm:"type:varchar(64);primaryKey"`
	CreatedAt time.Time `json:"not null"`
}
