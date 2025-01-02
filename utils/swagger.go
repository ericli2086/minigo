package utils

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/swaggo/swag"
)

// SwaggerInfo 存储 Swagger 文档的基本信息
type SwaggerInfo struct {
	Title       string
	Description string
	Version     string
	BasePath    string
}

// GenericSwaggerGenerator 用于生成通用 API 的 Swagger 文档
type GenericSwaggerGenerator struct {
	info SwaggerInfo
}

// NewSwaggerGenerator 创建一个新的 Swagger 生成器实例
func NewSwaggerGenerator(info SwaggerInfo) *GenericSwaggerGenerator {
	return &GenericSwaggerGenerator{
		info: info,
	}
}

// GenerateSwaggerDocs 为给定的模型生成 Swagger 文档
func (g *GenericSwaggerGenerator) GenerateSwaggerDocs(resourceName string, model interface{}) {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	// 生成模型定义
	modelSchema := g.generateModelSchema(modelType)

	// 注册 Swagger 信息
	swag.Register(swag.Name, &swag.Spec{
		InfoInstanceName: swag.Name,
		SwaggerTemplate:  g.generateSwaggerTemplate(resourceName, modelType.Name(), modelSchema, modelType),
	})
}

// generateModelSchema 生成模型的 Schema 定义
func (g *GenericSwaggerGenerator) generateModelSchema(modelType reflect.Type) string {
	var properties []string

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)

		// 获取字段标签
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		fieldName := strings.Split(jsonTag, ",")[0]
		if fieldName == "" {
			fieldName = field.Name
		}

		// 获取字段类型
		fieldType := g.convertGoTypeToSwaggerType(field.Type)

		// 获取字段描述
		description := field.Tag.Get("description")
		if description == "" {
			description = fieldName
		}

		// 构建属性定义
		var property string
		if fieldName != "BaseModel" {
			// 构建属性定义
			property = fmt.Sprintf(`
          %s:
            type: %s
            description: "%s"`, fieldName, fieldType, description)
		} else {
			property = `
          id:
            type: integer
            description: "Resource ID"
          created_at:
            type: integer
            description: "Create timestamp"
          updated_at:
            type: integer
            description: "Update timestamp"`
		}

		properties = append(properties, property)
	}

	return strings.Join(properties, "\n")
}

// convertGoTypeToSwaggerType 将 Go 类型转换为 Swagger 类型
func (g *GenericSwaggerGenerator) convertGoTypeToSwaggerType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.String:
		return "string"
	case reflect.Slice:
		return "array"
	default:
		return "object"
	}
}

