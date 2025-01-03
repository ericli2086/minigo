package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetDbByCtx 获取当前上下文中的事务或全局数据库实例
func GetDbByCtx(c *gin.Context) *gorm.DB {
	var db *gorm.DB

	tx, exists := c.Get("tx")
	if exists {
		db = tx.(*gorm.DB)
	}
	return db
}

// UnbindContext 解析请求体内容到 []map[string]interface{}
func UnbindContext(c *gin.Context) ([]map[string]interface{}, error) {
	results := make([]map[string]interface{}, 0)

	// 读取请求体内容
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %v", err)
	}

	// 重要：重新设置请求体，因为ReadAll会消耗body
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	// 获取 Content-Type
	contentType := c.GetHeader("Content-Type")

	// 如果是 JSON 格式
	if strings.HasPrefix(contentType, "application/json") {
		var result interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to parse json body: %v", err)
		}

		// 判断是否是 map 或 slice
		switch v := result.(type) {
		case map[string]interface{}:
			results = append(results, v)
		case []interface{}:
			for _, item := range v {
				if mapItem, ok := item.(map[string]interface{}); ok {
					results = append(results, mapItem)
				} else {
					return nil, fmt.Errorf("json array contains non-object element")
				}
			}
		default:
			return nil, fmt.Errorf("unexpected json type: %T", v)
		}
	} else if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(contentType, "multipart/form-data") {
		// 对于 multipart/form-data，应该使用 ParseMultipartForm
		if strings.HasPrefix(contentType, "multipart/form-data") {
			if err := c.Request.ParseMultipartForm(32 << 20); err != nil { // 32MB max
				return nil, fmt.Errorf("failed to parse multipart form: %v", err)
			}
		} else {
			if err := c.Request.ParseForm(); err != nil {
				return nil, fmt.Errorf("failed to parse form: %v", err)
			}
		}

		// 创建新的map存储表单数据
		formData := make(map[string]interface{})

		// 获取表单数据
		form := c.Request.Form
		for key, values := range form {
			if len(values) == 1 {
				formData[key] = values[0]
			} else {
				formData[key] = values
			}
		}

		// 处理文件上传（如果有）
		if c.Request.MultipartForm != nil && c.Request.MultipartForm.File != nil {
			for key, files := range c.Request.MultipartForm.File {
				fileNames := make([]string, len(files))
				for i, file := range files {
					fileNames[i] = file.Filename
				}
				if len(fileNames) == 1 {
					formData[key] = fileNames[0]
				} else {
					formData[key] = fileNames
				}
			}
		}

		results = append(results, formData)
	} else {
		return nil, fmt.Errorf("unsupported Content-Type: %s", contentType)
	}

	return results, nil
}

// BindContext 将 map[string]interface{} 数据绑定到结构体
func BindContext(data map[string]interface{}, v interface{}) error {
	// 获取指针指向的值
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("invalid target type, expected ptr, got %v", rv.Kind())
	}

	// 获取结构体的值和类型
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("invalid target type, expected struct, got %v", rv.Kind())
	}

	// 遍历结构体字段
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		fieldValue := rv.Field(i)

		// 跳过未导出的字段
		if !fieldValue.CanSet() {
			continue
		}

		// 获取字段名（转为小写用于匹配）
		fieldName := strings.ToLower(field.Name)

		// 查找对应的数据
		if value, exists := data[fieldName]; exists && value != nil {
			if err := setValue(fieldValue, value); err != nil {
				return fmt.Errorf("failed to set field %s: %v", field.Name, err)
			}
		}
	}

	return nil
}

// GetModelInfo 获取模型类型，指针，表名
func GetModelInfo(model interface{}) (reflect.Type, interface{}, string) {
	modelType := reflect.TypeOf(model)

	// 检查是否为指针类型
	if reflect.ValueOf(model).Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}

	// 创建新的实例
	modelPtr := reflect.New(modelType).Interface()

	// 获取数据库表名
	tableName := ""
	// 1. 先尝试从 modelPtr 中获取表名
	if tabler, ok := modelPtr.(interface{ TableName() string }); ok {
		tableName = tabler.TableName()
	} else {
		// 2. 如果 modelPtr 没有实现 TableName 方法，从 modelType 获取
		typeName := modelType.Name()
		if modelType.Kind() == reflect.Ptr {
			typeName = modelType.Elem().Name()
		}
		// 转换为蛇形命名
		runes := []rune(typeName)
		for i := 0; i < len(runes); i++ {
			if i > 0 && unicode.IsUpper(runes[i]) {
				tableName += "_"
			}
			tableName += strings.ToLower(string(runes[i]))
		}
		// 如果有数据库表配置，则添加前缀和后缀。
		for _, instanceDB := range instanceDbs {
			var skip = false
			if instanceDB != nil && !instanceDB.config.SingularTable {
				tableName += "s"
				skip = true
			}
			if instanceDB != nil && instanceDB.config.TablePrefix != "" {
				tableName = instanceDB.config.TablePrefix + tableName
				skip = true
			}
			if skip {
				break
			}
		}
	}

	return modelType, modelPtr, tableName
}

