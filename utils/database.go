package utils

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// DBType 数据库类型
type DBType string

const (
	MySQL      DBType = "mysql"
	MariaDB    DBType = "mariadb"
	TiDB       DBType = "tidb"
	PostgreSQL DBType = "postgresql"
	SQLite     DBType = "sqlite"
)

// DBConfig 数据库配置结构体
type DBConfig struct {
	Type            DBType        `mapstructure:"type"`            // 数据库类型
	Host            string        `mapstructure:"host"`            // 主机地址
	Port            int           `mapstructure:"port"`            // 端口
	Username        string        `mapstructure:"username"`        // 用户名
	Password        string        `mapstructure:"password"`        // 密码
	Database        string        `mapstructure:"database"`        // 数据库名
	Charset         string        `mapstructure:"charset"`         // 字符集
	MaxIdleConns    int           `mapstructure:"maxIdleConns"`    // 最大空闲连接数
	MaxOpenConns    int           `mapstructure:"maxOpenConns"`    // 最大打开连接数
	ConnMaxLifetime int           `mapstructure:"connMaxLifetime"` // 连接最大生命周期（秒）
	ConnMaxIdleTime int           `mapstructure:"connMaxIdleTime"` // 空闲连接最大生命周期（秒）
	SingularTable   bool          `mapstructure:"singularTable"`   // 是否使用单数表名
	TablePrefix     string        `mapstructure:"tablePrefix"`     // 表前缀
	SlowThreshold   int           `mapstructure:"slowThreshold"`   // 慢查询阈值（毫秒）
	LogLevel        string        `mapstructure:"logLevel"`        // 日志级别
	SQLite          *SQLiteConfig `mapstructure:"sqlite"`          // SQLite特定配置
}

// SQLiteConfig SQLite特定配置
type SQLiteConfig struct {
	File string `mapstructure:"file"` // 数据库文件路径
}

// Database 数据库结构体
type Database struct {
	*gorm.DB
	config *DBConfig
	dsn    string
	logger *Logger
	sync.Once
}

// 默认配置
var defaultDBConfig = DBConfig{
	Type:            MySQL,
	Host:            "localhost",
	Port:            3306,
	Username:        "root",
	Password:        "",
	Database:        "test",
	Charset:         "utf8mb4",
	MaxIdleConns:    10,
	MaxOpenConns:    100,
	ConnMaxLifetime: 3600,
	ConnMaxIdleTime: 1800,
	SingularTable:   false,
	SlowThreshold:   200,
	LogLevel:        "info",
	SQLite: &SQLiteConfig{
		File: "data.db",
	},
}

var (
	instanceDB *Database
	instances  = make(map[string]*Database)
	muDB       sync.RWMutex
)

// GetDB 获取数据库实例
func GetDB(args ...string) *Database {
	key := strings.Join(args, ":")

	muDB.RLock()
	if db, exists := instances[key]; exists {
		muDB.RUnlock()
		return db
	}
	muDB.RUnlock()

	muDB.Lock()
	defer muDB.Unlock()

	// 双重检查
	if db, exists := instances[key]; exists {
		return db
	}

	var config *DBConfig
	var err error
	var dsn string

	switch len(args) {
	case 1:
		// 使用默认配置 + DSN
		config = &defaultDBConfig
		dsn = args[0]
		if strings.Contains(dsn, "mysql") || strings.Contains(dsn, "@tcp(") {
			config.Type = MySQL
		} else if strings.Contains(dsn, "host=") && strings.Contains(dsn, "user=") && strings.Contains(dsn, "dbname=") {
			config.Type = PostgreSQL
		} else if strings.HasSuffix(dsn, ".db") || strings.HasSuffix(dsn, ".sqlite") || strings.Contains(dsn, "sqlite") {
			config.Type = SQLite
		} else {
			panic("unsupported database type")
		}
	case 2:
		// 使用配置文件，默认段
		config, err = loadDBConfig(args[0], "database")
		if err != nil {
			panic(fmt.Sprintf("failed to initialize database: %v", err))
		}
	case 3:
		// 使用配置文件，指定段
		config, err = loadDBConfig(args[0], args[1])
		if err != nil {
			panic(fmt.Sprintf("failed to initialize database: %v", err))
		}
	default:
		panic("invalid parameters: GetDB(dsn) or GetDB(configFile, section)")
	}

	db := &Database{
		config: config,
		dsn:    dsn,
	}
	if err := db.initDB(); err != nil {
		panic(fmt.Sprintf("failed to initialize database: %v", err))
	}

	instances[key] = db
	if instanceDB == nil {
		instanceDB = db
	}
	return db
}

