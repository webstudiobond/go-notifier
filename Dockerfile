FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

RUN mkdir -p /out/etc/ssl/certs && \
    chmod 0755 /out/etc /out/etc/ssl /out/etc/ssl/certs && \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/ca-certificates.crt && \
    chmod 0444 /out/etc/ssl/certs/ca-certificates.crt && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build \
    -ldflags="-s -w -X 'main.Version=${VERSION}'" \
    -trimpath \
    -o /bin/go-notifier \
    ./cmd/go-notifier

FROM scratch

COPY --from=builder /out/etc /etc
COPY --from=builder --chmod=0555 /bin/go-notifier /usr/bin/go-notifier

ENV NOTIFY_SMTP_ENABLED=true \
    NOTIFY_ADMIN_FILTER_REQUIRED=true

ENTRYPOINT ["/usr/bin/go-notifier"]
