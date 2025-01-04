package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/graphql-go/graphql"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gorm.io/gorm"
)

// AutoGraphQL 核心引擎
type AutoGraphQL struct {
	db       *gorm.DB
	registry *TypeRegistry
	schema   graphql.Schema
	config   Config
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
}

// RelationMeta 关系元数据
type RelationMeta struct {
	Name         string
	RelatedTable string
	RelationType string // hasOne, hasMany, belongsTo
	ForeignKey   string
	ReferenceKey string
}

// Config 配置选项
type Config struct {
	EnableQuery    bool
	EnableMutation bool
	EnableList     bool
	BatchSize      int
	MaxLimit       int
}

// New 创建新的AutoGraphQL实例
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

// RegisterTable 注册表
func (ag *AutoGraphQL) RegisterGraphql4Table(tableName string) error {
	err := ag.registry.RegisterTable(tableName)
	if err != nil {
		return err
	}

	// 生成GraphQL类型
	ag.registry.GenerateGraphQLType(tableName)
	ag.registry.GenerateInputType(tableName)

	return ag.buildSchema()
}

// RegisterTable 注册表到类型注册中心
func (r *TypeRegistry) RegisterTable(tableName string) error {
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

// GenerateGraphQLType 生成GraphQL对象类型
func (r *TypeRegistry) GenerateGraphQLType(tableName string) *graphql.Object {
	if existingType, ok := r.types[tableName]; ok {
		return existingType
	}

	meta := r.tableMeta[tableName]
	fields := graphql.Fields{}

	// 生成普通字段
	for _, col := range meta.Columns {
		fields[col.Name] = &graphql.Field{
			Type: r.mapSQLTypeToGraphQL(col.Type, col.Nullable),
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
		Name:   caser.String(tableName),
		Fields: fields,
	})

	r.types[tableName] = objectType
	return objectType
}

// GenerateInputType 生成GraphQL输入类型
func (r *TypeRegistry) GenerateInputType(tableName string) *graphql.InputObject {
	if existingType, ok := r.inputTypes[tableName]; ok {
		return existingType
	}

	meta := r.tableMeta[tableName]
	fields := graphql.InputObjectConfigFieldMap{}

	for _, col := range meta.Columns {
		if !col.IsKey { // 排除主键
			fields[col.Name] = &graphql.InputObjectFieldConfig{
				Type: r.mapSQLTypeToGraphQL(col.Type, true), // 输入字段总是可空的
			}
		}
	}

	caser := cases.Title(language.English)
	inputType := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:   caser.String(tableName) + "Input",
		Fields: fields,
	})

	r.inputTypes[tableName] = inputType
	return inputType
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
func (ag *AutoGraphQL) buildSchema() error {
	queryFields := graphql.Fields{}
	mutationFields := graphql.Fields{}

	for tableName, meta := range ag.registry.tableMeta {
		objectType := ag.registry.types[tableName]
		inputType := ag.registry.inputTypes[tableName]

		// 查询字段
		if ag.config.EnableQuery {
			// 单个查询
			queryFields[tableName] = &graphql.Field{
				Type: objectType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: ag.generateFieldResolver(tableName, meta),
			}

			// 列表查询
			if ag.config.EnableList {
				queryFields[tableName+"List"] = &graphql.Field{
					Type: graphql.NewList(objectType),
					Args: graphql.FieldConfigArgument{
						"limit": &graphql.ArgumentConfig{
							Type:        graphql.Int,
							Description: fmt.Sprintf("Maximum number of records (default: %d, max: %d)", ag.config.BatchSize, ag.config.MaxLimit),
						},
						"offset": &graphql.ArgumentConfig{
							Type:        graphql.Int,
							Description: "Number of records to skip",
						},
						"where": &graphql.ArgumentConfig{
							Type:        graphql.String,
							Description: "SQL WHERE clause",
						},
						"order": &graphql.ArgumentConfig{
							Type:        graphql.String,
							Description: "SQL ORDER BY clause",
						},
					},
					Resolve: ag.generateListResolver(tableName, meta),
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
		var request struct {
			Query         string                 `json:"query"`
			OperationName string                 `json:"operationName"`
			Variables     map[string]interface{} `json:"variables"`
		}

		if err := c.BindJSON(&request); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		// 判断是否为mutation操作
		isMutation := strings.HasPrefix(strings.TrimSpace(request.Query), "mutation")

		var result *graphql.Result
		if isMutation {
			// 开启事务
			tx := ag.db.Begin()
			if tx.Error != nil {
				c.JSON(500, gin.H{"error": "Failed to start transaction"})
				return
			}

			ctx := context.WithValue(c.Request.Context(), "tx", tx)
			result = graphql.Do(graphql.Params{
				Schema:         ag.schema,
				RequestString:  request.Query,
				OperationName:  request.OperationName,
				VariableValues: request.Variables,
				Context:        ctx,
			})

			if len(result.Errors) > 0 {
				tx.Rollback()
			} else {
				if err := tx.Commit().Error; err != nil {
					tx.Rollback()
					c.JSON(500, gin.H{"error": "Failed to commit transaction"})
					return
				}
			}
		} else {
			result = graphql.Do(graphql.Params{
				Schema:         ag.schema,
				RequestString:  request.Query,
				OperationName:  request.OperationName,
				VariableValues: request.Variables,
				Context:        c.Request.Context(),
			})
		}

		statusCode := 200
		if len(result.Errors) > 0 {
			statusCode = 400
		}

		c.JSON(statusCode, result)
	}
}

// generateFieldResolver 生成字段解析器
func (ag *AutoGraphQL) generateFieldResolver(tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		tx := getTransaction(p.Context, ag.db)
		id := p.Args["id"]
		result := make(map[string]interface{})

		err := tx.Table(tableName).Where(meta.PrimaryKey+" = ?", id).Take(&result).Error
		if err != nil {
			return nil, err
		}

		return result, nil
	}
}

// generateListResolver 生成列表解析器
func (ag *AutoGraphQL) generateListResolver(tableName string, meta *TableMeta) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		tx := getTransaction(p.Context, ag.db)

		limit := ag.config.BatchSize
		if p.Args["limit"] != nil {
			limit = p.Args["limit"].(int)
			if limit > ag.config.MaxLimit {
				limit = ag.config.MaxLimit
			}
		}

		offset := 0
		if p.Args["offset"] != nil {
			offset = p.Args["offset"].(int)
		}

		query := tx.Table(tableName)

		if where, ok := p.Args["where"].(string); ok && where != "" {
			query = query.Where(where)
		}

		if order, ok := p.Args["order"].(string); ok && order != "" {
			query = query.Order(order)
		}

		var results []map[string]interface{}
		err := query.Limit(limit).Offset(offset).Find(&results).Error
		if err != nil {
			return nil, err
		}

		return results, nil
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

// getTransaction 获取事务对象
func getTransaction(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value("tx").(*gorm.DB); ok {
		return tx
	}
	return db
}