// SetLogger 设置自定义logger
func (d *Database) SetLogger(logger *Logger) *Database {
	if logger != nil {
		d.logger = logger
		gormLogger := NewCustomGormLogger(
			logger,
			time.Duration(d.config.SlowThreshold)*time.Millisecond,
			getGormLogLevel(d.config.LogLevel),
		)
		d.DB.Logger = gormLogger
	}
	return d
}

// loadDBConfig 加载数据库配置
func loadDBConfig(configPath, configSection string) (*DBConfig, error) {
	config := defaultDBConfig

	v := viper.New()
	v.SetConfigFile(configPath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read configuration file: %v", err)
	}

	// 读取指定配置段
	if configSection != "" {
		subConfig := v.Sub(configSection)
		if subConfig == nil {
			return nil, fmt.Errorf("configuration section does not exist: %s", configSection)
		}
		if err := subConfig.Unmarshal(&config); err != nil {
			return nil, fmt.Errorf("failed to parse configuration file: %v", err)
		}
	} else {
		if err := v.Unmarshal(&config); err != nil {
			return nil, fmt.Errorf("failed to parse configuration file: %v", err)
		}
	}

	return &config, nil
}

// CustomGormLogger GORM日志适配器
type CustomGormLogger struct {
	logger        *Logger
	SlowThreshold time.Duration
	LogLevel      logger.LogLevel
}

// NewCustomGormLogger 创建GORM日志适配器
func NewCustomGormLogger(logger *Logger, slowThreshold time.Duration, level logger.LogLevel) logger.Interface {
	if logger == nil {
		return nil
	}
	return &CustomGormLogger{
		logger:        logger,
		SlowThreshold: slowThreshold,
		LogLevel:      level,
	}
}

// LogMode 实现 logger.Interface
func (l *CustomGormLogger) LogMode(level logger.LogLevel) logger.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

// Info 实现 logger.Interface
func (l *CustomGormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Info {
		l.logger.Info(msg, zap.Any("data", data))
	}
}

// Warn 实现 logger.Interface
func (l *CustomGormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Warn {
		l.logger.Warn(msg, zap.Any("data", data))
	}
}

// Error 实现 logger.Interface
func (l *CustomGormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= logger.Error {
		l.logger.Error(msg, zap.Any("data", data))
	}
}

// Trace 实现 logger.Interface
func (l *CustomGormLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel <= logger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()
	fields := []zap.Field{
		zap.Duration("elapsed", elapsed),
		zap.String("sql", sql),
		zap.Int64("rows", rows),
	}

	// 处理错误
	if err != nil {
		fields = append(fields, zap.Error(err))
		l.logger.Error("Error SQL", fields...)
		return
	}

	// 处理慢查询
	if l.SlowThreshold != 0 && elapsed > l.SlowThreshold {
		l.logger.Warn("Slow SQL", fields...)
		return
	}

	// 记录普通查询
	if l.LogLevel >= logger.Info {
		l.logger.Info("SQL", fields...)
	}
}

// getGormLogLevel 获取GORM日志级别
func getGormLogLevel(level string) logger.LogLevel {
	switch level {
	case "silent":
		return logger.Silent
	case "error":
		return logger.Error
	case "warn":
		return logger.Warn
	case "info":
		return logger.Info
	default:
		return logger.Info
	}
}

// initDB 初始化数据库连接
func (d *Database) initDB() error {
	var dialector gorm.Dialector

	switch d.config.Type {
	case MySQL, MariaDB, TiDB:
		if d.dsn != "" {
			dialector = mysql.Open(d.dsn)
		} else {
			dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local",
				d.config.Username,
				d.config.Password,
				d.config.Host,
				d.config.Port,
				d.config.Database,
				d.config.Charset,
			)
			dialector = mysql.Open(dsn)
		}

	case PostgreSQL:
		if d.dsn != "" {
			dialector = postgres.Open(d.dsn)
		} else {
			dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai",
				d.config.Host,
				d.config.Port,
				d.config.Username,
				d.config.Password,
				d.config.Database,
			)
			dialector = postgres.Open(dsn)
		}

	case SQLite:
		if d.dsn != "" {
			dialector = sqlite.Open(d.dsn)
		} else {
			dialector = sqlite.Open(d.config.SQLite.File)
		}

	default:
		return fmt.Errorf("unspported database type: %s", d.config.Type)
	}

	gormConfig := &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: d.config.SingularTable,
			TablePrefix:   d.config.TablePrefix,
		},
		Logger: logger.Default.LogMode(getGormLogLevel(d.config.LogLevel)),
	}

	db, err := gorm.Open(dialector, gormConfig)
	if err != nil {
		return fmt.Errorf("failed to connect database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to connect database: %v", err)
	}

	// 设置连接池
	sqlDB.SetMaxIdleConns(d.config.MaxIdleConns)
	sqlDB.SetMaxOpenConns(d.config.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(d.config.ConnMaxLifetime) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(d.config.ConnMaxIdleTime) * time.Second)

	d.DB = db
	return nil
}

