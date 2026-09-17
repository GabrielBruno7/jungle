package queue

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

// NewClient builds an SQS client. When cfg.SQS.Endpoint is set (LocalStack,
// in local dev) it points the client at it with dummy static credentials;
// otherwise it resolves the real AWS endpoint and credential chain.
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
			if _, err := client.ListQueues(ctx, &sqs.ListQueuesInput{}); err != nil {
				return fmt.Errorf("connecting to sqs: %w", err)
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

// Module provides the shared *sqs.Client and forces it to be built (and
// its connectivity verified) on startup even before any feature depends on it.
var Module = fx.Module("queue",
	fx.Provide(NewClient),
	fx.Invoke(func(*sqs.Client) {}),
)
