# syntax=docker/dockerfile:1
# Build from this folder: docker build -t blink-backend .
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/blink-backend ./cmd/server

FROM node:22-bookworm-slim AS previewjs
WORKDIR /preview
COPY preview-bundler/package.json preview-bundler/bundle.mjs ./
RUN npm install --omit=dev

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3 python3-yaml \
    && rm -rf /var/lib/apt/lists/*
COPY --from=node:22-bookworm-slim /usr/local /usr/local
COPY --from=previewjs /preview /opt/preview-bundler
COPY --from=build /out/blink-backend /app/blink-backend
COPY schema.sql /app/schema.sql
COPY migrations /app/migrations
COPY src/main/resources/schema.sql /app/src/main/resources/schema.sql
ENV BLINK_AUTOMATION_SDLC_PATH=/app/automation_sdlc
ENV BLINK_PROD=true
ENV BLINK_PREVIEW_BUNDLER=/opt/preview-bundler
ENV BLINK_PREVIEW_NODE=/usr/local/bin/node
EXPOSE 8090
ENTRYPOINT ["/app/blink-backend"]
