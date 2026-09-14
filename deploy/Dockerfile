# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.22
ARG STEP_CLI_VERSION=0.28.7

FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=development
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath \
      -ldflags="-s -w \
        -X slider/pkg/conf.Version=${VERSION} \
        -X slider/pkg/conf.commit=${COMMIT} \
        -X slider/pkg/conf.date=${BUILD_DATE} \
        -X slider/pkg/conf.goVer=$(go env GOVERSION)" \
      -o /out/slider .

FROM smallstep/step-cli:${STEP_CLI_VERSION} AS certgen

COPY --chmod=0555 deploy/caddy/init-tls.sh /usr/local/bin/init-slider-tls

USER root
ENTRYPOINT ["/usr/local/bin/init-slider-tls"]

FROM alpine:${ALPINE_VERSION} AS runtime

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 slider \
    && adduser -S -D -H -u 10001 -G slider slider \
    && install -d -o slider -g slider -m 0700 /data

COPY --from=build /out/slider /usr/local/bin/slider

USER slider:slider
WORKDIR /data

ENV S_HOME=/data/ \
    NO_COLOR=1

ENTRYPOINT ["/usr/local/bin/slider"]
CMD ["--help"]
