package cmdutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/adjust/rmq/v5"
	"github.com/go-redis/redis/extra/redisotel/v8"
	"github.com/go-redis/redis/v8"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sideshow/apns2/token"

	"go.uber.org/zap"
)

func NewLogger(service string) *zap.Logger {
	env := os.Getenv("ENV")
	logger, _ := zap.NewProduction(zap.Fields(
		zap.String("env", env),
		zap.String("service", service),
	))

	if env == "" || env == "development" {
		logger, _ = zap.NewDevelopment()
	}

	return logger
}

// LoadAPNS reads the four APNs env vars (APPLE_KEY_PATH, APPLE_KEY_ID,
// APPLE_TEAM_ID, APPLE_APNS_TOPIC) and has three outcomes:
//
//   - all four set: returns the signing token plus topic (APNs enabled)
//   - all four empty/unset: returns (nil, "", nil) — APNs disabled, the
//     deployment is Bark-only and callers must tolerate a nil token
//   - partially set: returns an error naming the missing vars, so a typo'd
//     config fails fast instead of silently running Bark-only
//
// An empty value counts as unset (env_file lines like APPLE_KEY_ID= yield ""
// which os.Getenv can't distinguish from absent). APPLE_APNS_SANDBOX and
// BARK_DEFAULT_ICON are independent knobs, deliberately not part of the
// all-or-nothing set.
func LoadAPNS() (*token.Token, string, error) {
	vars := []string{"APPLE_KEY_PATH", "APPLE_KEY_ID", "APPLE_TEAM_ID", "APPLE_APNS_TOPIC"}
	var set, missing []string
	for _, v := range vars {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		} else {
			set = append(set, v)
		}
	}

	if len(set) == 0 {
		return nil, "", nil
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf(
			"partial APNs config: %s set but %s missing; set all four APPLE_* vars for APNs delivery, or unset all four for Bark-only mode",
			strings.Join(set, ", "), strings.Join(missing, ", "))
	}

	keyPath := os.Getenv("APPLE_KEY_PATH")
	authKey, err := token.AuthKeyFromFile(keyPath)
	if err != nil {
		return nil, "", fmt.Errorf("loading APNs auth key from APPLE_KEY_PATH (%s): %w", keyPath, err)
	}

	return &token.Token{
		AuthKey: authKey,
		KeyID:   os.Getenv("APPLE_KEY_ID"),
		TeamID:  os.Getenv("APPLE_TEAM_ID"),
	}, os.Getenv("APPLE_APNS_TOPIC"), nil
}

func NewStatsdClient(tags ...string) (statsd.ClientInterface, error) {
	url := os.Getenv("STATSD_URL")
	if url == "" {
		return &statsd.NoOpClient{}, nil
	}

	if env := os.Getenv("ENV"); env != "" {
		tags = append(tags, fmt.Sprintf("env:%s", env))
	}

	return statsd.New(url, statsd.WithTags(tags))
}

func NewRedisLocksClient(ctx context.Context, maxConns int) (*redis.Client, error) {
	return newRedisClient(ctx, "REDIS_LOCKS_URL", maxConns)
}

func NewRedisQueueClient(ctx context.Context, maxConns int) (*redis.Client, error) {
	return newRedisClient(ctx, "REDIS_QUEUE_URL", maxConns)
}

func newRedisClient(ctx context.Context, env string, maxConns int) (*redis.Client, error) {
	opt, err := redis.ParseURL(os.Getenv(env))
	if err != nil {
		return nil, err
	}
	opt.PoolSize = maxConns

	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	client.AddHook(redisotel.NewTracingHook())

	return client, nil
}

func NewDatabasePool(ctx context.Context, maxConns int) (*pgxpool.Pool, error) {
	if maxConns == 0 {
		maxConns = 1
	}

	url := fmt.Sprintf(
		"%s?pool_max_conns=%d&pool_min_conns=%d",
		os.Getenv("DATABASE_CONNECTION_POOL_URL"),
		maxConns,
		2,
	)
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}

	// Setting the build statement cache to nil helps this work with pgbouncer
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Second
	return pgxpool.NewWithConfig(ctx, config)
}

func NewQueueClient(logger *zap.Logger, conn *redis.Client, identifier string) (rmq.Connection, error) {
	errChan := make(chan error, 10)
	go func() {
		for err := range errChan {
			logger.Error("error occurred within queue", zap.Error(err))
		}
	}()

	return rmq.OpenConnectionWithRedisClient(identifier, conn, errChan)
}
