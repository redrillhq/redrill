#!/usr/bin/env bash
#
# Enter the redrill dev environment, or run one command inside it.
# The ONLY host dependency is Docker — borg, postgres client tools, etc. live
# in the dev image (built here on first use).
#
#   dev/shell.sh                              # interactive shell
#   dev/shell.sh dev/make-borg-fixture.sh     # one-shot command
#   SEED=7 dev/shell.sh dev/make-borg-fixture.sh   # env knobs pass through
#
# Mounts: the repo at /repo (cwd), a persistent data volume at /work (fixtures,
# scratch, results), and the Docker socket so the toolset can run engine and
# sandbox containers on your daemon (they appear as siblings, not nested).
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE=${REDRILL_DEV_IMAGE:-redrill-dev}
DATA_VOLUME=${REDRILL_DEV_VOLUME:-redrill-dev-data}
SOCK=${DOCKER_SOCK:-/var/run/docker.sock}

docker info >/dev/null 2>&1 || { echo "ERROR: docker daemon not reachable" >&2; exit 2; }

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "==> Building dev image '$IMAGE' (first run only)" >&2
  docker build -t "$IMAGE" dev/
fi

TTY_FLAGS=""
if [ -t 0 ] && [ -t 1 ]; then TTY_FLAGS="-it"; fi

# Knobs forwarded into the container when set on the host (docker omits unset ones).
PASS_ENV="-e SEED -e NUM_FILES -e USERS -e EVENTS -e PG_IMAGE -e KEEP \
  -e BORG_REPO -e BORG_PASSPHRASE_FILE -e BORG_PASSPHRASE -e BORG_RSH \
  -e DUMP_DIR -e PATTERN -e ARCHIVE -e SAMPLE_FILES \
  -e CONFIG_PATH -e DB_DUMP_PATH -e ASSERT_DB -e ASSERT_SQL_1 -e ASSERT_SQL_2"

# shellcheck disable=SC2086  # TTY_FLAGS/PASS_ENV are intentionally word-split
exec docker run --rm $TTY_FLAGS \
  -v "$PWD":/repo -w /repo \
  -v "$DATA_VOLUME":/work \
  -v "$SOCK":/var/run/docker.sock \
  -e DEV_DATA=/work \
  $PASS_ENV \
  "$IMAGE" "${@:-bash}"
