# drillbit dev-toolset image — every engine dependency lives here so the host
# needs nothing but Docker. Built and used by dev/shell.sh.
# This is NOT the product image (that lands in deploy/ at M10).
FROM alpine:3.21

RUN apk add --no-cache \
    bash coreutils grep sed gzip zstd \
    borgbackup \
    postgresql16-client \
    openssh-client \
    docker-cli

WORKDIR /repo
CMD ["bash"]
