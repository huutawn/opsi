ARG GO_VERSION=1.26.4

FROM golang:${GO_VERSION}-alpine AS builder
ARG VERSION=dev
WORKDIR /src/cloud
COPY contracts/go /src/contracts/go
COPY cloud/go.mod cloud/go.sum ./
RUN go mod download
COPY cloud/cmd ./cmd
COPY cloud/internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/opsi-cloud ./cmd/opsi-cloud && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker

FROM alpine:3.22 AS runtime-base
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 1000 opsi && \
    adduser -S -D -H -u 1000 -G opsi opsi && \
    install -d -o opsi -g opsi -m 0750 /var/lib/opsi /run/opsi-secrets
USER opsi:opsi

FROM runtime-base AS cloud
COPY --from=builder --chown=opsi:opsi /out/opsi-cloud /usr/local/bin/opsi-cloud
ENTRYPOINT ["/usr/local/bin/opsi-cloud"]

FROM runtime-base AS bootstrap-worker
COPY --from=builder --chown=opsi:opsi /out/opsi-bootstrap-worker /usr/local/bin/opsi-bootstrap-worker
ENTRYPOINT ["/usr/local/bin/opsi-bootstrap-worker"]