// Stats 获取连接池统计信息
func (d *Database) Stats() interface{} {
	if d.DB != nil {
		sqlDB, err := d.DB.DB()
		if err != nil {
			return nil
		}
		return sqlDB.Stats()
	}
	return nil
}

// Close 关闭数据库连接
func (d *Database) Close() error {
	if d.DB != nil {
		sqlDB, err := d.DB.DB()
		if err != nil {
			return fmt.Errorf("failed to connect database: %v", err)
		}
		return sqlDB.Close()
	}
	return nil
}

// Transaction 事务封装
func Transaction(db *gorm.DB, fc func(tx *gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		return fc(tx)
	})
}

// CreateCounter4Table 为指定表创建触发计数器
func CreateCounter4Table(db *Database, tableName string) {
	sql := `
        CREATE TABLE counters (
            name VARCHAR(255) PRIMARY KEY,
            counter INT NOT NULL DEFAULT 0
        );
    `
	if err := db.DB.Exec(sql).Error; err == nil {
		switch db.config.Type {
		case MySQL, MariaDB, TiDB:
			createMySQLTriggers(db.DB, tableName)
		case PostgreSQL:
			createPostgresTriggers(db.DB, tableName)
		case SQLite:
			createSQLiteTriggers(db.DB, tableName)
		default:
			log.Fatalf("unsupported database type: %s", db.config.Type)
		}
	}
}

// createMySQLTriggers 为 MySQL 创建触发器
func createMySQLTriggers(db *gorm.DB, tableName string) {
	triggerSQL := fmt.Sprintf(`
        -- 初始插入数据
        DELETE FROM counters WHERE name = '%s';
        INSERT INTO counters (name, counter) VALUES ('%s', (SELECT COUNT(*) FROM %s WHERE deleted_at = 0));

        -- 删除旧的触发器
        DROP TRIGGER IF EXISTS after_%s_insert;
        DROP TRIGGER IF EXISTS after_%s_update;
        DROP TRIGGER IF EXISTS after_%s_update_restore;
        
        -- 插入触发器
        CREATE TRIGGER after_%s_insert 
        AFTER INSERT ON %s
        FOR EACH ROW
        BEGIN
            IF NEW.deleted_at = 0 THEN
                UPDATE counters SET counter = counter + 1 WHERE name = '%s';
            END IF;
        END;

        -- 软删除触发器
        CREATE TRIGGER after_%s_update 
        AFTER UPDATE ON %s
        FOR EACH ROW
        BEGIN
            IF OLD.deleted_at = 0 AND NEW.deleted_at != 0 THEN
                UPDATE counters SET counter = counter - 1 WHERE name = '%s';
            END IF;
        END;

        -- 恢复触发器
        CREATE TRIGGER after_%s_update_restore
        AFTER UPDATE ON %s
        FOR EACH ROW
        BEGIN
            IF OLD.deleted_at != 0 AND NEW.deleted_at = 0 THEN
                UPDATE counters SET counter = counter + 1 WHERE name = '%s';
            END IF;
        END;
    `,
		// 初始数据的参数
		tableName, tableName, tableName,
		// 插入触发器的参数
		tableName, tableName, tableName,
		// 软删除触发器的参数
		tableName, tableName, tableName,
		// 更新触发器的参数
		tableName, tableName, tableName,
		// 恢复触发器的参数
		tableName, tableName, tableName)

	if err := db.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create mysql triggers for table %s: %v", tableName, err)
	}
}

