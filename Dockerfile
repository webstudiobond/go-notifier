FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}'" \
    -trimpath \
    -o /bin/go-notifier \
    ./cmd/go-notifier

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/go-notifier /usr/bin/go-notifier

ENV NOTIFY_SMTP_ENABLED=true \
    NOTIFY_ADMIN_FILTER_REQUIRED=true

ENTRYPOINT ["/usr/bin/go-notifier"]
