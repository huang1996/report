# ---------- 构建阶段 ----------
FROM golang:1.23-alpine AS build
ARG APP_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.AppVersion=${APP_VERSION}" \
    -o /out/inspection-report ./cmd/report

# ---------- 运行阶段 ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata fonts-droid-fallback \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/inspection-report /app/inspection-report
RUN mkdir -p /app/report /app/logs
ENV TZ=Asia/Shanghai
VOLUME ["/app/report", "/app/logs"]
ENTRYPOINT ["/app/inspection-report"]
# 默认：定时模式（每天 REPORT_TIME 触发一次）；单次执行用 command: ["-now", ...]
CMD []
