# Build from repo root:
#   docker build -f backend/Dockerfile -t truerp-api .
# Or: docker compose -f backend/docker-compose.yml up --build

FROM golang:1.25-bookworm AS builder

WORKDIR /src

RUN apt-get update \
  && apt-get install -y --no-install-recommends gcc libc6-dev \
  && rm -rf /var/lib/apt/lists/*

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# mattn/go-sqlite3 requires CGO
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w" -o /out/truerp .

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates tzdata curl \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --system --uid 1000 --create-home --home-dir /app truerp

WORKDIR /app

COPY --from=builder /out/truerp /app/truerp
COPY HSN_DATASET.csv /app/HSN_DATASET.csv

RUN mkdir -p /app/data /app/uploads \
  && chown -R truerp:truerp /app

USER truerp

ENV API_TRANSPORT=rest \
    API_ADDR=:8088 \
    DATABASE_PATH=/app/data/truerp.db \
    STORAGE_TYPE=local \
    LOCAL_STORAGE_PATH=/app/uploads \
    LOCAL_STORAGE_BASE_URL=/uploads \
    GIN_MODE=release

EXPOSE 8088

VOLUME ["/app/data", "/app/uploads"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS "http://127.0.0.1:8088/health" || exit 1

ENTRYPOINT ["/app/truerp"]
