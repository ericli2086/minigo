package main

import (
	"log"
	"reflect"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"minigo/controllers"
	"minigo/middlewares"
	"minigo/models"
	"minigo/utils"
)

func main() {
	logger := utils.GetLogger()
	db := utils.GetDB("test.db").SetLogger(logger)

	for i := 0; i < 15; i++ {
		logger.Info("Info message")
		logger.Warn("Warn message")
		logger.Error("Error message")
		logger.Debug("Debug message")
		logger.WithTraceID("trace-abc123").Info("Info message")
		logger.WithTraceID("trace-abc234").Info("创建用户", zap.String("username", "test"))
	}
	// logger.Fatal("Fatal message")

	// 设置路由
	r := gin.Default()

	// 注册事务中间件
	r.Use(middlewares.TransactionMiddleware(db.DB))

	for _, model := range []interface{}{models.User{}} {
		// 迁移数据库
		modelType, modelPtr, tableName := utils.GetModelInfo(model)
		err := db.DB.AutoMigrate(modelPtr)
		if err != nil {
			log.Fatalf("failed to migrate database: %v", err)
		}

		// 创建计数器
		utils.CreateCounter4Table(db, tableName)

		// 注册路由
		controllers.RegisterGenericRoutes(r, tableName, reflect.Zero(modelType).Interface())
	}

	log.Println("server starting on :38081")
	r.Run(":38081")
}
