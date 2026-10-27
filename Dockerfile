# syntax=docker/dockerfile:1
FROM oven/bun:1.4.4@sha256:1d22548f177f4ff38e0c15baa3c2c46c6c2060ba2d1bc573bb4720b407548f1d AS web-build
WORKDIR /src/apps/mekugi/internal/webui/app
COPY apps/mekugi/internal/webui/app/package.json apps/mekugi/internal/webui/app/bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache \
    bun install --frozen-lockfile
COPY apps/mekugi/internal/webui/app/ ./
COPY apps/mekugi/internal/sessionapi/*.ts /src/apps/mekugi/internal/sessionapi/
RUN bun run --bun build

FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS go-build
WORKDIR /src
# go.work resolves all local modules together. Tree-sitter requires CGO.
COPY . .
COPY --from=web-build /src/apps/mekugi/internal/webui/dist/app/ apps/mekugi/internal/webui/dist/app/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -buildvcs=false -ldflags='-s -w' \
    -o /out/mekugi ./apps/mekugi/cmd/mekugi

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go-build /out/mekugi /usr/local/bin/mekugi
USER 1000:1000
EXPOSE 7777 7778
ENTRYPOINT ["/usr/local/bin/mekugi"]
CMD ["web", "--listen", "0.0.0.0:7777"]
