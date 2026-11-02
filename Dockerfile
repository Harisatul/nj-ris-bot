FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN go mod tidy && go mod download
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mimo-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mimo-worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mimo-migrate ./cmd/migrate
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY db/migrations/ /app/db/migrations/
USER 65532:65532
EXPOSE 8080
CMD ["mimo-api"]
