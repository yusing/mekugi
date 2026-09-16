# syntax=docker/dockerfile:1

FROM node:24-bookworm AS node-runtime

FROM golang:1.27-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/mekugi ./cmd/mekugi
RUN go build -trimpath -o /out/shell ./cmd/shell
RUN go build -trimpath -o /out/mekugi-merge-captures ./benchmarks/cmd/merge-captures

FROM golang:1.27-bookworm

COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node

RUN apt-get update \
	&& apt-get install -y --no-install-recommends python3 ripgrep iptables util-linux \
	&& rm -rf /var/lib/apt/lists/*

RUN printf '%s\n' 'export PATH="/usr/local/go/bin:$PATH"' >/etc/profile.d/go-path.sh

ARG CODEX_RELEASE=0.153.4
RUN curl -fsSL https://chatgpt.com/codex/install.sh \
	| CODEX_RELEASE="$CODEX_RELEASE" CODEX_NON_INTERACTIVE=1 CODEX_INSTALL_DIR=/usr/local/bin sh
# Keep the immutable CLI package outside the writable per-session CODEX_HOME.
RUN target="$(readlink -f /usr/local/bin/codex)" \
    && case "$target" in /root/.codex/packages/*) ;; *) exit 1 ;; esac \
    && mkdir -p /opt/codex /usr/local/libexec \
    && mv /root/.codex/packages /opt/codex/packages \
    && rm /usr/local/bin/codex \
    && ln -s "/opt/codex/packages/${target#/root/.codex/packages/}" /usr/local/libexec/codex-real
COPY --chmod=0755 benchmarks/isolated-codex.sh /usr/local/bin/codex
COPY --chmod=0755 benchmarks/session-entry.sh /usr/local/bin/mekugi-benchmark-session
COPY --chmod=0755 benchmarks/agent-mounts.sh /usr/local/libexec/mekugi-agent-mounts
COPY benchmarks/agent-check.py /usr/local/libexec/mekugi-agent-check.py
COPY --from=builder /out/ /usr/local/bin/
COPY --chmod=0755 benchmarks/report-issue-hook.sh /usr/local/bin/mekugi-benchmark-report-issue

WORKDIR /workspace
