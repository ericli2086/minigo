package main

import (
	"log"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	"minigo/controllers"
	"minigo/middlewares"
	"minigo/models"
	"minigo/utils"
)

func main() {
	dsn := "test.db"

	// 连接数据库
	dbutil := utils.InitDB(dsn)

	// 设置路由
	r := gin.Default()

	// 注册事务中间件
	r.Use(middlewares.TransactionMiddleware(dbutil.DB))

	for _, model := range []interface{}{models.User{}} {
		// 迁移数据库
		modelType, modelPtr, tableName := utils.GetModelInfo(model)
		err := dbutil.DB.AutoMigrate(modelPtr)
		if err != nil {
			log.Fatalf("Failed to migrate database: %v", err)
		}

		// 创建计数器
		dbutil.CreateCounter4Table(tableName)

		// 注册路由
		controllers.RegisterGenericRoutes(r, strings.TrimSuffix(tableName, "s"), reflect.Zero(modelType).Interface())
	}

	log.Println("Server starting on :8080")
	r.Run(":8080")
}
