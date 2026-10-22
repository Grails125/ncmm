# ---- 构建阶段 ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN go mod download

COPY main.go VERSION ./
COPY api ./api
COPY config ./config
COPY internal ./internal
COPY pkg ./pkg
COPY cmd ./cmd
ARG VERSION
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
RUN BUILD_VERSION="${VERSION:-$(cat VERSION)}" && \
    CGO_ENABLED=0 go build \
    -trimpath -ldflags="-s -w -X main.Version=${BUILD_VERSION} -X main.Commit=${VCS_REF} -X main.BuildTime=${BUILD_DATE}" \
    -o /ncmm main.go && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=${BUILD_VERSION}" -o /ncmm-launcher ./cmd/ncmm-launcher


# ---- 运行阶段 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
ENV NCMM_DOCKER_OFFICIAL=1 \
    NCMM_WEB_PRESERVE_LEGACY_SCHEDULES=true \
    NCMM_RUNTIME_MANAGED=1 \
    NCMM_RUNTIME_DIR=/data/runtime
LABEL org.opencontainers.image.source="https://github.com/Grails125/ncmm" \
    org.opencontainers.image.description="ncmm 原生音乐合伙人评分集成（基于 v1.2.2）"

COPY --from=builder /ncmm /opt/ncmm/ncmm
COPY --from=builder /ncmm-launcher /usr/local/bin/ncmm-launcher
COPY scripts/ncmm-runtime.sh /usr/local/bin/ncmm
RUN sed -i 's/\r$//' /usr/local/bin/ncmm && chmod +x /usr/local/bin/ncmm
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/ncmm/
COPY licenses /usr/share/licenses/ncmm/licenses

# 复制默认配置模板作为系统备份
COPY config/config.yaml /etc/ncmm/config.yaml
COPY config/notify.yaml /etc/ncmm/notify.yaml

# 复制并配置入口脚本
COPY entrypoint.sh /entrypoint.sh
RUN sed -i 's/\r$//' /entrypoint.sh && chmod +x /entrypoint.sh

# 设置工作目录
WORKDIR /data

EXPOSE 3899

ENTRYPOINT ["/entrypoint.sh"]
CMD ["web", "--listen", "0.0.0.0:3899"]
