# Product image: the static redrill binary plus the engine tools it shells out to
# (borg, restic) and the postgres client used inside L3 sandboxes. Build from the
# repo root:  docker build -f deploy/docker/Dockerfile -t redrill .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
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
