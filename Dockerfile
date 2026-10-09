# syntax=docker/dockerfile:1
# Build from this folder: docker build -t blink-backend .
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/blink-backend ./cmd/server

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3 python3-yaml \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/blink-backend /app/blink-backend
COPY schema.sql /app/schema.sql
COPY migrations /app/migrations
COPY src/main/resources/schema.sql /app/src/main/resources/schema.sql
ENV BLINK_AUTOMATION_SDLC_PATH=/app/automation_sdlc
ENV BLINK_PROD=true
EXPOSE 8090
ENTRYPOINT ["/app/blink-backend"]
