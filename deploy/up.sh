#!/bin/sh
# Starts the stack in the background and waits until bootstrap has finished.
# `make up` runs it. `docker compose up --wait` would treat bootstrap exiting
# as a failure, so this waits on its exit code instead.
set -u

docker compose up -d --build || exit 1
docker compose wait bootstrap >/dev/null
code=$(docker compose ps -a bootstrap --format '{{.ExitCode}}')
if [ "$code" = 0 ]; then
  echo "Stack is up. Streams and buckets are ready. The web app is on http://localhost:3000"
else
  echo "Bootstrap failed with exit code $code:"
  docker compose logs --no-log-prefix bootstrap
  exit 1
fi
