package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
)

func NewClient(lc fx.Lifecycle, cfg *config.Config, logger *zap.Logger) (*sqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.SQS.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	if cfg.SQS.Endpoint != "" {
		awsCfg.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")
	}

	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.SQS.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.SQS.Endpoint)
		}
	})

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			deadline := time.Now().Add(cfg.Startup.DependencyTimeout)

			for attempt := 1; ; attempt++ {
				probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := client.ListQueues(probeCtx, &sqs.ListQueuesInput{})
				cancel()

				if err == nil {
					break
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("connecting to sqs: %w", err)
				}

				logger.Warn("sqs not reachable yet, retrying",
					zap.String("endpoint", cfg.SQS.Endpoint),
					zap.Int("attempt", attempt),
					zap.Error(err))

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}

			logger.Info("connected to sqs",
				zap.String("region", cfg.SQS.Region),
				zap.String("endpoint", cfg.SQS.Endpoint),
			)
			return nil
		},
	})

	return client, nil
}

var Module = fx.Module("queue",
	fx.Provide(
		NewClient,
		NewConsumer,
		fx.Annotate(NewPublisher, fx.As(new(app.Publisher))),
	),
	fx.Invoke(func(*sqs.Client) {}),
)