// generateSwaggerTemplate 生成完整的 Swagger 模板
func (g *GenericSwaggerGenerator) generateSwaggerTemplate(resourceName, modelName string, modelSchema string, modelType reflect.Type) string {
	return fmt.Sprintf(`
swagger: "2.0"
info:
  title: %s
  description: %s
  version: %s
basePath: %s
schemes:
  - http
  - https
consumes:
  - application/json
  - application/x-www-form-urlencoded
produces:
  - application/json

paths:
  /%s:
    get:
      summary: List %s
      description: Get a paginated list of %s
      parameters:
        - in: query
          name: page
          type: integer
          description: Page number
          default: 1
        - in: query
          name: page_size
          type: integer
          description: Number of items per page
          default: 10
        - in: query
          name: search
          type: string
          description: Search term
        - in: query
          name: order
          type: string
          description: Order by field (prefix with - for desc)
      responses:
        200:
          description: Successful operation
          schema:
            type: object
            properties:
              total:
                type: integer
              page:
                type: integer
              page_size:
                type: integer
              data:
                type: array
                items:
                  $ref: "#/definitions/%s"
    post:
      summary: Batch Create %s
      description: Create new %s (single or batch)
      parameters:
        - in: body
          name: body
          required: true
          schema:
            type: array
            items:
              $ref: "#/definitions/%sSingleUpdate"
      responses:
        201:
          description: Successfully created
          schema:
            $ref: "#/definitions/%s"
    delete:
      summary: Batch Delete %s
      description: Delete multiple %s by IDs
      parameters:
        - in: query
          name: ids
          type: string
          description: Comma separated IDs (e.g. 1,2,3)
        - in: body
          name: body
          schema:
            type: object
            properties:
              ids:
                type: array
                items:
                  type: integer
                description: Array of IDs to delete
      responses:
        200:
          description: Successfully deleted
          schema:
            type: object
            properties:
              message:
                type: string
    put:
      summary: Batch Update %s
      description: Update multiple %s
      parameters:
        - in: body
          name: body
          required: true
          schema:
            type: object
            properties:
              objs:
                type: array
                items:
                  $ref: "#/definitions/%sBatchUpdate"
      responses:
        200:
          description: Successfully updated
          schema:
            type: object
            properties:
              message:
                type: string
    
  /%s/{id}:
    get:
      summary: Get %s
      description: Get a single %s by ID
      parameters:
        - in: path
          name: id
          required: true
          type: integer
          description: ID of the %s
      responses:
        200:
          description: Successful operation
          schema:
            $ref: "#/definitions/%s"
    put:
      summary: Update %s
      description: Update an existing %s
      parameters:
        - in: path
          name: id
          required: true
          type: integer
          description: ID of the %s
        - in: body
          name: body
          required: true
          schema:
            $ref: "#/definitions/%sSingleUpdate"
      responses:
        200:
          description: Successfully updated
          schema:
            type: object
            properties:
              message:
                type: string
    delete:
      summary: Delete %s
      description: Delete a %s by ID
      parameters:
        - in: path
          name: id
          required: true
          type: integer
          description: ID of the %s
      responses:
        200:
          description: Successfully deleted
          schema:
            type: object
            properties:
              message:
                type: string

definitions:
  %s:
    type: object
    properties:%s
  %sSingleUpdate:
    type: object
    description: Fields that can be updated
    properties:
%s
  %sBatchUpdate:
    type: object
    description: Fields that can be updated
    properties:
%s
`,
		g.info.Title,                            // 1
		g.info.Description,                      // 2
		g.info.Version,                          // 3
		g.info.BasePath,                         // 4
		resourceName,                            // 5
		modelName,                               // 6
		modelName,                               // 7
		modelName,                               // 8
		modelName,                               // 9
		modelName,                               // 10
		modelName,                               // 11
		modelName,                               // 12
		modelName,                               // 13
		modelName,                               // 14
		modelName,                               // 15
		modelName,                               // 16
		modelName,                               // 17
		resourceName,                            // 18
		modelName,                               // 19
		modelName,                               // 20
		modelName,                               // 21
		modelName,                               // 22
		modelName,                               // 23
		modelName,                               // 24
		modelName,                               // 25
		modelName,                               // 26
		modelName,                               // 27
		modelName,                               // 28
		modelName,                               // 29
		modelName,                               // 30
		modelSchema,                             // 31
		modelName,                               // 32
		g.generateSingleUpdateSchema(modelType), // 33
		modelName,                               // 34
		g.generateBatchUpdateSchema(modelType),  // 35
	)
}

// generateBatchUpdateSchema 生成可更新字段的 Schema
func (g *GenericSwaggerGenerator) generateBatchUpdateSchema(modelType reflect.Type) string {
	var properties []string

	// 添加 id 字段
	properties = append(properties, `
      id:
        type: integer
        description: "Resource ID"`)

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		tag := field.Tag.Get("ctags")

		if tag != "" {
			fieldName := strings.Split(tag, ",")[0]
			fieldTags := strings.Split(tag, ",")[1:]

			if fieldName != "" && ExistsIn(fieldTags, "u") {
				fieldType := g.convertGoTypeToSwaggerType(field.Type)
				description := field.Tag.Get("description")
				if description == "" {
					description = fieldName
				}

				property := fmt.Sprintf(`      %s:
        type: %s
        description: "%s"`, fieldName, fieldType, description)
				properties = append(properties, property)
			}
		}
	}

	return strings.Join(properties, "\n")
}

// generateSingleUpdateSchema 生成可更新字段的 Schema
func (g *GenericSwaggerGenerator) generateSingleUpdateSchema(modelType reflect.Type) string {
	var properties []string

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		tag := field.Tag.Get("ctags")

		if tag != "" {
			fieldName := strings.Split(tag, ",")[0]
			fieldTags := strings.Split(tag, ",")[1:]

			if fieldName != "" && ExistsIn(fieldTags, "u") {
				fieldType := g.convertGoTypeToSwaggerType(field.Type)
				description := field.Tag.Get("description")
				if description == "" {
					description = fieldName
				}

				property := fmt.Sprintf(`      %s:
        type: %s
        description: "%s"`, fieldName, fieldType, description)
				properties = append(properties, property)
			}
		}
	}

	return strings.Join(properties, "\n")
}

// RegisterSwaggerRoute 注册 Swagger UI 路由
func (g *GenericSwaggerGenerator) RegisterSwaggerRoute(r *gin.Engine) {
	// 需要先安装 gin-swagger
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
}
