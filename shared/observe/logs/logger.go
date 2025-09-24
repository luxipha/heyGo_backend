package logs

import (
	"fmt"
	"os"
	"sync"

	"github.com/cprakhar/uber-clone/shared/env"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config represents runtime logging configuration. Values are typically
// sourced from environment variables so behavior can be tuned per deployment
// without code changes.
type Config struct {
	// Format is either "json" (production) or "console" (human-friendly dev).
	Format string
	// Rotate enables file rotation via lumberjack logger when LogFile != "".
	Rotate bool
	// LogFile path to write logs (in addition to stdout). If empty, only stdout.
	LogFile string
	// MaxSize MB size of a log file before rotation.
	MaxSize int
	// MaxBackups backup files
	MaxBackups int
	// MaxAge days for rotation (when Rotate is true).
	MaxAge int
	// Compress rotated files.
	Compress bool
}

// globalSugar is the package-global logger. It is nil until Init is called.
var (
	globalSugar *zap.SugaredLogger
	mu          sync.RWMutex
)

// loadConfig builds Config from environment with sensible defaults.
func loadConfig(serviceName string) Config {
	return Config{
		Format:     env.GetString("LOG_FORMAT", "json"),
		Rotate:     env.GetBool("LOG_ROTATE", true),
		LogFile:    fmt.Sprintf("/var/log/%s.log", serviceName),
		MaxSize:    env.GetInt("LOG_MAX_SIZE_MB", 50),
		MaxBackups: env.GetInt("LOG_MAX_BACKUPS", 5),
		MaxAge:     env.GetInt("LOG_MAX_AGE_DAYS", 30),
		Compress:   env.GetBool("LOG_COMPRESS", true),
	}
}

// Init initializes the global logger. It is safe to call multiple times; the
// last call replaces the global logger (closing the previous one).
func Init(serviceName string) (*zap.SugaredLogger, error) {
	cfg := loadConfig(serviceName)
	atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
	encoderCfg := zap.NewProductionEncoderConfig()

	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.TimeKey = "ts"
	encoderCfg.CallerKey = "caller"
	encoderCfg.MessageKey = "msg"
	encoderCfg.LevelKey = "level"
	encoderCfg.EncodeLevel = zapcore.LowercaseLevelEncoder

	var encoder zapcore.Encoder
	if cfg.Format == "console" {
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	syncers := []zapcore.WriteSyncer{zapcore.AddSync(os.Stdout)}
	if cfg.LogFile != "" {
		if cfg.Rotate {
			lj := &lumberjack.Logger{
				Filename:   cfg.LogFile,
				MaxSize:    cfg.MaxSize,
				MaxBackups: cfg.MaxBackups,
				MaxAge:     cfg.MaxAge,
				Compress:   cfg.Compress,
			}
			syncers = append(syncers, zapcore.AddSync(lj))
		} else {
			f, err := os.OpenFile(cfg.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				syncers = append(syncers, zapcore.AddSync(f))
			}
		}
	}

	core := zapcore.NewCore(encoder, zapcore.NewMultiWriteSyncer(syncers...), atomicLevel)

	opts := []zap.Option{}
	opts = append(opts, zap.AddCaller())
	opts = append(opts, zap.AddStacktrace(zap.ErrorLevel))
	version := env.GetString("VERSION", "1.0.0")
	opts = append(opts, zap.Fields(zap.String("service", serviceName), zap.String("version", version)))

	logger := zap.New(core, opts...)
	sugar := logger.Sugar()

	mu.Lock()
	if globalSugar != nil {
		_ = globalSugar.Sync()
	}
	globalSugar = sugar
	mu.Unlock()

	return sugar, nil
}

// L returns the global sugared logger. Panics if Init not called.
func L() *zap.SugaredLogger {
	mu.RLock()
	l := globalSugar
	mu.RUnlock()
	if l == nil {
		panic("logger not initialized: call logs.Init() early in main")
	}
	return l
}

// Sync flushes any buffered log entries.
func Sync() error {
	mu.RLock()
	l := globalSugar
	mu.RUnlock()
	if l == nil {
		return fmt.Errorf("logger not initialized")
	}
	return l.Sync()
}
