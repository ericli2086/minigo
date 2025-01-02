package main

import (
	"log"
	"reflect"

	"github.com/gin-gonic/gin"

	"minigo/controllers"
	"minigo/middlewares"
	"minigo/models"
	"minigo/utils"
)

func main() {
	logger := utils.GetLogger()
	db := utils.GetDB("test.db").SetLogger(logger)

	// 测试日志
	// logger.Info("Info message")
	// logger.Warn("Warn message")
	// logger.Error("Error message")
	// logger.Debug("Debug message")
	// logger.WithTraceID("trace-abc123").Info("Info message")
	// logger.WithTraceID("trace-abc234").Info("创建用户", zap.String("username", "test"))
	// logger.Fatal("Fatal message")

	// 设置路由
	r := gin.Default()

	// 注册事务中间件
	r.Use(middlewares.TransactionMiddleware(db.DB))

	for _, model := range []interface{}{models.User{}} {
		modelType, modelPtr, tableName := utils.GetModelInfo(model)
		// 迁移数据库
		err := db.DB.AutoMigrate(modelPtr)
		if err != nil {
			log.Fatalf("failed to migrate database: %v", err)
		}

		// 创建计数器
		utils.CreateCounter4Table(db, tableName)

		// 注册路由
		controllers.RegisterGenericRoutes(r, "/api/"+tableName, reflect.Zero(modelType).Interface())
	}

	// 创建 Swagger 生成器
	swaggerGen := utils.NewSwaggerGenerator(utils.SwaggerInfo{
		Title:       "Your API",
		Description: "Your API Description",
		Version:     "1.0",
		BasePath:    "/api",
	})
	for _, model := range []interface{}{models.User{}} {
		modelType, _, tableName := utils.GetModelInfo(model)
		swaggerGen.GenerateSwaggerDocs(tableName, reflect.Zero(modelType).Interface())
	}
	swaggerGen.RegisterSwaggerRoute(r)

	log.Println("server starting on :38080")
	r.Run(":38080")
}
