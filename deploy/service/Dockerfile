# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS go-toolchain
FROM --platform=$BUILDPLATFORM node:24-bookworm-slim AS build
COPY --from=go-toolchain /usr/local/go /usr/local/go
COPY --from=go-toolchain /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /src
COPY go.mod go.sum LICENSE ./
COPY internal ./internal
COPY pkg ./pkg
COPY cmd/reignhub-server ./cmd/reignhub-server
COPY clients/nsgo ./clients/nsgo
COPY scripts/build.sh scripts/go-toolexec.sh ./scripts/
COPY scripts/platform/build-console.mjs ./scripts/platform/build-console.mjs
COPY apps/site/components/console-loading.css ./apps/site/components/console-loading.css
COPY apps/site/app/icon.svg ./apps/site/app/icon.svg
ARG VERSION=dev
# A Docker build only needs Node built-ins and Go, not Next.js or Cloudflare.
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    VERSION="$VERSION" node scripts/platform/build-console.mjs --self-hosted \
 && rm -rf internal/webui/assets \
 && cp -R build/self-hosted-assets/assets/chat internal/webui/assets
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
    -ldflags="-s -w -X github.com/monkeyWie/reignhub/internal/servercmd.Version=$VERSION" \
    -o /reignhub-server ./cmd/reignhub-server \
 && mkdir /data && chown 65532:65532 /data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
COPY --from=build /reignhub-server /reignhub-server
COPY --from=build --chown=65532:65532 /data /data
COPY deploy/service/config.json /config.json
WORKDIR /
VOLUME /data
EXPOSE 26330/tcp 26331/tcp 26331/udp 26332/tcp 26332/udp
ENTRYPOINT ["/reignhub-server"]
