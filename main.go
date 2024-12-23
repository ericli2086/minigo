package main

import (
	"log"

	"github.com/gin-gonic/gin"
	
	"minigo/utils"
	"minigo/models"
	"minigo/controllers"
	"minigo/middlewares"
)

func main() {
	dsn := "test.db"

	// 初始化数据库工具
	dbutil := utils.InitDB(dsn)

	// 迁移数据库
	err := dbutil.DB.AutoMigrate(&models.User{})
	if err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	// 为指定表创建触发计数器
	tableNames := []string{"users"}
	for _, tableName := range tableNames {
		dbutil.CreateCounter4Table(tableName)
		log.Printf("Counter for table %s created successfully.\n", tableName)
	}

	// 设置路由
	r := gin.Default()

	// 注册事务中间件
	r.Use(middlewares.TransactionMiddleware(dbutil.DB))

	// 注册user模型路由
	controllers.RegisterGenericRoutes(r, "users", models.User{})

	log.Println("Server starting on :8080")
	r.Run(":8080")
}
