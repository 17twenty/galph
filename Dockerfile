FROM node:20-bookworm-slim

# System deps for git, networking, and build tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    iptables \
    ipset \
    iproute2 \
    ca-certificates \
    curl \
    make \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Install Go (uses TARGETARCH for correct arch on arm64/amd64)
ARG GO_VERSION=1.24.1
ARG TARGETARCH
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/home/node/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Create workspace and user dirs
RUN mkdir -p /workspace /home/node/.claude /home/node/.galph /home/node/go && \
    chown -R node:node /workspace /home/node

# Set git safe directory (workspace is a volume mount)
RUN git config --system safe.directory /workspace

WORKDIR /workspace
USER node

# Container stays alive — galph exec's commands into it
ENTRYPOINT ["sleep", "infinity"]
