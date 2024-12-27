package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"minigo/utils"
)

// 通用路由注册函数
func RegisterGenericRoutes(r *gin.Engine, resourceName string, model interface{}) {
	// 创建路由组
	group := r.Group(resourceName)

	// 列表查询
	group.GET("", func(c *gin.Context) {
		genericList(c, model)
	})

	// 创建资源
	group.POST("", func(c *gin.Context) {
		genericCreate(c, model)
	})

	// 批量删除
	group.DELETE("", func(c *gin.Context) {
		genericBatchDelete(c, model)
	})

	// 批量更新
	group.PUT("", func(c *gin.Context) {
		genericUpdate(c, model)
	})

	// 获取单个资源
	group.GET("/:id", func(c *gin.Context) {
		genericRetrieve(c, model)
	})

	// 删除单个资源
	group.DELETE("/:id", func(c *gin.Context) {
		genericDelete(c, model)
	})

	// 更新单个资源
	group.PUT("/:id", func(c *gin.Context) {
		genericUpdate(c, model)
	})
}

// 通用列表查询
func genericList(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	const MaxPageSize = 10000
	pageSize = min(pageSize, MaxPageSize)
	offset := (page - 1) * pageSize

	// 获取模型类型和指针
	modelType, modelPtr, tableName := utils.GetModelInfo(model)

	// 使用反射检查字段标签，获取允许更新字段列表
	var allowedQueryFields []string
	var allowedOrderFields []string = []string{"id"}

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		tag := field.Tag.Get("ctags")
		if tag != "" {
			filedName := strings.Split(tag, ",")[0]
			filedTags := strings.Split(tag, ",")[1:]
			if filedName != "" && utils.ExistsIn(filedTags, "q") {
				allowedQueryFields = append(allowedQueryFields, filedName)
			}
			if filedName != "" && utils.ExistsIn(filedTags, "o") {
				allowedOrderFields = append(allowedOrderFields, filedName)
			}
		}
	}

	// 创建反射切片
	sliceType := reflect.SliceOf(modelType)
	results := reflect.New(sliceType).Elem()

	// 构建查询
	query := db.Model(modelPtr)

	// 是否使用计数器
	useCounter := true

	// 处理搜索参数
	searchParam := c.DefaultQuery("search", "")
	if searchParam != "" {
		// 获取所有字符串类型的字段
		var orConditions []string
		var args []interface{}

		for i := 0; i < modelType.NumField(); i++ {
			field := modelType.Field(i)

			// 只处理字符串类型的字段
			if field.Type.Kind() == reflect.String {
				// 获取字段的数据库列名
				// 如果没有设置 gorm:"column:<column_name>" 标签，Gorm 默认会将字段名称小写，并且采用下划线风格（如果是驼峰命名的话）
				columnName := field.Name
				if tag := field.Tag.Get("gorm"); tag != "" {
					if strings.Contains(tag, "column:") {
						match := regexp.MustCompile(`column:(\w+)`).FindStringSubmatch(tag)
						if len(match) > 1 {
							columnName = match[1]
						}
					}
				}
				columnName = utils.Camel2Snake(columnName)
				if columnName == "password" { // 排除password字段
					continue
				}

				orConditions = append(orConditions, fmt.Sprintf("%s LIKE ?", columnName))
				// TODO: 避免左通配符使用,如果确实需要完整的全文搜索考虑es或者根据实际使用数据库设置全文索引
				args = append(args, "%"+searchParam+"%")
			}
		}

		// 如果存在字符串字段，添加搜索条件
		if len(orConditions) > 0 {
			query = query.Where(strings.Join(orConditions, " OR "), args...)
			useCounter = false
		}
	}

	// 处理其他查询参数
	queryParams := c.Request.URL.Query()
	for key, values := range queryParams {
		if key == "page" || key == "page_size" || key == "order" || key == "search" {
			continue
		}
		if !utils.ExistsIn(allowedQueryFields, key) {
			continue
		}

		value := values[0]

		// 处理模糊查询和精确查询
		if strings.HasSuffix(key, "_contains") {
			field := strings.TrimSuffix(key, "_contains")
			query = query.Where(fmt.Sprintf("%s LIKE ?", field), "%"+value+"%")
		} else {
			query = query.Where(fmt.Sprintf("%s = ?", key), value)
		}
		useCounter = false
	}

	// 处理排序参数
	orderParam := c.DefaultQuery("order", "-id")
	if orderParam != "" && utils.ExistsIn(allowedOrderFields, orderParam) {
		// 判断是升序还是降序
		var orderType string
		var orderField string

		if strings.HasPrefix(orderParam, "-") {
			// 降序
			orderField = orderParam[1:]
			orderType = "DESC"
		} else {
			// 升序
			orderField = orderParam
			orderType = "ASC"
		}

		// 构建排序查询
		orderQuery := fmt.Sprintf("%s %s", orderField, orderType)
		query = query.Order(orderQuery)
	}

	// 大表统计直接从计数器表查询，如果查询失败则重新查询总数
	var total int64
	if useCounter {
		status := db.Raw("SELECT (counter) FROM counters WHERE name = ?", tableName).Scan(&total)
		if status.Error != nil {
			query.Count(&total)
		}
	} else {
		query.Count(&total)
	}

	// 执行分页查询
	err := query.Offset(offset).Limit(pageSize).Find(results.Addr().Interface()).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"data":      results.Interface(),
	})
}

