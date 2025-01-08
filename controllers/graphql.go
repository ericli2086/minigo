package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"go.uber.org/zap"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"

	"minigo/utils"
)

// AutoGraphQL 核心引擎
type AutoGraphQL struct {
	db       *gorm.DB
	registry *TypeRegistry
	schema   graphql.Schema
	config   Config
}

// Config 配置选项
type Config struct {
	EnableQuery    bool
	EnableMutation bool
	EnableList     bool
	BatchSize      int
	MaxLimit       int
}

// NewGraphQL 创建新的AutoGraphQL实例
func NewGraphQL(db *gorm.DB, config Config) *AutoGraphQL {
	return &AutoGraphQL{
		db: db,
		registry: &TypeRegistry{
			db:         db,
			types:      make(map[string]*graphql.Object),
			inputTypes: make(map[string]*graphql.InputObject),
			tableMeta:  make(map[string]*TableMeta),
		},
		config: config,
	}
}

// TypeRegistry 类型注册中心
type TypeRegistry struct {
	db         *gorm.DB
	types      map[string]*graphql.Object
	inputTypes map[string]*graphql.InputObject
	tableMeta  map[string]*TableMeta
}

// TableMeta 表元数据
type TableMeta struct {
	Name       string
	Columns    []ColumnMeta
	Relations  []RelationMeta
	PrimaryKey string
}

// ColumnMeta 列元数据
type ColumnMeta struct {
	Name     string
	Type     string
	Nullable bool
	IsKey    bool
	Tags     map[string]string // 自定义标签
}

// RelationMeta 关系元数据
type RelationMeta struct {
	Name         string
	RelatedTable string
	RelationType string // hasOne, hasMany, belongsTo
	ForeignKey   string
	ReferenceKey string
}

// RegisterGraphql4Table 注册表到类型注册中心
func (ag *AutoGraphQL) RegisterGraphql4Table(resourceName string, model interface{}) error {
	_, _, tableName := utils.GetModelInfo(model)

	err := ag.registry.RegisterTable(tableName, model)
	if err != nil {
		return err
	}

	// 生成GraphQL类型
	ag.registry.GenerateGraphQLType(tableName, resourceName)
	ag.registry.GenerateInputType(tableName, resourceName)

	return ag.buildSchema(resourceName)
}

