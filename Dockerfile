# Pin base images for reproducible builds
ARG ALPINE_VERSION=3.21
ARG GO_VERSION=1.23

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS builder

# Pin SSO tool version - use branch/tag/commit
ARG SSO_VERSION=master

WORKDIR /app

RUN apk upgrade --no-cache && \
    apk add --no-cache git bash curl

# Copy Go source (go.mod, sso.go, sso/, totp/)
COPY go.mod go.sum sso.go ./
COPY sso/ ./sso/
COPY totp/ ./totp/

# Build the SSO tool from local source
RUN go mod tidy && go build -o /usr/local/bin/sso

# Application stage
ARG ALPINE_VERSION
FROM alpine:${ALPINE_VERSION} AS application

# Copy the built SSO binary
COPY --from=builder /usr/local/bin/sso /usr/local/bin/sso

# Install required dependencies
RUN apk add --no-cache bash curl aws-cli jq socat

# Create directories
RUN mkdir -p /root/.aws /config
VOLUME /root/.aws
VOLUME /config

# Copy scripts
COPY get-credentials-lib.sh get-credentials.sh serve-credentials.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/*

# Default entrypoint
ENTRYPOINT ["/usr/local/bin/get-credentials.sh"]
