package model

import "time"

// NodeJDK 节点托管或登记的 JDK。
type NodeJDK struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	NodeID        uint      `gorm:"not null;index" json:"nodeId"`
	Vendor        string    `gorm:"type:varchar(64);not null" json:"vendor"`
	MajorVersion  int       `gorm:"not null;index" json:"majorVersion"`
	Version       string    `gorm:"type:varchar(64);not null" json:"version"`
	Arch          string    `gorm:"type:varchar(32);not null" json:"arch"`
	Path          string    `gorm:"type:varchar(512);not null" json:"path"`
	Managed       bool      `gorm:"default:false" json:"managed"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
