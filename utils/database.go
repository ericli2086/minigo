package utils

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DBUtils 包含常用数据库工具函数
type DBUtils struct {
	DB     *gorm.DB
	DBType string
}

// InitDB 初始化数据库连接并检测数据库类型
func InitDB(dsn string) *DBUtils {
	var db *gorm.DB
	var dbType string
	var err error

	if dbType, err = detectDatabaseType(dsn); err != nil {
		log.Fatalf("failed to detect database type: %v", err)
		return nil
	}

	switch dbType {
	case "mysql":
		// 尝试连接不同类型数据库
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err == nil {
			return &DBUtils{DB: db, DBType: dbType}
		}
	case "postgres":
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err == nil {
			return &DBUtils{DB: db, DBType: dbType}
		}
	case "sqlite":
		db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err == nil {
			return &DBUtils{DB: db, DBType: dbType}
		}
	default:
		log.Fatalf("unsupported database type: %s", dbType)
	}

	log.Fatalf("failed to connect to any database with dsn: %s", dsn)
	return nil
}

// CreateCounter4Table 为指定表创建触发计数器
func (u *DBUtils) CreateCounter4Table(tableName string) {
	sql := `
        CREATE TABLE counters (
            name VARCHAR(255) PRIMARY KEY,
            counter INT NOT NULL DEFAULT 0
        );
    `
	if err := u.DB.Exec(sql).Error; err == nil {
		switch u.DBType {
		case "mysql":
			u.createMySQLTriggers(tableName)
		case "postgres":
			u.createPostgresTriggers(tableName)
		case "sqlite":
			u.createSQLiteTriggers(tableName)
		default:
			log.Fatalf("unsupported database type: %s", u.DBType)
		}
	}
}

// createMySQLTriggers 为 MySQL 创建触发器
func (u *DBUtils) createMySQLTriggers(tableName string) {
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

	if err := u.DB.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create mysql triggers for table %s: %v", tableName, err)
	}
}

// createPostgresTriggers 为 PostgreSQL 创建触发器
func (u *DBUtils) createPostgresTriggers(tableName string) {
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

	if err := u.DB.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create postgresql triggers for table %s: %v", tableName, err)
	}
}

// createSQLiteTriggers 为 SQLite 创建触发器
func (u *DBUtils) createSQLiteTriggers(tableName string) {
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

	if err := u.DB.Exec(triggerSQL).Error; err != nil {
		log.Fatalf("failed to create sqlite triggers for table %s: %v", tableName, err)
	}
}

// detectDatabaseType 根据 DSN 判断数据库类型
func detectDatabaseType(dsn string) (string, error) {
	if strings.Contains(dsn, "mysql") || strings.Contains(dsn, "@tcp(") {
		return "mysql", nil
	} else if strings.Contains(dsn, "host=") && strings.Contains(dsn, "user=") && strings.Contains(dsn, "dbname=") {
		return "postgres", nil
	} else if strings.HasSuffix(dsn, ".db") || strings.HasSuffix(dsn, ".sqlite") || strings.Contains(dsn, "sqlite") {
		return "sqlite", nil
	}
	return "", errors.New("unsupported or unknown database type")
}