// 通用资源创建
func genericCreate(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	// 获取模型类型和指针
	_, modelPtr, _ := utils.GetModelInfo(model)

	// 解析请求数据
	context, err := utils.UnbindContext(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}

	for i := 0; i < len(context); i++ {
		// 将 JSON 字节解析到模型指针
		if err := utils.BindContext(context[i], modelPtr); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to bind data to model"})
			return
		}

		// 创建记录
		if err := db.Create(modelPtr).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusCreated, modelPtr)
}

// 通用批量删除
func genericBatchDelete(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	var ids []int

	// 支持 JSON、Form 和 Query 参数
	switch c.ContentType() {
	case "application/json":
		// 解析 json 格式，形如 {"ids":[1, 2, 3, 4, 5, 6]}
		var body map[string]interface{}
		if err := c.ShouldBindJSON(&body); err != nil {
			break
		}

		idsInterface := body["ids"].([]interface{})
		ids = make([]int, len(idsInterface))
		for i, v := range idsInterface {
			ids[i] = int(v.(float64))
		}
	default:
		// 获取查询参数，形如 ?ids=1,2,3,4,5,6
		idParams := c.Query("ids")
		if idParams != "" {
			// 使用 strings.Split 将参数按逗号分隔
			idStrings := strings.Split(idParams, ",")

			// 转换为整数切片
			for _, idStr := range idStrings {
				id, err := strconv.Atoi(idStr) // 字符串转换为整数
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id format"})
					return
				}
				ids = append(ids, id)
			}
		} else {
			// 如果没有，解析 form 格式，形如 ids=[1,2,3,4,5,6]
			// gin默认不解析delete请求体，需要手动解析请求体中的表单数据
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(400, gin.H{"error": "failed to read body"})
				return
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				c.JSON(400, gin.H{"error": "failed to parse form"})
				return
			}
			idStrings := values.Get("ids")
			if idStrings == "" {
				break
			}
			err = json.Unmarshal([]byte(idStrings), &ids)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid obj format"})
				return
			}
		}
	}

	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ids format"})
		return
	}

	// 获取模型类型和指针
	_, modelPtr, _ := utils.GetModelInfo(model)

	// 批量删除
	result := db.Delete(modelPtr, ids)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("deleted %d", result.RowsAffected)})
}

// 通用单个资源获取
func genericRetrieve(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	id := c.Param("id")

	// 获取模型类型和指针
	_, modelPtr, _ := utils.GetModelInfo(model)

	result := db.First(modelPtr, id)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
		return
	}

	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}

	c.JSON(http.StatusOK, modelPtr)
}

// 通用单个资源删除
func genericDelete(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	id := c.Param("id")

	// 获取模型类型和指针
	_, modelPtr, _ := utils.GetModelInfo(model)

	// 设置ID
	result := db.Delete(modelPtr, id)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("deleted %d", result.RowsAffected)})
}

// 通用资源更新
func genericUpdate(c *gin.Context, model interface{}) {
	// 获取数据库实例（自动绑定到事务中）
	db := utils.GetDB(c, nil)

	// 使用反射检查字段标签，获取允许更新字段列表
	var allowedUpdateFields []string

	// 获取模型类型和指针
	modelType, modelPtr, _ := utils.GetModelInfo(model)

	for i := 0; i < modelType.NumField(); i++ {
		field := modelType.Field(i)
		tag := field.Tag.Get("ctags")
		if tag != "" {
			filedName := strings.Split(tag, ",")[0]
			filedTags := strings.Split(tag, ",")[1:]
			if filedName != "" && utils.ExistsIn(filedTags, "u") {
				allowedUpdateFields = append(allowedUpdateFields, filedName)
			}
		}
	}

	// 判断URL路径中是否包含ID，来区分是批量更新还是单一更新
	if urlPath := c.Param("id"); urlPath == "" {
		// 处理批量更新
		var objs []map[string]interface{}

		// 解析 json 格式，形如 {"objs":[{},{}]}
		if c.ContentType() == "application/json" {
			var requestBody struct {
				Objs []map[string]interface{} `json:"objs"`
			}
			if err := c.ShouldBindJSON(&requestBody); err == nil {
				objs = requestBody.Objs
			}
		} else {
			// 解析 form 格式，形如 objs=[{},{}]
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(400, gin.H{"error": "failed to read body"})
				return
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				c.JSON(400, gin.H{"error": "failed to parse form"})
				return
			}
			objStrings := values.Get("objs")
			err = json.Unmarshal([]byte(objStrings), &objs)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid obj format"})
				return
			}
		}

		if len(objs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid objs format"})
			return
		}

		// 执行批量更新
		for _, obj := range objs {
			id, exists := obj["id"]
			if !exists {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'id' in object"})
				return
			}

			// 仅允许更新特定字段
			filteredUpdates := make(map[string]interface{})
			for key, value := range obj {
				if utils.ExistsIn(allowedUpdateFields, key) {
					filteredUpdates[key] = value
				}
			}
			if len(filteredUpdates) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "no available fields to update"})
				return
			}

			if err := db.Model(modelPtr).Where("id = ?", id).Updates(filteredUpdates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "batch update successful"})
	} else {
		// 处理单一更新
		id := c.Param("id") // 获取路径中的 ID
		contexts, err := utils.UnbindContext(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		if len(contexts) != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		// 仅允许更新特定字段
		filteredUpdates := make(map[string]interface{})
		for key, value := range contexts[0] {
			if utils.ExistsIn(allowedUpdateFields, key) {
				filteredUpdates[key] = value
			}
		}
		if len(filteredUpdates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no available fields to update"})
			return
		}

		// 执行单一更新
		if err := db.Model(modelPtr).Where("id = ?", id).Updates(filteredUpdates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "single update successful"})
	}
}
