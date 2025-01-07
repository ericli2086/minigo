package main

import (
	"embed"
	"log"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"minigo/controllers"
	"minigo/middlewares"
	"minigo/models"
	"minigo/utils"
)

//go:embed graphiql.html
var graphiqlFs embed.FS

func main() {
	logger := utils.GetLogger("config.yaml", "develop.logger")
	db := utils.GetDataBase("config.yaml", "develop.database").SetLogger(logger)

	// 设置路由
	r := gin.Default()

	// 注册中间件
	r.Use(middlewares.TransactionMiddleware(db.DB))
	r.Use(middlewares.TraceIDMiddleware("1"))
	r.Use(middlewares.Brotli(nil))

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
		controllers.RegisterRestfulRoutes(r, "/api/"+utils.ToSingular(tableName), reflect.Zero(modelType).Interface())
	}

	// 创建 GraphQL 实例
	autoGraphQL := controllers.NewGraphQL(db.DB, controllers.Config{
		EnableQuery:    true,
		EnableMutation: true,
		EnableList:     true,
		BatchSize:      10,
		MaxLimit:       1000,
	})
	for _, model := range []interface{}{models.User{}} {
		modelType, _, tableName := utils.GetModelInfo(model)
		err := autoGraphQL.RegisterGraphql4Table(utils.ToSingular(tableName), reflect.Zero(modelType).Interface())
		if err != nil {
			log.Fatalf("failed to register %s table: %v", tableName, err)
		}
	}
	r.POST("/api/graphql", autoGraphQL.Handler())

	// 创建 Swagger 生成器
	swaggerGen := utils.NewSwaggerGenerator(utils.SwaggerInfo{
		Title:       "Your API",
		Description: "Your API Description",
		Version:     "1.0",
		BasePath:    "/api",
	})
	for _, model := range []interface{}{models.User{}} {
		modelType, _, tableName := utils.GetModelInfo(model)
		swaggerGen.GenerateSwaggerDocs(utils.ToSingular(tableName), reflect.Zero(modelType).Interface())
	}
	swaggerGen.RegisterSwaggerRoute(r)

	// GraphiQL界面
	r.GET("/graphiql.html", func(c *gin.Context) {
		c.FileFromFS("graphiql.html", http.FS(graphiqlFs))
	})

	log.Println("server starting on :38080")
	r.Run(":38080")
}
