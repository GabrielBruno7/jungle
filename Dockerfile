FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/jungle ./cmd/jungle
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/jungle /jungle
COPY --from=build /out/migrate /migrate
COPY --from=build /src/migrations /migrations

EXPOSE 8080
ENTRYPOINT ["/jungle"]
