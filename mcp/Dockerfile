# demo-mcp: the incident demo's in-house MCP servers, one image holding one binary per server
# (/catalog-mcp, /escalation-mcp, /pager-mcp, /rollout-mcp, /slackread-mcp; each Deployment names its
# binary in command). Multi-stage from digest-pinned images (the same pins Agent Kourier uses); the final
# stage is distroless static and non-root. Build context: this directory (mcp/).
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ ./...

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=build /out/ /
USER 65532:65532
EXPOSE 8084
