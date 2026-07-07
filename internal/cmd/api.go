package cmd

import (
	"context"
	"os"
	"strconv"

	"github.com/christianselig/apollo-backend/internal/api"
	"github.com/christianselig/apollo-backend/internal/cmdutil"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func APICmd(ctx context.Context) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "api",
		Args:  cobra.ExactArgs(0),
		Short: "Runs the RESTful API.",
		RunE: func(cmd *cobra.Command, args []string) error {
			port = 4000
			if os.Getenv("PORT") != "" {
				port, _ = strconv.Atoi(os.Getenv("PORT"))
			}

			logger := cmdutil.NewLogger("api")
			defer func() { _ = logger.Sync() }()

			statsd, err := cmdutil.NewStatsdClient()
			if err != nil {
				return err
			}
			defer statsd.Close()

			apns, apnsTopic, err := cmdutil.LoadAPNS()
			if err != nil {
				logger.Error("apns startup failed", zap.Error(err))
				return err
			}
			if apns == nil {
				logger.Info("APNs disabled (no APPLE_* env vars set); running in Bark-only mode")
			}

			db, err := cmdutil.NewDatabasePool(ctx, 16)
			if err != nil {
				return err
			}
			defer db.Close()

			redis, err := cmdutil.NewRedisLocksClient(ctx, 16)
			if err != nil {
				return err
			}
			defer redis.Close()

			api := api.NewAPI(ctx, logger, statsd, redis, db, apns, apnsTopic)
			srv := api.Server(port)

			go func() { _ = srv.ListenAndServe() }()

			logger.Info("started api", zap.Int("port", port))

			<-ctx.Done()

			_ = srv.Shutdown(ctx)

			return nil
		},
	}

	return cmd
}
