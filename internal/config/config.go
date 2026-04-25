package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// ---------------- STRUCT ----------------

type Config struct {
	AppMode  string
	WorkerID string

	MaxRetry        int
	MaxQueueSize    int
	RecoveryTimeout time.Duration
}

// singleton instance
var (
	C    *Config
	once sync.Once
)

func ParseInt(val string) int64 {
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ---------------- INIT ----------------

func Init() {

	once.Do(func() {

		cfg := &Config{
			AppMode: getEnv("APP_MODE", "api"),

			WorkerID: getEnv("WORKER_ID",
				fmt.Sprintf("worker-%d", time.Now().UnixNano()),
			),

			MaxRetry:     mustGetEnvInt("MAX_RETRY", 3),
			MaxQueueSize: mustGetEnvInt("MAX_QUEUE_SIZE", 1000),

			RecoveryTimeout: time.Duration(
				mustGetEnvInt("RECOVERY_TIMEOUT", 40),
			) * time.Second,
		}

		validate(cfg)

		C = cfg

		logConfig(cfg) // visibility
	})
}

// ---------------- VALIDATION ----------------

func validate(c *Config) {

	if c.MaxRetry < 0 {
		fatal("MAX_RETRY cannot be negative")
	}

	if c.MaxQueueSize <= 0 {
		fatal("MAX_QUEUE_SIZE must be > 0")
	}

	if c.RecoveryTimeout <= 0 {
		fatal("RECOVERY_TIMEOUT must be > 0")
	}

	if c.AppMode != "api" && c.AppMode != "worker" {
		fatal("APP_MODE must be api or worker")
	}
}

// ---------------- LOGGING ----------------

func logConfig(c *Config) {
	log.Printf(
		"event=config_loaded mode=%s worker=%s max_retry=%d max_queue=%d recovery_timeout=%s",
		c.AppMode,
		c.WorkerID,
		c.MaxRetry,
		c.MaxQueueSize,
		c.RecoveryTimeout.String(),
	)
}

// ---------------- HELPERS ----------------

func getEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func mustGetEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		fatal(fmt.Sprintf("invalid %s=%s", key, v))
	}

	return n
}

// ---------------- FAIL FAST ----------------

func fatal(msg string) {
	log.Printf("event=config_error msg=%s", msg)
	os.Exit(1)
}