// Camel2Snake 驼峰转蛇形
func Camel2Snake(input string) string {
	var result []rune
	for i, r := range input {
		if unicode.IsUpper(r) && i > 0 {
			result = append(result, '_')
			result = append(result, unicode.ToLower(r))
		} else {
			result = append(result, unicode.ToLower(r))
		}
	}
	return string(result)
}

// ExistsIn 测试切片是否包含某个元素
func ExistsIn[T comparable](slice []T, item T) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// 类型转换辅助函数
func ToInt64(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int8:
		return int64(val), true
	case int16:
		return int64(val), true
	case int32:
		return int64(val), true
	case int64:
		return val, true
	case uint:
		return int64(val), true
	case uint8:
		return int64(val), true
	case uint16:
		return int64(val), true
	case uint32:
		return int64(val), true
	case uint64:
		return int64(val), true
	case float32:
		return int64(val), true
	case float64:
		return int64(val), true
	case string:
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func ToUint64(v interface{}) (uint64, bool) {
	switch val := v.(type) {
	case uint:
		return uint64(val), true
	case uint8:
		return uint64(val), true
	case uint16:
		return uint64(val), true
	case uint32:
		return uint64(val), true
	case uint64:
		return val, true
	case int:
		if val < 0 {
			return 0, false
		}
		return uint64(val), true
	case float64:
		if val < 0 {
			return 0, false
		}
		return uint64(val), true
	case string:
		if i, err := strconv.ParseUint(val, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func ToFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float32:
		return float64(val), true
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint64:
		return float64(val), true
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func ToBool(v interface{}) (bool, bool) {
	switch val := v.(type) {
	case bool:
		return val, true
	case int:
		return val != 0, true
	case string:
		if b, err := strconv.ParseBool(val); err == nil {
			return b, true
		}
	}
	return false, false
}

// setValue 设置字段值
func setValue(field reflect.Value, value interface{}) error {
	val := reflect.ValueOf(value)

	// 处理指针类型
	if field.Kind() == reflect.Ptr {
		if val.Kind() != reflect.Ptr {
			// 如果值不是指针，创建一个新的指针
			ptr := reflect.New(field.Type().Elem())
			if err := setValue(ptr.Elem(), value); err != nil {
				return err
			}
			field.Set(ptr)
			return nil
		}
		if val.IsNil() {
			field.Set(reflect.Zero(field.Type()))
			return nil
		}
		field = field.Elem()
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(fmt.Sprint(value))

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, ok := ToInt64(value)
		if !ok {
			return fmt.Errorf("cannot convert %v to int64", value)
		}
		field.SetInt(v)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, ok := ToUint64(value)
		if !ok {
			return fmt.Errorf("cannot convert %v to uint64", value)
		}
		field.SetUint(v)

	case reflect.Float32, reflect.Float64:
		v, ok := ToFloat64(value)
		if !ok {
			return fmt.Errorf("cannot convert %v to float64", value)
		}
		field.SetFloat(v)

	case reflect.Bool:
		v, ok := ToBool(value)
		if !ok {
			return fmt.Errorf("cannot convert %v to bool", value)
		}
		field.SetBool(v)

	case reflect.Slice:
		return setSlice(field, value)

	case reflect.Map:
		return setMap(field, value)

	case reflect.Struct:
		if m, ok := value.(map[string]interface{}); ok {
			return BindContext(m, field.Addr().Interface())
		}
		return fmt.Errorf("cannot convert %v to struct", value)

	default:
		return fmt.Errorf("unsupported type: %v", field.Kind())
	}

	return nil
}

func setSlice(field reflect.Value, value interface{}) error {
	val := reflect.ValueOf(value)
	if val.Kind() != reflect.Slice && val.Kind() != reflect.Array {
		return fmt.Errorf("cannot convert %v to slice", value)
	}

	slice := reflect.MakeSlice(field.Type(), val.Len(), val.Len())
	for i := 0; i < val.Len(); i++ {
		if err := setValue(slice.Index(i), val.Index(i).Interface()); err != nil {
			return err
		}
	}
	field.Set(slice)
	return nil
}

func setMap(field reflect.Value, value interface{}) error {
	val := reflect.ValueOf(value)
	if val.Kind() != reflect.Map {
		return fmt.Errorf("cannot convert %v to map", value)
	}

	mapType := field.Type()
	newMap := reflect.MakeMap(mapType)

	iter := val.MapRange()
	for iter.Next() {
		key := iter.Key()
		mapValue := iter.Value()

		newKey := reflect.New(mapType.Key()).Elem()
		if err := setValue(newKey, key.Interface()); err != nil {
			return err
		}

		newVal := reflect.New(mapType.Elem()).Elem()
		if err := setValue(newVal, mapValue.Interface()); err != nil {
			return err
		}

		newMap.SetMapIndex(newKey, newVal)
	}

	field.Set(newMap)
	return nil
}