// RegisterTable 注册表到类型注册中心
func (r *TypeRegistry) RegisterTable(tableName string, model interface{}) error {
	// 获取数据库类型
	dialectName := r.db.Dialector.Name()

	var columns []struct {
		ColumnName string `gorm:"column:COLUMN_NAME"`
		DataType   string `gorm:"column:DATA_TYPE"`
		IsNullable string `gorm:"column:IS_NULLABLE"`
		ColumnKey  string `gorm:"column:COLUMN_KEY"`
		ColumnType string `gorm:"column:COLUMN_TYPE"`
	}

	var err error

	switch dialectName {
	case "sqlite":
		// SQLite specific query
		err = r.db.Raw(`
            SELECT 
                name as COLUMN_NAME,
                type as DATA_TYPE,
                CASE WHEN "notnull" = 0 THEN 'YES' ELSE 'NO' END as IS_NULLABLE,
                CASE WHEN pk = 1 THEN 'PRI' ELSE '' END as COLUMN_KEY,
                type as COLUMN_TYPE
            FROM pragma_table_info(?)
        `, tableName).Scan(&columns).Error

	case "mysql", "mariadb", "tidb":
		// MySQL specific query
		err = r.db.Raw(`
            SELECT 
                COLUMN_NAME, 
                DATA_TYPE,
                IS_NULLABLE,
                COLUMN_KEY,
                COLUMN_TYPE
            FROM INFORMATION_SCHEMA.COLUMNS 
            WHERE TABLE_NAME = ? AND TABLE_SCHEMA = DATABASE()
        `, tableName).Scan(&columns).Error

	case "postgres":
		// PostgreSQL specific query
		err = r.db.Raw(`
            SELECT 
                column_name as COLUMN_NAME,
                data_type as DATA_TYPE,
                is_nullable as IS_NULLABLE,
                CASE WHEN position('PRIMARY KEY' in string_agg(constraint_type, ', ')) > 0 
                     THEN 'PRI' ELSE '' END as COLUMN_KEY,
                data_type as COLUMN_TYPE
            FROM information_schema.columns
            LEFT JOIN information_schema.constraint_column_usage 
                ON columns.column_name = constraint_column_usage.column_name 
                AND columns.table_name = constraint_column_usage.table_name
            LEFT JOIN information_schema.table_constraints 
                ON constraint_column_usage.constraint_name = table_constraints.constraint_name
            WHERE columns.table_name = ?
            GROUP BY column_name, data_type, is_nullable
        `, tableName).Scan(&columns).Error

	default:
		return fmt.Errorf("unsupported database dialect: %s", dialectName)
	}

	if err != nil {
		return fmt.Errorf("failed to get table schema: %v", err)
	}

	meta := &TableMeta{
		Name:    tableName,
		Columns: make([]ColumnMeta, 0),
	}

	for _, col := range columns {
		meta.Columns = append(meta.Columns, ColumnMeta{
			Name:     col.ColumnName,
			Type:     col.DataType,
			Nullable: col.IsNullable == "YES",
			IsKey:    col.ColumnKey == "PRI",
			Tags:     parseTags(col.ColumnName, model), // 解析自定义标签
		})

		if col.ColumnKey == "PRI" {
			meta.PrimaryKey = col.ColumnName
		}
	}

	// 获取外键关系
	var constraints []struct {
		ConstraintName string
		ColumnName     string
		RefTableName   string
		RefColumnName  string
	}

	switch dialectName {
	case "sqlite":
		err = r.db.Raw(`
            SELECT 
                fk.'from' as ColumnName,
                fk.'table' as RefTableName,
                fk.'to' as RefColumnName,
                fk.'from' || '_fk' as ConstraintName
            FROM pragma_foreign_key_list(?) as fk
        `, tableName).Scan(&constraints).Error

	case "mysql":
		err = r.db.Raw(`
            SELECT 
                tc.CONSTRAINT_NAME,
                kcu.COLUMN_NAME,
                kcu.REFERENCED_TABLE_NAME,
                kcu.REFERENCED_COLUMN_NAME
            FROM information_schema.TABLE_CONSTRAINTS tc
            JOIN information_schema.KEY_COLUMN_USAGE kcu
                ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
            WHERE tc.TABLE_NAME = ?
                AND tc.CONSTRAINT_TYPE = 'FOREIGN KEY'
                AND tc.TABLE_SCHEMA = DATABASE()
        `, tableName).Scan(&constraints).Error

	case "postgres":
		err = r.db.Raw(`
            SELECT
                tc.constraint_name as ConstraintName,
                kcu.column_name as ColumnName,
                ccu.table_name as RefTableName,
                ccu.column_name as RefColumnName
            FROM information_schema.table_constraints AS tc
            JOIN information_schema.key_column_usage AS kcu
                ON tc.constraint_name = kcu.constraint_name
            JOIN information_schema.constraint_column_usage AS ccu
                ON ccu.constraint_name = tc.constraint_name
            WHERE tc.constraint_type = 'FOREIGN KEY'
                AND tc.table_name = ?
        `, tableName).Scan(&constraints).Error
	}

	if err != nil {
		return fmt.Errorf("failed to get foreign keys: %v", err)
	}

	for _, constraint := range constraints {
		meta.Relations = append(meta.Relations, RelationMeta{
			Name:         strings.TrimSuffix(constraint.ColumnName, "_id"),
			RelatedTable: constraint.RefTableName,
			RelationType: "belongsTo",
			ForeignKey:   constraint.ColumnName,
			ReferenceKey: constraint.RefColumnName,
		})
	}

	r.tableMeta[tableName] = meta
	return nil
}

// parseTags 解析自定义标签
func parseTags(columnName string, model interface{}) map[string]string {
	tags := make(map[string]string)

	// 获取模型反射类型和指针
	modelType, _, _ := utils.GetModelInfo(model)

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		if utils.Camel2Snake(field.Name) == columnName {
			tags["json"] = field.Tag.Get("json")
			tags["ctags"] = field.Tag.Get("ctags")
		}
	}

	return tags
}

