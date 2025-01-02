package models

// BaseModel 包含通用的时间戳字段
type BaseModel struct {
	ID        uint  `json:"id" gorm:"primarykey"`
	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime:milli"` // 使用毫秒级时间戳
	UpdatedAt int64 `json:"updated_at" gorm:"autoUpdateTime:milli"` // 使用毫秒级时间戳
}
