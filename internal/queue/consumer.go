package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/observability"
)

type messageEnvelope struct {
	MessageID  string          `json:"messageId"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Data       messageData     `json:"data"`
	Raw        json.RawMessage `json:"-"`
}

type messageData struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	CorrelationID                  string `json:"correlationId,omitempty"`
}

var errPermanent = errors.New("queue: permanently unprocessable message")

type Consumer struct {
	client    *sqs.Client
	processor *app.ProcessWager
	cfg       *config.Config
	logger    *zap.Logger
}

func NewConsumer(client *sqs.Client, processor *app.ProcessWager, cfg *config.Config, logger *zap.Logger) *Consumer {
	return &Consumer{client: client, processor: processor, cfg: cfg, logger: logger}
}

type Lifetime struct {
	Fetch context.Context
	Work  context.Context
}

func (c *Consumer) Run(life Lifetime) {
	for {
		if life.Fetch.Err() != nil {
			return
		}

		out, err := c.client.ReceiveMessage(life.Fetch, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.cfg.SQS.RequestQueueURL),
			MaxNumberOfMessages:   c.cfg.SQS.MaxMessages,
			WaitTimeSeconds:       c.cfg.SQS.WaitTimeSeconds,
			VisibilityTimeout:     c.cfg.SQS.VisibilityTimeout,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			if life.Fetch.Err() != nil {
				return
			}
			c.logger.Warn("receiving from queue failed", zap.Error(err))
			select {
			case <-life.Fetch.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		for i, msg := range out.Messages {
			if life.Work.Err() != nil {
				c.release(out.Messages[i:])
				return
			}
			c.handle(life.Work, msg)
		}
	}
}

func (c *Consumer) release(messages []types.Message) {
	if len(messages) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, msg := range messages {
		_, err := c.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(c.cfg.SQS.RequestQueueURL),
			ReceiptHandle:     msg.ReceiptHandle,
			VisibilityTimeout: 0,
		})
		if err != nil {
			c.logger.Warn("releasing an unprocessed message failed; it will be redelivered after its visibility timeout",
				zap.Error(err))
		}
	}

	c.logger.Info("released unprocessed messages for immediate redelivery",
		zap.Int("messages", len(messages)))
}

func (c *Consumer) handle(ctx context.Context, msg types.Message) {
	started := time.Now()

	ctx = contextFromMessageAttributes(ctx, msg.MessageAttributes)
	ctx, span := observability.Tracer().Start(ctx, "consume wager-transactions",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	span.SetAttributes(
		attribute.String("messaging.system", "aws_sqs"),
		attribute.String("messaging.source.name", c.cfg.SQS.RequestQueueURL),
		attribute.String("messaging.consumer.group.name", c.cfg.SQS.ConsumerName),
	)

	err := c.process(ctx, aws.ToString(msg.Body))
	switch {
	case err == nil:
		c.delete(ctx, msg.ReceiptHandle)

	case errors.Is(err, errPermanent):
		span.RecordError(err)
		span.SetStatus(codes.Error, "permanently unprocessable")
		observability.MessagesDeadLettered.Inc()
		c.logger.Error("permanently unprocessable message, leaving for DLQ", zap.Error(err))

	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, "transient failure")
		observability.MessageRetries.Inc()
		c.logger.Warn("transient failure handling message, will be redelivered", zap.Error(err))
	}

	observability.ProcessingLatency.WithLabelValues("sqs").Observe(time.Since(started).Seconds())
}

func (c *Consumer) process(ctx context.Context, body string) error {
	var envelope messageEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return fmt.Errorf("%w: malformed body: %v", errPermanent, err)
	}
	if envelope.MessageID == "" {
		return fmt.Errorf("%w: missing messageId", errPermanent)
	}

	cmd, err := c.buildCommand(envelope)
	if err != nil {
		return err
	}

	result, err := c.processor.Execute(ctx, cmd)
	c.logOutcome(cmd, result, err)
	switch {
	case err == nil:
		observability.TransactionResults.
			WithLabelValues(string(cmd.Kind), string(result.Transaction.Status()), "sqs").Inc()
		if result.IdempotentReplay {
			observability.Duplicates.WithLabelValues("idempotency-key").Inc()
		}
		return nil

	case errors.Is(err, app.ErrIdempotencyConflict),
		errors.Is(err, app.ErrOperationIdentityConflict),
		errors.Is(err, app.ErrWalletMismatch),
		errors.Is(err, app.ErrNotFound):
		return fmt.Errorf("%w: %v", errPermanent, err)

	case errors.Is(err, app.ErrConcurrencyConflict):
		observability.ConcurrencyConflicts.Inc()
		return err

	default:
		return err
	}
}

func (c *Consumer) buildCommand(envelope messageEnvelope) (app.ProcessWagerCommand, error) {
	data := envelope.Data

	if data.IdempotencyKey == "" {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: missing data.idempotencyKey", errPermanent)
	}

	playerID, err := uuid.Parse(data.PlayerID)
	if err != nil {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: playerId is not a UUID", errPermanent)
	}
	walletID, err := uuid.Parse(data.WalletID)
	if err != nil {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: walletId is not a UUID", errPermanent)
	}

	kind := wagertx.Kind(data.Kind)
	if !kind.IsValidExternal() {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: kind %q is not acceptable from a provider", errPermanent, data.Kind)
	}

	currency, err := money.NewCurrency(data.Money.Currency)
	if err != nil {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: %v", errPermanent, err)
	}
	amount, err := money.ParseNonNegative(data.Money.Amount, currency)
	if err != nil {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: %v", errPermanent, err)
	}

	hash, err := app.PayloadHash(app.PayloadFields{
		ProviderID:                     data.ProviderID,
		ExternalTransactionID:          data.ExternalTransactionID,
		PlayerID:                       data.PlayerID,
		WalletID:                       data.WalletID,
		RoundID:                        data.RoundID,
		GameID:                         data.GameID,
		Kind:                           string(kind),
		MoneyAmount:                    amount.DecimalString(),
		MoneyCurrency:                  string(amount.Currency()),
		ReferenceExternalTransactionID: data.ReferenceExternalTransactionID,
	})
	if err != nil {
		return app.ProcessWagerCommand{}, fmt.Errorf("%w: %v", errPermanent, err)
	}

	correlationID := uuid.New()
	if data.CorrelationID != "" {
		if parsed, err := uuid.Parse(data.CorrelationID); err == nil {
			correlationID = parsed
		}
	}

	return app.ProcessWagerCommand{
		ProviderID:                     data.ProviderID,
		ExternalTransactionID:          data.ExternalTransactionID,
		IdempotencyKey:                 data.IdempotencyKey,
		PayloadHash:                    hash,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        data.RoundID,
		GameID:                         data.GameID,
		Kind:                           kind,
		Money:                          amount,
		ReferenceExternalTransactionID: data.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID,
		ConsumerName:                   c.cfg.SQS.ConsumerName,
		MessageID:                      envelope.MessageID,
	}, nil
}

func (c *Consumer) delete(ctx context.Context, receipt *string) {
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if _, err := c.client.DeleteMessage(deleteCtx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.SQS.RequestQueueURL),
		ReceiptHandle: receipt,
	}); err != nil {
		c.logger.Warn("deleting handled message failed; it will be redelivered and deduplicated by the inbox",
			zap.Error(err))
	}
}

func (c *Consumer) logOutcome(cmd app.ProcessWagerCommand, result app.ProcessWagerResult, err error) {
	fields := []zap.Field{
		zap.String("source", "sqs"),
		zap.String("messageId", cmd.MessageID),
		zap.String("consumer", cmd.ConsumerName),
		zap.String("correlationId", cmd.CorrelationID.String()),
		zap.String("providerId", cmd.ProviderID),
		zap.String("externalTransactionId", cmd.ExternalTransactionID),
		zap.String("walletId", cmd.WalletID.String()),
		zap.String("playerId", cmd.PlayerID.String()),
		zap.String("kind", string(cmd.Kind)),
	}

	if err != nil {
		c.logger.Warn("wager operation not applied", append(fields, zap.Error(err))...)
		return
	}

	fields = append(fields,
		zap.String("transactionId", result.Transaction.ID().String()),
		zap.String("status", string(result.Transaction.Status())),
		zap.Bool("idempotentReplay", result.IdempotentReplay),
	)
	if code := result.Transaction.FailureCode(); code != "" {
		fields = append(fields, zap.String("failureCode", string(code)))
	}

	c.logger.Info("wager operation settled", fields...)
}

func contextFromMessageAttributes(ctx context.Context, attrs map[string]types.MessageAttributeValue) context.Context {
	if len(attrs) == 0 {
		return ctx
	}

	carrier := propagation.MapCarrier{}
	for key, value := range attrs {
		if value.StringValue != nil {
			carrier.Set(key, *value.StringValue)
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}
