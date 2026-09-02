# ---- Build stage ----
FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/jira-ai-creator .

# ---- Runtime stage ----
# Distroless static image: no shell, no package manager, no libc — minimal attack surface.
# Includes ca-certificates and tzdata. Runs as nonroot (uid 65534) by default.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=builder /bin/jira-ai-creator /usr/local/bin/jira-ai-creator

ENTRYPOINT ["jira-ai-creator"]