// GenerateGraphQLType 生成GraphQL对象类型
func (r *TypeRegistry) GenerateGraphQLType(tableName string, resourceName ...string) *graphql.Object {
	if existingType, ok := r.types[tableName]; ok {
		return existingType
	}

	meta := r.tableMeta[tableName]
	fields := graphql.Fields{}

	// 生成普通字段
	for _, col := range meta.Columns {
		if col.Tags["json"] != "-" { // 排除不展示的字段
			fields[col.Name] = &graphql.Field{
				Type: r.mapSQLTypeToGraphQL(col.Type, col.Nullable),
			}
		}
	}

	// 生成关系字段
	for _, relation := range meta.Relations {
		fields[relation.Name] = &graphql.Field{
			Type:    r.getRelationType(relation),
			Resolve: r.generateRelationResolver(relation),
		}
	}

	caser := cases.Title(language.English)
	objectType := graphql.NewObject(graphql.ObjectConfig{
		// Name:   caser.String(tableName),
		Name:   caser.String(resourceName[0]),
		Fields: fields,
	})

	r.types[tableName] = objectType
	return objectType
}

// GenerateInputType 生成GraphQL输入类型
func (r *TypeRegistry) GenerateInputType(tableName string, resourceName ...string) *graphql.InputObject {
	if existingType, ok := r.inputTypes[tableName]; ok {
		return existingType
	}

	meta := r.tableMeta[tableName]
	fields := graphql.InputObjectConfigFieldMap{}

	for _, col := range meta.Columns {
		if !col.IsKey && col.Tags["json"] != "-" { // 排除主键和不展示的字段
			fields[col.Name] = &graphql.InputObjectFieldConfig{
				Type: r.mapSQLTypeToGraphQL(col.Type, true), // 输入字段总是可空的
			}
		}
	}

	caser := cases.Title(language.English)
	inputType := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:   caser.String(resourceName[0]) + "Input",
		Fields: fields,
	})

	r.inputTypes[tableName] = inputType
	return inputType
}

// mapSQLTypeToGraphQL 将SQL类型映射为GraphQL类型
func (r *TypeRegistry) mapSQLTypeToGraphQL(sqlType string, nullable bool) graphql.Type {
	var baseType graphql.Type
	switch strings.ToLower(sqlType) {
	case "int", "tinyint", "smallint", "mediumint", "bigint":
		baseType = graphql.Int
	case "float", "double", "decimal":
		baseType = graphql.Float
	case "char", "varchar", "text", "mediumtext", "longtext":
		baseType = graphql.String
	case "boolean", "tinyint(1)":
		baseType = graphql.Boolean
	case "datetime", "timestamp", "date":
		baseType = graphql.DateTime
	default:
		baseType = graphql.String
	}

	if !nullable {
		return graphql.NewNonNull(baseType)
	}
	return baseType
}

// getRelationType 获取关系类型
func (r *TypeRegistry) getRelationType(relation RelationMeta) graphql.Type {
	relatedType := r.GenerateGraphQLType(relation.RelatedTable)
	if relation.RelationType == "hasMany" {
		return graphql.NewList(relatedType)
	}
	return relatedType
}

// generateRelationResolver 生成关系解析器
func (r *TypeRegistry) generateRelationResolver(relation RelationMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		source := p.Source.(map[string]interface{})
		foreignKeyValue := source[relation.ForeignKey]

		if foreignKeyValue == nil {
			return nil, nil
		}

		tx := getTransaction(p.Context, r.db)

		if relation.RelationType == "hasMany" {
			var results []map[string]interface{}
			err := tx.Table(relation.RelatedTable).
				Where(relation.ReferenceKey+" = ?", foreignKeyValue).
				Find(&results).Error
			return results, err
		}

		result := make(map[string]interface{})
		err := tx.Table(relation.RelatedTable).
			Where(relation.ReferenceKey+" = ?", foreignKeyValue).
			First(&result).Error
		return result, err
	}
}

