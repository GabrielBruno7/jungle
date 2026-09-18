#!/bin/sh
set -e

REGION="${DEFAULT_REGION:-us-east-1}"

create_fifo_queue() {
    name="$1"
    shift
    awslocal sqs create-queue \
        --region "$REGION" \
        --queue-name "$name" \
        "$@"
}

create_fifo_queue "wager-transactions-dlq.fifo" \
    --attributes FifoQueue=true

create_fifo_queue "wager-events-dlq.fifo" \
    --attributes FifoQueue=true

TX_DLQ_ARN=$(awslocal sqs get-queue-attributes \
    --region "$REGION" \
    --queue-url "http://localhost:4566/000000000000/wager-transactions-dlq.fifo" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' --output text)

EV_DLQ_ARN=$(awslocal sqs get-queue-attributes \
    --region "$REGION" \
    --queue-url "http://localhost:4566/000000000000/wager-events-dlq.fifo" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' --output text)

create_fifo_queue "wager-transactions.fifo" \
    --attributes "{
        \"FifoQueue\": \"true\",
        \"VisibilityTimeout\": \"60\",
        \"RedrivePolicy\": \"{\\\"deadLetterTargetArn\\\":\\\"${TX_DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"
    }"

create_fifo_queue "wager-events.fifo" \
    --attributes "{
        \"FifoQueue\": \"true\",
        \"VisibilityTimeout\": \"60\",
        \"RedrivePolicy\": \"{\\\"deadLetterTargetArn\\\":\\\"${EV_DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"
    }"

PROVIDER_PRINCIPAL="${SQS_PROVIDER_PRINCIPAL:-arn:aws:iam::000000000000:role/wager-provider}"
SERVICE_PRINCIPAL="${SQS_SERVICE_PRINCIPAL:-arn:aws:iam::000000000000:role/jungle-service}"

set_queue_policy() {
    queue_url="$1"
    policy="$2"
    awslocal sqs set-queue-attributes \
        --region "$REGION" \
        --queue-url "$queue_url" \
        --attributes "{\"Policy\":\"$(echo "$policy" | sed 's/"/\\"/g' | tr -d '\n')\"}"
}

TX_QUEUE_ARN=$(awslocal sqs get-queue-attributes \
    --region "$REGION" \
    --queue-url "http://localhost:4566/000000000000/wager-transactions.fifo" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' --output text)

EV_QUEUE_ARN=$(awslocal sqs get-queue-attributes \
    --region "$REGION" \
    --queue-url "http://localhost:4566/000000000000/wager-events.fifo" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' --output text)

set_queue_policy "http://localhost:4566/000000000000/wager-transactions.fifo" "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    {
      \"Sid\": \"ProvidersMayOnlyEnqueue\",
      \"Effect\": \"Allow\",
      \"Principal\": {\"AWS\": \"${PROVIDER_PRINCIPAL}\"},
      \"Action\": \"sqs:SendMessage\",
      \"Resource\": \"${TX_QUEUE_ARN}\"
    },
    {
      \"Sid\": \"OnlyThisServiceMayConsume\",
      \"Effect\": \"Allow\",
      \"Principal\": {\"AWS\": \"${SERVICE_PRINCIPAL}\"},
      \"Action\": [
        \"sqs:ReceiveMessage\",
        \"sqs:DeleteMessage\",
        \"sqs:ChangeMessageVisibility\",
        \"sqs:GetQueueAttributes\",
        \"sqs:GetQueueUrl\"
      ],
      \"Resource\": \"${TX_QUEUE_ARN}\"
    }
  ]
}"

set_queue_policy "http://localhost:4566/000000000000/wager-events.fifo" "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    {
      \"Sid\": \"OnlyThisServiceMayPublish\",
      \"Effect\": \"Allow\",
      \"Principal\": {\"AWS\": \"${SERVICE_PRINCIPAL}\"},
      \"Action\": [
        \"sqs:SendMessage\",
        \"sqs:GetQueueAttributes\",
        \"sqs:GetQueueUrl\"
      ],
      \"Resource\": \"${EV_QUEUE_ARN}\"
    },
    {
      \"Sid\": \"ProvidersMayOnlyConsumeEvents\",
      \"Effect\": \"Allow\",
      \"Principal\": {\"AWS\": \"${PROVIDER_PRINCIPAL}\"},
      \"Action\": [
        \"sqs:ReceiveMessage\",
        \"sqs:DeleteMessage\"
      ],
      \"Resource\": \"${EV_QUEUE_ARN}\"
    }
  ]
}"

echo "SQS queues provisioned:"
awslocal sqs list-queues --region "$REGION"

echo "Access policy on wager-transactions.fifo:"
awslocal sqs get-queue-attributes \
    --region "$REGION" \
    --queue-url "http://localhost:4566/000000000000/wager-transactions.fifo" \
    --attribute-names Policy --output text
