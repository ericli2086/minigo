package middlewares

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// TransactionMiddleware 自动事务中间件
func TransactionMiddleware(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 开启事务
		tx := db.Begin()

		// 将事务设置到上下文中
		c.Set("tx", tx)

		// 捕获 panic，回滚事务
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
				panic(r) // 继续抛出 panic
			}
		}()

		// 执行下一个中间件或处理程序
		c.Next()

		// 根据响应状态提交或回滚事务
		if len(c.Errors) > 0 {
			tx.Rollback()
		} else {
			if err := tx.Commit().Error; err != nil {
				tx.Rollback()
			}
		}
	}
}