// createPostgresTriggers 为 PostgreSQL 创建触发器
func createPostgresTriggers(db *gorm.DB, tableName string) {
	triggerSQL := fmt.Sprintf(`
        -- 初始插入数据
        DELETE FROM counters WHERE name = '%s';
        INSERT INTO counters (name, counter) VALUES ('%s', (SELECT COUNT(*) FROM %s WHERE deleted_at = 0));

        -- 清理旧的触发器和函数
        DROP TRIGGER IF EXISTS after_%s_insert ON %s;
        DROP TRIGGER IF EXISTS after_%s_update ON %s;
        DROP TRIGGER IF EXISTS after_%s_update_restore ON %s;
        
        DROP FUNCTION IF EXISTS fn_after_%s_insert();
        DROP FUNCTION IF EXISTS fn_after_%s_update();
        DROP FUNCTION IF EXISTS fn_after_%s_update_restore();

        -- 创建插入触发器函数和触发器
        CREATE OR REPLACE FUNCTION fn_after_%s_insert()
        RETURNS TRIGGER AS $$
        BEGIN
            IF NEW.deleted_at = 0 THEN
                UPDATE counters SET counter = counter + 1 WHERE name = '%s';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;

        CREATE TRIGGER after_%s_insert
            AFTER INSERT ON %s
            FOR EACH ROW
            EXECUTE FUNCTION fn_after_%s_insert();

        -- 创建更新触发器函数和触发器
        CREATE OR REPLACE FUNCTION fn_after_%s_update()
        RETURNS TRIGGER AS $$
        BEGIN
            IF OLD.deleted_at = 0 AND NEW.deleted_at != 0 THEN
                UPDATE counters SET counter = counter - 1 WHERE name = '%s';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;

        CREATE TRIGGER after_%s_update
            AFTER UPDATE ON %s
            FOR EACH ROW
            EXECUTE FUNCTION fn_after_%s_update();

        -- 创建恢复触发器函数和触发器
        CREATE OR REPLACE FUNCTION fn_after_%s_update_restore()
        RETURNS TRIGGER AS $$
        BEGIN
            IF OLD.deleted_at != 0 AND NEW.deleted_at = 0 THEN
                UPDATE counters SET counter = counter + 1 WHERE name = '%s';
            END IF;
            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;

        CREATE TRIGGER after_%s_update_restore
            AFTER UPDATE ON %s
            FOR EACH ROW
            EXECUTE FUNCTION fn_after_%s_update_restore();
    `,
		// 初始数据的参数
		tableName, tableName, tableName,
		// 删除旧触发器的参数
		tableName, tableName, tableName, tableName, tableName, tableName,
		// 删除旧函数的参数
		tableName, tableName, tableName,
		// 插入触发器的参数
		tableName, tableName,
		tableName, tableName, tableName,
		// 更新触发器的参数
		tableName, tableName,
		tableName, tableName, tableName,
		// 恢复触发器的参数
		tableName, tableName,
		tableName, tableName, tableName)

	if err := db.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create postgresql triggers for table %s: %v", tableName, err)
	}
}

// createSQLiteTriggers 为 SQLite 创建触发器
func createSQLiteTriggers(db *gorm.DB, tableName string) {
	triggerSQL := fmt.Sprintf(`
        -- 初始插入数据
        DELETE FROM counters WHERE name = '%s';
        INSERT INTO counters (name, counter) VALUES ('%s', (SELECT COUNT(*) FROM %s WHERE deleted_at = 0));

        -- 清理旧的触发器
        DROP TRIGGER IF EXISTS after_%s_insert;
        DROP TRIGGER IF EXISTS after_%s_update;
        DROP TRIGGER IF EXISTS after_%s_update_restore;

        -- 创建触发器维护计数
        CREATE TRIGGER after_%s_insert AFTER INSERT ON %s
        BEGIN
            UPDATE counters SET counter = counter + 1 WHERE name = '%s';
        END;

        CREATE TRIGGER after_%s_update AFTER UPDATE ON %s
        WHEN OLD.deleted_at = 0 AND NEW.deleted_at != 0
        BEGIN
            UPDATE counters SET counter = counter - 1 WHERE name = '%s';
        END;

        CREATE TRIGGER after_%s_update_restore AFTER UPDATE ON %s
        WHEN OLD.deleted_at != 0 AND NEW.deleted_at = 0
        BEGIN
            UPDATE counters SET counter = counter + 1 WHERE name = '%s';
        END;
    `, tableName, tableName, tableName, tableName, tableName, tableName, tableName,
		tableName, tableName, tableName, tableName, tableName, tableName, tableName, tableName)

	if err := db.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create sqlite triggers for table %s: %v", tableName, err)
	}
}
