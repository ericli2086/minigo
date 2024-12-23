package models

import (
	"gorm.io/plugin/soft_delete"
)

type User struct {
	BaseModel
	DeletedAt soft_delete.DeletedAt `json:"-" gorm:"index:i_user_deleted_at;uniqueIndex:u_user_username;uniqueIndex:u_user_email;"`

	Username string `json:"username" gorm:"type:varchar(64);index:i_user_username;uniqueIndex:u_user_username;" allowUpdate:"username"`

	Email string `json:"email" gorm:"type:varchar(64);index:i_user_email;uniqueIndex:u_user_email;" allowUpdate:"email"`

	Password string `json:"-" gorm:"type:varchar(256);" allowUpdate:"password"`
}