// buildSchema 构建GraphQL schema
func (ag *AutoGraphQL) buildSchema(resourceName string) error {
	queryFields := graphql.Fields{}
	mutationFields := graphql.Fields{}

	for tableName, meta := range ag.registry.tableMeta {
		objectType := ag.registry.types[tableName]
		inputType := ag.registry.inputTypes[tableName]

		// 查询字段
		if ag.config.EnableQuery {
			// 单个查询
			queryFields[resourceName] = &graphql.Field{
				Type: objectType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: ag.generateFieldResolver(resourceName, tableName, meta),
			}

			// 列表查询
			if ag.config.EnableList {
				queryFields[resourceName+"s"] = &graphql.Field{
					Type: graphql.NewObject(graphql.ObjectConfig{
						Name: cases.Title(language.English).String(resourceName) + "s",
						Fields: graphql.Fields{
							"items": &graphql.Field{
								Type: graphql.NewList(objectType),
							},
							"total": &graphql.Field{
								Type: graphql.Int,
							},
						},
					}),
					Args: graphql.FieldConfigArgument{
						"pageSize": &graphql.ArgumentConfig{
							Type:        graphql.Int,
							Description: fmt.Sprintf("Maximum number of perpage (default: %d, max: %d)", ag.config.BatchSize, ag.config.MaxLimit),
						},
						"page": &graphql.ArgumentConfig{
							Type:        graphql.Int,
							Description: "Number of page",
						},
						"orderBy": &graphql.ArgumentConfig{
							Type:        graphql.String,
							Description: "SQL ORDER BY clause",
						},
					},
					Resolve: ag.generateListResolver(resourceName+"s", tableName, meta),
				}
			}
		}

		// 修改字段
		if ag.config.EnableMutation {
			// 创建
			mutationFields["create"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: objectType,
				Args: graphql.FieldConfigArgument{
					"input": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(inputType),
					},
				},
				Resolve: ag.generateMutationResolver("create", tableName, meta),
			}

			// 批量创建
			mutationFields["createBulk"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: graphql.NewList(objectType),
				Args: graphql.FieldConfigArgument{
					"inputs": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(inputType))),
					},
				},
				Resolve: ag.generateBulkMutationResolver("create", tableName, meta),
			}

			// 更新
			mutationFields["update"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: objectType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
					"input": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(inputType),
					},
				},
				Resolve: ag.generateMutationResolver("update", tableName, meta),
			}

			// 批量更新
			mutationFields["updateBulk"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: graphql.NewList(objectType),
				Args: graphql.FieldConfigArgument{
					"inputs": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(inputType))),
					},
				},
				Resolve: ag.generateBulkMutationResolver("update", tableName, meta),
			}

			// 删除
			mutationFields["delete"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: graphql.Boolean,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: ag.generateMutationResolver("delete", tableName, meta),
			}

			// 批量删除
			mutationFields["deleteBulk"+cases.Title(language.English).String(tableName)] = &graphql.Field{
				Type: graphql.Boolean,
				Args: graphql.FieldConfigArgument{
					"ids": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.ID))),
					},
				},
				Resolve: ag.generateBulkMutationResolver("delete", tableName, meta),
			}
		}
	}

	// 构建Schema
	schemaConfig := graphql.SchemaConfig{}

	// 如果启用了查询
	if len(queryFields) > 0 {
		schemaConfig.Query = graphql.NewObject(graphql.ObjectConfig{
			Name:   "Query",
			Fields: queryFields,
		})
	}

	// 如果启用了修改
	if len(mutationFields) > 0 {
		schemaConfig.Mutation = graphql.NewObject(graphql.ObjectConfig{
			Name:   "Mutation",
			Fields: mutationFields,
		})
	}

	schema, err := graphql.NewSchema(schemaConfig)
	if err != nil {
		return err
	}

	ag.schema = schema
	return nil
}

// Handler GraphQL请求处理器
func (ag *AutoGraphQL) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 读取请求体内容
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			logger := utils.GetLogger()
			logger.WithTraceID(c.GetString("trace_id")).Error("failed to execute graphql", zap.Error(err))
		}

		// 重要：重新设置请求体，因为ReadAll会消耗body
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		var request struct {
			Query         string                 `json:"query"`
			OperationName string                 `json:"operationName"`
			Variables     map[string]interface{} `json:"variables"`
		}

		// 在执行前将gin.Context存入context中
		ctx := context.WithValue(c.Request.Context(), "ginContext", c)
		ctx = context.WithValue(ctx, "requestBody", body)

		if err := c.BindJSON(&request); err != nil {
			logger := utils.GetLogger()
			logger.WithTraceID(c.GetString("trace_id")).Error("failed to execute graphql", zap.Error(err))
		}

		// 已经由中间件统一处理事务，仅需在报错时触发中间件自动回滚即可
		result := graphql.Do(graphql.Params{
			Schema:         ag.schema,
			RequestString:  request.Query,
			OperationName:  request.OperationName,
			VariableValues: request.Variables,
			Context:        ctx,
		})

		if len(result.Errors) > 0 {
			var errMsgs []string
			for _, err := range result.Errors {
				errMsgs = append(errMsgs, err.Message)
			}
			errors := errors.New(strings.Join(errMsgs, ";"))
			logger := utils.GetLogger()
			logger.WithTraceID(c.GetString("trace_id")).Error("failed to do graphql", zap.Error(errors))
			c.Error(errors)
		}

		// GraphQL 的官方规范确实规定，无论请求是成功还是失败，HTTP 响应的状态码 始终是 200
		c.JSON(http.StatusOK, result)
	}
}

