//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"jungle/internal/config"
	"jungle/internal/queue"
)

const (
	defaultSQSEndpoint = "http://localhost:4566"
	testConsumerName   = "integration-test-consumer"
)

func sqsEndpoint() string {
	if v := os.Getenv("SQS_ENDPOINT_URL"); v != "" {
		return v
	}
	return defaultSQSEndpoint
}

func sqsRegion() string {
	if v := os.Getenv("AWS_REGION"); v != "" {
		return v
	}
	return "us-east-1"
}

func newSQSClient(t *testing.T) *sqs.Client {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(sqsRegion()),
	)
	if err != nil {
		t.Fatalf("loading aws config: %v", err)
	}
	awsCfg.Credentials = credentials.NewStaticCredentialsProvider("test", "test", "")

	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(sqsEndpoint())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.ListQueues(ctx, &sqs.ListQueuesInput{}); err != nil {
		t.Fatalf("reaching SQS at %s: %v (is `docker compose up -d localstack` running?)", sqsEndpoint(), err)
	}

	return client
}

type testQueues struct {
	client   *sqs.Client
	main     string
	dlq      string
	mainName string
}

func newTestQueues(t *testing.T, maxReceiveCount int) *testQueues {
	t.Helper()

	client := newSQSClient(t)
	suffix := uuid.NewString()[:8]
	mainName := "jungle-it-" + suffix + ".fifo"
	dlqName := "jungle-it-" + suffix + "-dlq.fifo"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dlq, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(dlqName),
		Attributes: map[string]string{"FifoQueue": "true"},
	})
	if err != nil {
		t.Fatalf("creating dead letter queue: %v", err)
	}

	arn, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       dlq.QueueUrl,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("reading dead letter queue arn: %v", err)
	}

	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`,
		arn.Attributes[string(types.QueueAttributeNameQueueArn)], maxReceiveCount)

	main, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(mainName),
		Attributes: map[string]string{
			"FifoQueue":         "true",
			"VisibilityTimeout": "1",
			"RedrivePolicy":     redrive,
		},
	})
	if err != nil {
		t.Fatalf("creating request queue: %v", err)
	}

	queues := &testQueues{
		client:   client,
		main:     aws.ToString(main.QueueUrl),
		dlq:      aws.ToString(dlq.QueueUrl),
		mainName: mainName,
	}

	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()
		_, _ = client.DeleteQueue(cleanupCtx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queues.main)})
		_, _ = client.DeleteQueue(cleanupCtx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queues.dlq)})
	})

	return queues
}

func (q *testQueues) send(t *testing.T, groupID, dedupID, body string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(q.main),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		t.Fatalf("sending message to %s: %v", q.mainName, err)
	}
}

func (q *testQueues) depth(t *testing.T, queueURL string) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatalf("reading queue attributes: %v", err)
	}

	total := 0
	for _, name := range []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages,
		types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	} {
		if raw, ok := out.Attributes[string(name)]; ok {
			n, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatalf("parsing queue attribute %s=%q: %v", name, raw, err)
			}
			total += n
		}
	}
	return total
}

func (q *testQueues) receiveOne(t *testing.T, visibilityTimeout int32) (types.Message, bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.main),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     3,
		VisibilityTimeout:   visibilityTimeout,
	})
	if err != nil {
		t.Fatalf("receiving message: %v", err)
	}
	if len(out.Messages) == 0 {
		return types.Message{}, false
	}
	return out.Messages[0], true
}

func (q *testQueues) makeVisible(t *testing.T, receiptHandle *string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.main),
		ReceiptHandle:     receiptHandle,
		VisibilityTimeout: 0,
	})
	if err != nil {
		t.Fatalf("making message visible again: %v", err)
	}
}

func envelopeFor(t *testing.T, op operation, messageID string) string {
	t.Helper()

	provider := op.providerID
	if provider == "" {
		provider = "provider-a"
	}
	round := op.roundID
	if round == "" {
		round = "round-1"
	}
	game := op.gameID
	if game == "" {
		game = "fortune-chimp"
	}
	key := op.key
	if key == "" {
		key = provider + ":" + op.externalID
	}

	data := map[string]any{
		"providerId":            provider,
		"externalTransactionId": op.externalID,
		"idempotencyKey":        key,
		"playerId":              op.playerID.String(),
		"walletId":              op.walletID.String(),
		"roundId":               round,
		"gameId":                game,
		"kind":                  string(op.kind),
		"money": map[string]string{
			"amount":   brl(t, op.amount).DecimalString(),
			"currency": "BRL",
		},
	}
	if op.reference != "" {
		data["referenceExternalTransactionId"] = op.reference
	}

	envelope := map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encoding message envelope: %v", err)
	}
	return string(encoded)
}

type runningConsumer struct {
	stopFetch   context.CancelFunc
	abandonWork context.CancelFunc
	done        chan struct{}
}

func startConsumer(t *testing.T, inst *instance, q *testQueues, visibilityTimeout int32) *runningConsumer {
	t.Helper()

	cfg := &config.Config{
		SQS: config.SQS{
			Region:            sqsRegion(),
			Endpoint:          sqsEndpoint(),
			RequestQueueURL:   q.main,
			ConsumerName:      testConsumerName,
			MaxMessages:       10,
			WaitTimeSeconds:   1,
			VisibilityTimeout: visibilityTimeout,
		},
	}

	consumer := queue.NewConsumer(q.client, inst.process, cfg, zap.NewNop())

	fetchCtx, stopFetch := context.WithCancel(context.Background())
	workCtx, abandonWork := context.WithCancel(context.Background())
	running := &runningConsumer{
		stopFetch:   stopFetch,
		abandonWork: abandonWork,
		done:        make(chan struct{}),
	}

	go func() {
		defer close(running.done)
		consumer.Run(queue.Lifetime{Fetch: fetchCtx, Work: workCtx})
	}()

	t.Cleanup(running.stop)
	return running
}

func (r *runningConsumer) stop() {
	r.stopFetch()
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
	}
	r.abandonWork()
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func (i *instance) inboxCount(t *testing.T, messageID string) int {
	t.Helper()

	var count int
	err := i.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox WHERE consumer_name = $1 AND message_id = $2`,
		testConsumerName, messageID).Scan(&count)
	if err != nil {
		t.Fatalf("counting inbox rows: %v", err)
	}
	return count
}

func (i *instance) transactionCount(t *testing.T, externalID string) int {
	t.Helper()

	var count int
	err := i.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wager_transaction WHERE external_transaction_id = $1`,
		externalID).Scan(&count)
	if err != nil {
		t.Fatalf("counting transactions: %v", err)
	}
	return count
}
