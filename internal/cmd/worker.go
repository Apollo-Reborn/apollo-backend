package cmd

import (
	"context"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/christianselig/apollo-backend/internal/cmdutil"
	"github.com/christianselig/apollo-backend/internal/worker"
)

var (
	queues = map[string]worker.NewWorkerFn{
		"live-activities":     worker.NewLiveActivitiesWorker,
		"notifications":       worker.NewNotificationsWorker,
		"stuck-notifications": worker.NewStuckNotificationsWorker,
		"subreddits":          worker.NewSubredditsWorker,
		"trending":            worker.NewTrendingWorker,
		"users":               worker.NewUsersWorker,
	}
)

func WorkerCmd(ctx context.Context) *cobra.Command {
	var consumers int
	var queueID string

	cmd := &cobra.Command{
		Use:   "worker",
		Args:  cobra.ExactArgs(0),
		Short: "Work through job queues.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if queueID == "" {
				return fmt.Errorf("need a queue to work on")
			}

			runtime.SetBlockProfileRate(1)
			runtime.SetMutexProfileFraction(1)

			svc := fmt.Sprintf("worker: %s", queueID)
			logger := cmdutil.NewLogger(svc)
			defer func() { _ = logger.Sync() }()

			tag := fmt.Sprintf("worker:%s", queueID)
			statsd, err := cmdutil.NewStatsdClient(tag)
			if err != nil {
				return err
			}
			defer statsd.Close()

			tracer := otel.Tracer(tag)

			// Load APNs config before any queue/redis client exists: a config
			// error here must not leave an rmq heartbeat goroutine running
			// against a closed redis client (spurious "heartbeat error" spam
			// during the crash-loop unwind).
			apns, apnsTopic, err := cmdutil.LoadAPNS()
			if err != nil {
				logger.Error("apns startup failed", zap.Error(err))
				return err
			}
			if apns == nil {
				logger.Info("APNs disabled (no APPLE_* env vars set); running in Bark-only mode")
			}

			db, err := cmdutil.NewDatabasePool(ctx, consumers/16)
			if err != nil {
				return err
			}
			defer db.Close()

			redis, err := cmdutil.NewRedisLocksClient(ctx, consumers/4)
			if err != nil {
				return err
			}
			defer redis.Close()

			qredis, err := cmdutil.NewRedisQueueClient(ctx, consumers/16)
			if err != nil {
				return err
			}
			defer qredis.Close()

			queue, err := cmdutil.NewQueueClient(logger, qredis, "worker")
			if err != nil {
				return err
			}

			workerFn, ok := queues[queueID]
			if !ok {
				return fmt.Errorf("invalid queue: %s", queueID)
			}

			worker := workerFn(ctx, logger, tracer, statsd, db, redis, queue, consumers, apns, apnsTopic)
			if err := worker.Start(); err != nil {
				return err
			}

			<-ctx.Done()

			worker.Stop()

			return nil
		},
	}

	cmd.Flags().IntVar(&consumers, "consumers", runtime.NumCPU()*64, "The consumers to run")
	cmd.Flags().StringVar(&queueID, "queue", "", "The queue to work on")

	return cmd
}
