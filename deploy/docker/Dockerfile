# Product image: the static redrill binary (with the embedded web UI) plus the
# engine tools it shells out to (borg, restic) and the postgres client used inside
# L3 sandboxes. Build from the repo root:
#   docker build -f deploy/docker/Dockerfile -t redrill .

# 1. Build the web UI (Vite). Its output (web/dist) is embedded into the Go binary,
#    so this stage must run before the Go build — no Node is needed at runtime.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Build the static Go binary, embedding the UI built above.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/redrill ./cmd/redrill

FROM alpine:3.21
RUN apk add --no-cache \
    borgbackup \
    restic \
    postgresql16-client \
    openssh-client \
    util-linux \
    coreutils \
    ca-certificates \
    tzdata
COPY --from=build /out/redrill /usr/local/bin/redrill
# sqlite store + scratch live here; mount a volume to persist evidence.
VOLUME ["/var/lib/redrill"]
ENTRYPOINT ["redrill"]
CMD ["serve", "-c", "/etc/redrill/config.yaml"]