// generateFieldResolver 生成字段解析器
func (ag *AutoGraphQL) generateFieldResolver(queryName string, tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		ctx := p.Context
		tx := getTransaction(ctx, ag.db)

		// 提取字段
		fields, err := getSelectedFields(ctx, queryName)
		if err != nil {
			return nil, fmt.Errorf("failed to parse query: %v", err)
		}

		id := p.Args["id"]

		var softDelete = false
		for name, meta := range ag.registry.tableMeta {
			for _, column := range meta.Columns {
				if name == tableName && column.Name == "deleted_at" {
					softDelete = true
				}
			}
		}

		result := make(map[string]interface{})
		if softDelete {
			err = tx.Table(tableName).Select(fields).Where(meta.PrimaryKey+"=? AND deleted_at=0", id).Take(&result).Error
		} else {
			err = tx.Table(tableName).Select(fields).Where(meta.PrimaryKey+"=?", id).Take(&result).Error
		}
		if err != nil {
			return result, err
		}

		return result, nil
	}
}

// generateListResolver 生成列表解析器
func (ag *AutoGraphQL) generateListResolver(queryName string, tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		_ = meta
		ctx := p.Context
		tx := getTransaction(ctx, ag.db)

		// 提取字段
		fields, err := getSelectedFields(ctx, queryName)
		if err != nil {
			return nil, fmt.Errorf("failed to parse query: %v", err)
		}

		pageSize := ag.config.BatchSize
		if p.Args["pageSize"] != nil {
			pageSize = p.Args["pageSize"].(int)
			if pageSize > ag.config.MaxLimit {
				pageSize = ag.config.MaxLimit
			}
		}

		page := 1
		if p.Args["page"] != nil {
			page = p.Args["page"].(int)
		}

		query := tx.Table(tableName)

		// 使用反射检查字段标签，获取允许更新字段列表
		var softDelete = false
		var allowedOrderFields []string = []string{"id"}
		for name, meta := range ag.registry.tableMeta {
			for _, column := range meta.Columns {
				if name == tableName && column.Name == "deleted_at" {
					softDelete = true
				}
				tag := column.Tags["ctags"]
				if tag != "" {
					filedName := strings.Split(tag, ",")[0]
					filedTags := strings.Split(tag, ",")[1:]
					if filedName != "" && utils.ExistsIn(filedTags, "o") {
						allowedOrderFields = append(allowedOrderFields, filedName)
					}
				}
			}
		}

		if softDelete {
			query = query.Where("deleted_at=0")
		}

		if orderBy, ok := p.Args["orderBy"].(string); ok && orderBy != "" {
			if utils.ExistsIn(allowedOrderFields, strings.ReplaceAll(orderBy, "-", "")) {
				// 判断是升序还是降序
				var orderType string
				var orderField string

				if strings.HasPrefix(orderBy, "-") {
					// 降序
					orderField = orderBy[1:]
					orderType = "DESC"
				} else {
					// 升序
					orderField = orderBy
					orderType = "ASC"
				}

				// 构建排序查询
				orderQuery := fmt.Sprintf("%s %s", orderField, orderType)
				query = query.Order(orderQuery)
			}
		} else {
			query = query.Order("id DESC")
		}

		var results []map[string]interface{}
		err = query.Select(fields).Limit(pageSize).Offset((page - 1) * pageSize).Find(&results).Error
		if err != nil {
			return nil, err
		}

		var total int64
		status := tx.Raw("SELECT (counter) FROM counters WHERE name = ?", tableName).Scan(&total)
		if status.Error != nil {
			if softDelete {
				tx.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE deleted_at=0", tableName)).Scan(&total)
			} else {
				tx.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&total)
			}
		}

		return map[string]interface{}{
			"items": results,
			"total": total,
		}, nil
	}
}

