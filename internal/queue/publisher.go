package queue

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/observability"
)

type Publisher struct {
	client   *sqs.Client
	queueURL string
}

func NewPublisher(client *sqs.Client, cfg *config.Config) *Publisher {
	return &Publisher{client: client, queueURL: cfg.SQS.EventQueueURL}
}

func (p *Publisher) Publish(ctx context.Context, rec app.OutboxRecord) error {
	ctx = contextFromTraceParent(ctx, rec.TraceParent)

	ctx, span := observability.Tracer().Start(ctx, "publish "+rec.Type,
		trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()

	span.SetAttributes(
		attribute.String("messaging.system", "aws_sqs"),
		attribute.String("messaging.destination.name", p.queueURL),
		attribute.String("jungle.event_id", rec.EventID.String()),
		attribute.String("jungle.event_type", rec.Type),
	)

	attributes := map[string]types.MessageAttributeValue{
		"eventType": {DataType: aws.String("String"), StringValue: aws.String(rec.Type)},
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	for _, key := range carrier.Keys() {
		attributes[key] = types.MessageAttributeValue{
			DataType:    aws.String("String"),
			StringValue: aws.String(carrier.Get(key)),
		}
	}

	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(rec.Payload)),
		MessageGroupId:         aws.String(rec.AggregateID),
		MessageDeduplicationId: aws.String(rec.EventID.String()),
		MessageAttributes:      attributes,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		observability.OutboxRetries.Inc()
		return fmt.Errorf("publishing event %s: %w", rec.EventID, err)
	}

	observability.OutboxPublished.Inc()
	return nil
}

func contextFromTraceParent(ctx context.Context, traceParent string) context.Context {
	if traceParent == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": traceParent})
}
