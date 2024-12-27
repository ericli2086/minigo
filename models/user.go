package models

import (
	"gorm.io/plugin/soft_delete"
)

// ctags自定义标签说明: q-查询字段, u-更新字段，o-排序字段，用于在列表和更新接口校验参数
type User struct {
	BaseModel
	DeletedAt soft_delete.DeletedAt `json:"-" gorm:"index:i_user_deleted_at;uniqueIndex:u_user_username;uniqueIndex:u_user_email;"`

	Username string `json:"username" gorm:"type:varchar(64);index:i_user_username;uniqueIndex:u_user_username;" ctags:"username,q,u"`

	Email string `json:"email" gorm:"type:varchar(64);index:i_user_email;uniqueIndex:u_user_email;" ctags:"email,q,u"`

	Password string `json:"-" gorm:"type:varchar(256);" ctags:"password,u"`
}
