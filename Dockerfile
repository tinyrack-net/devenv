# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
RUN npm i -g pnpm@12.16.1
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=web /src/web/dist web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/devenv ./cmd/devenv \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/devenv-agent ./cmd/devenv-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/devenv /devenv
# Environments mount this image at /opt/devenv (image volume), so the helper
# is available there as /opt/devenv/bin/devenv-agent.
COPY --from=build /out/devenv-agent /bin/devenv-agent
USER 65532:65532
EXPOSE 8080 2222
ENTRYPOINT ["/devenv"]
