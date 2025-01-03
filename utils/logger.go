package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LogConfig 日志配置结构体
type LogConfig struct {
	Level         string `mapstructure:"level"`         // 日志级别
	Directory     string `mapstructure:"directory"`     // 日志目录
	SeparateLevel bool   `mapstructure:"separateLevel"` // 是否按级别分割日志文件
	MaxSize       int    `mapstructure:"maxSize"`       // 单个日志文件最大大小，单位MB
	MaxBackups    int    `mapstructure:"maxBackups"`    // 最大保留的旧文件数量
	MaxAge        int    `mapstructure:"maxAge"`        // 旧文件保留天数
	Compress      bool   `mapstructure:"compress"`      // 是否压缩旧文件
	Console       bool   `mapstructure:"console"`       // 是否输出到控制台
	TraceID       string `mapstructure:"traceID"`       // 链路追踪ID字段名
}

// Logger 日志结构体
type Logger struct {
	config *LogConfig
	logger *zap.Logger
	sync.Once
}

// 默认配置
var defaultLogConfig = LogConfig{
	Level:         "info",
	Directory:     "logs",
	SeparateLevel: false,
	MaxSize:       100,
	MaxBackups:    30,
	MaxAge:        7,
	Compress:      true,
	Console:       true,
	TraceID:       "trace_id",
}

var (
	instanceLog *Logger
	onceLog     sync.Once
)

// GetLogger 获取日志实例，支持多种初始化方式
func GetLogger(args ...string) *Logger {
	onceLog.Do(func() {
		var config *LogConfig
		var err error

		switch len(args) {
		case 0:
			// 使用默认配置
			config = &defaultLogConfig
		case 1:
			// 使用配置文件，默认段
			config, err = loadLogConfig(args[0], "logger")
		case 2:
			// 使用配置文件，指定段
			config, err = loadLogConfig(args[0], args[1])
		default:
			panic("invalid parameters")
		}

		if err != nil {
			panic(fmt.Sprintf("failed to initialize log: %v", err))
		}

		instanceLog = &Logger{
			config: config,
		}
		if err := instanceLog.initLogger(); err != nil {
			panic(fmt.Sprintf("failed to initialize log: %v", err))
		}
	})
	return instanceLog
}

// loadLogConfig 加载配置文件
func loadLogConfig(configPath string, configSection string) (*LogConfig, error) {
	config := defaultLogConfig

	v := viper.New()
	v.SetConfigFile(configPath)

	// 根据文件扩展名设置配置类型
	ext := strings.ToLower(filepath.Ext(configPath))
	switch ext {
	case ".yaml", ".yml":
		v.SetConfigType("yaml")
	case ".json":
		v.SetConfigType("json")
	case ".env":
		v.SetConfigType("env")
	default:
		return nil, fmt.Errorf("unspported configuration file type: %s", ext)
	}

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

// initLogger 初始化日志对象
func (l *Logger) initLogger() error {
	// 创建日志目录
	if err := os.MkdirAll(l.config.Directory, 0755); err != nil {
		return fmt.Errorf("failed to make logs directory: %v", err)
	}

	// 配置编码器
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    "func",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     timeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 创建核心
	var cores []zapcore.Core

	// 文件输出
	if l.config.SeparateLevel {
		levels := []zapcore.Level{
			zapcore.DebugLevel,
			zapcore.InfoLevel,
			zapcore.WarnLevel,
			zapcore.ErrorLevel,
			zapcore.FatalLevel,
		}
		for _, level := range levels {
			core := l.createLevelCore(level, encoderConfig)
			cores = append(cores, core)
		}
	} else {
		core := l.createLevelCore(getLogLevel(l.config.Level), encoderConfig)
		cores = append(cores, core)
	}

	// 控制台输出
	if l.config.Console {
		consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
		consoleCore := zapcore.NewCore(
			consoleEncoder,
			zapcore.AddSync(os.Stdout),
			getLogLevel(l.config.Level),
		)
		cores = append(cores, consoleCore)
	}

	// 创建logger
	l.logger = zap.New(
		zapcore.NewTee(cores...),
		zap.AddCaller(),
		zap.AddCallerSkip(1),
	)

	return nil
}

// createLevelCore 创建特定级别的日志核心
func (l *Logger) createLevelCore(level zapcore.Level, encoderConfig zapcore.EncoderConfig) zapcore.Core {
	filename := l.getLogFileName(level)
	writer := &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    l.config.MaxSize,
		MaxBackups: l.config.MaxBackups,
		MaxAge:     l.config.MaxAge,
		Compress:   l.config.Compress,
	}

	return zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(writer),
		level,
	)
}

// getLogFileName 获取日志文件名
func (l *Logger) getLogFileName(level zapcore.Level) string {
	date := time.Now().Format("2006-01-02")
	if l.config.SeparateLevel {
		return filepath.Join(l.config.Directory, fmt.Sprintf("%s-%s.log", date, level.String()))
	}
	return filepath.Join(l.config.Directory, fmt.Sprintf("%s.log", date))
}

// timeEncoder 自定义时间编码器
func timeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
}

// getLogLevel 获取日志级别
func getLogLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

// 日志方法
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	fields = append(fields, getBaseFields()...)
	l.logger.Debug(msg, fields...)
}

func (l *Logger) Info(msg string, fields ...zap.Field) {
	fields = append(fields, getBaseFields()...)
	l.logger.Info(msg, fields...)
}

func (l *Logger) Warn(msg string, fields ...zap.Field) {
	fields = append(fields, getBaseFields()...)
	l.logger.Warn(msg, fields...)
}

func (l *Logger) Error(msg string, fields ...zap.Field) {
	fields = append(fields, getBaseFields()...)
	l.logger.Error(msg, fields...)
}

func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	fields = append(fields, getBaseFields()...)
	l.logger.Fatal(msg, fields...)
}

// WithTraceID 添加链路追踪ID
func (l *Logger) WithTraceID(traceID string) *zap.Logger {
	return l.logger.With(zap.String(l.config.TraceID, traceID))
}

// getBaseFields 获取基本字段信息
func getBaseFields() []zap.Field {
	pc, file, line, _ := runtime.Caller(2)
	function := runtime.FuncForPC(pc).Name()

	return []zap.Field{
		zap.Int("pid", os.Getpid()),                 // 进程ID
		zap.Uint64("tid", uint64(syscall.Gettid())), // 线程ID
		zap.String("file", file),
		zap.Int("line", line),
		zap.String("func", function),
		zap.Time("timestamp", time.Now()),
	}
}
