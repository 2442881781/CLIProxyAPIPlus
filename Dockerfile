FROM oven/bun:1.3.14 AS web-builder

WORKDIR /web
ARG VERSION=dev

COPY web/management-center/package.json web/management-center/bun.lock ./
RUN bun install --frozen-lockfile

COPY web/management-center/ ./
RUN VERSION="${VERSION}" bun run build

FROM golang:1.26-bookworm AS builder

WORKDIR /app
ARG GOPROXY=https://proxy.golang.org,direct

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN GOPROXY="${GOPROXY}" go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /CLIProxyAPI/static

COPY --from=builder ./app/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=builder ./app/config.example.yaml /CLIProxyAPI/config.example.yaml
COPY --from=web-builder ./web/dist/index.html /CLIProxyAPI/static/management.html

WORKDIR /CLIProxyAPI

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./CLIProxyAPI"]
