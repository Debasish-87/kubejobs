package redis

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var RDB *goredis.Client
var once sync.Once

// ---------------- INIT ----------------

func InitRedis() {

	addr := getEnv("REDIS_ADDR", "localhost:6379")
	password := getEnv("REDIS_PASSWORD", "")

	poolSize := getEnvInt("REDIS_POOL_SIZE", 50)

	RDB = goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,

		// timeouts
		DialTimeout:  3 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,

		// pool tuning
		PoolSize:        poolSize,
		MinIdleConns:    10,
		PoolTimeout:     3 * time.Second,
		ConnMaxIdleTime: 5 * time.Minute,
		ConnMaxLifetime: 30 * time.Minute,

		// retry strategy
		MaxRetries:      2,
		MinRetryBackoff: 50 * time.Millisecond,
		MaxRetryBackoff: 500 * time.Millisecond,
	})

	// FIX: retry logic instead of fatal crash
	var err error

	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)

		err = RDB.Ping(ctx).Err()
		cancel()

		if err == nil {
			break
		}

		log.Printf("event=redis_connect_retry attempt=%d addr=%s err=%v", i+1, addr, err)
		time.Sleep(2 * time.Second)
	}

	// only fail after retries exhausted
	if err != nil {
		log.Fatalf("event=redis_connection_failed addr=%s err=%v", addr, err)
	}

	log.Printf("event=redis_connected addr=%s pool=%d", addr, poolSize)

	startHealthCheck()
}

// ---------------- HEALTH CHECK ----------------

func startHealthCheck() {
	once.Do(func() {
		go healthCheckLoop()
	})
}

func healthCheckLoop() {

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		err := RDB.Ping(ctx).Err()
		cancel()

		if err != nil {
			log.Printf("event=redis_unhealthy err=%v", err)
		}
	}
}

// ---------------- SAFE CONTEXT ----------------

func Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// ---------------- CLOSE ----------------

func CloseRedis() {
	if RDB != nil {
		if err := RDB.Close(); err != nil {
			log.Println("event=redis_close_error err=", err)
		} else {
			log.Println("event=redis_closed")
		}
	}
}

// ---------------- HELPERS ----------------

func getEnv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("event=config_invalid_int key=%s val=%s", key, v)
		return def
	}
	return n
}