// generateMutationResolver 生成修改解析器
func (ag *AutoGraphQL) generateMutationResolver(operation string, tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		tx := getTransaction(p.Context, ag.db)

		switch operation {
		case "create":
			input := p.Args["input"].(map[string]interface{})
			result := make(map[string]interface{})
			err := tx.Table(tableName).Create(input).Scan(&result).Error
			return result, err

		case "update":
			id := p.Args["id"]
			input := p.Args["input"].(map[string]interface{})
			result := make(map[string]interface{})
			err := tx.Table(tableName).Where(meta.PrimaryKey+" = ?", id).Updates(input).Scan(&result).Error
			return result, err

		case "delete":
			id := p.Args["id"]
			err := tx.Table(tableName).Where(meta.PrimaryKey+" = ?", id).Delete(nil).Error
			return err == nil, err

		default:
			return nil, fmt.Errorf("unknown operation: %s", operation)
		}
	}
}

// generateBulkMutationResolver 生成批量修改解析器
func (ag *AutoGraphQL) generateBulkMutationResolver(operation string, tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		tx := getTransaction(p.Context, ag.db)

		switch operation {
		case "create":
			inputs := p.Args["inputs"].([]interface{})
			var results []map[string]interface{}
			for _, input := range inputs {
				result := make(map[string]interface{})
				err := tx.Table(tableName).Create(input).Scan(&result).Error
				if err != nil {
					return nil, err
				}
				results = append(results, result)
			}
			return results, nil

		case "update":
			inputs := p.Args["inputs"].([]interface{})
			var results []map[string]interface{}
			for _, input := range inputs {
				inputMap := input.(map[string]interface{})
				id := inputMap[meta.PrimaryKey]
				result := make(map[string]interface{})
				err := tx.Table(tableName).Where(meta.PrimaryKey+" = ?", id).Updates(inputMap).Scan(&result).Error
				if err != nil {
					return nil, err
				}
				results = append(results, result)
			}
			return results, nil

		case "delete":
			ids := p.Args["ids"].([]interface{})
			for _, id := range ids {
				err := tx.Table(tableName).Where(meta.PrimaryKey+" = ?", id).Delete(nil).Error
				if err != nil {
					return false, err
				}
			}
			return true, nil

		default:
			return nil, fmt.Errorf("unknown operation: %s", operation)
		}
	}
}

// getTransaction 获取事务对象
func getTransaction(ctx context.Context, db *gorm.DB) *gorm.DB {
	if ginCtx, ok := ctx.Value("ginContext").(*gin.Context); ok {
		return utils.GetDbByCtx(ginCtx)
	}

	return db
}

// getSelectedFields 获取查询字段
func getSelectedFields(ctx context.Context, queryName string) ([]string, error) {
	var reqestBody interface{}
	if body, ok := ctx.Value("requestBody").([]byte); ok {
		if err := json.Unmarshal(body, &reqestBody); err != nil {
			return nil, fmt.Errorf("failed to parse json body: %v", err)
		}
	}

	// 检查 requestBody 是否为 map 类型
	queryMap, ok := reqestBody.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid request body")
	}

	// 获取 query 字符串
	queryString, ok := queryMap["query"].(string)
	if !ok {
		return nil, fmt.Errorf("query string not found in request body")
	}

	// 提取字段
	fields, err := extractSelectedFields(queryString, queryName)
	if err != nil {
		return nil, fmt.Errorf("failed to parse query: %v", err)
	}

	return fields, nil
}

// extractSelectedFields 解析 GraphQL 查询字符串
func extractSelectedFields(query string, queryName string) ([]string, error) {
	astDoc, err := parser.Parse(parser.ParseParams{
		Source: query,
	})
	if err != nil {
		return nil, fmt.Errorf("parse query error: %w", err)
	}

	var fields []string

	// 遍历 AST 节点
	for _, def := range astDoc.Definitions {
		opDef, ok := def.(*ast.OperationDefinition)
		if !ok {
			continue
		}

		// 查找指定的查询
		for _, selection := range opDef.SelectionSet.Selections {
			field, ok := selection.(*ast.Field)
			if !ok || field.Name.Value != queryName {
				continue
			}

			// 如果找到指定查询，提取其字段
			if field.SelectionSet != nil {
				for _, subSelection := range field.SelectionSet.Selections {
					subField, ok := subSelection.(*ast.Field)
					if !ok {
						continue
					}

					if subField.Name.Value == "items" {
						// 提取 items 内的字段
						if subField.SelectionSet != nil {
							for _, itemSelection := range subField.SelectionSet.Selections {
								if itemField, ok := itemSelection.(*ast.Field); ok {
									fields = append(fields, itemField.Name.Value)
								}
							}
						}
					} else if subField.Name.Value == "total" {
						continue
					} else {
						fields = append(fields, subField.Name.Value)
					}
				}
			}
		}
	}

	return fields, nil
}
