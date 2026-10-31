#!/usr/bin/env bash
# Build the old images, scan them, try three patch approaches, rescan, and
# check /up. A failed approach is recorded and the script continues.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RAW="$ROOT/results/raw"
mkdir -p "$RAW"
# shellcheck disable=SC1091
source "$ROOT/scripts/pins.env"

STAGE="${1:-all}"
ONLY="${2:-}"

log() { printf '\n== %s\n' "$*"; }

need() {
  command -v "$1" >/dev/null || {
    echo "missing $1" >&2
    exit 1
  }
}

seconds_of() {
  local start=$1
  python3 -c "import time; print(round(time.time() - float('$start'), 1))"
}

image_of() {
  # $1 name, $2 approach
  echo "livepatch-$1:$2"
}

want() {
  [[ -z "$ONLY" || "$ONLY" == "$1" ]]
}

scan_one() {
  local name=$1 approach=$2
  local image
  image="$(image_of "$name" "$approach")"
  mkdir -p "$RAW/$name"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "no image $image" | tee "$RAW/$name/$approach.scan-error"
    return 0
  fi
  docker image inspect -f '{{.Size}}' "$image" >"$RAW/$name/$approach.size"
  log "scan $image"
  trivy image \
    --skip-db-update \
    --format json \
    --output "$RAW/$name/$approach.trivy.json" \
    --scanners vuln \
    --pkg-types os,library \
    --no-progress \
    "$image"
}

health_one() {
  local name=$1 approach=$2 port=$3
  local image cid mapped body up_code root_code i
  image="$(image_of "$name" "$approach")"
  mkdir -p "$RAW/$name"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    printf 'status=absent\n' >"$RAW/$name/$approach.health"
    return 0
  fi
  cid="$(docker run -d -p "127.0.0.1::${port}" "$image")"
  mapped="$(docker port "$cid" "${port}/tcp" | awk -F: 'NR==1 {print $NF}')"
  up_code="000"
  root_code="000"
  body=""
  for i in $(seq 1 40); do
    up_code="$(curl -s -o "$RAW/$name/$approach.up" -w '%{http_code}' -m 2 "http://127.0.0.1:${mapped}/up" || true)"
    if [[ "$up_code" == "200" ]]; then
      break
    fi
    sleep 1
  done
  root_code="$(curl -s -o "$RAW/$name/$approach.root" -w '%{http_code}' -m 2 "http://127.0.0.1:${mapped}/" || true)"
  body="$(head -c 200 "$RAW/$name/$approach.root" 2>/dev/null || true)"
  docker logs "$cid" >"$RAW/$name/$approach.container-log" 2>&1 || true
  docker rm -f "$cid" >/dev/null
  if [[ "$up_code" == "200" && "$root_code" == "200" ]]; then
    printf 'status=ok\nup=%s\nroot=%s\nbody=%s\n' "$up_code" "$root_code" "$body" >"$RAW/$name/$approach.health"
  else
    printf 'status=fail\nup=%s\nroot=%s\nbody=%s\n' "$up_code" "$root_code" "$body" >"$RAW/$name/$approach.health"
  fi
  echo "health $image -> $(head -n 1 "$RAW/$name/$approach.health") body=$(printf '%s' "$body" | tr '\n' ' ')"
}

timed() {
  # timed <seconds-file> <log-file> -- command...
  local dest=$1 logf=$2
  shift 2
  local start
  start="$(python3 -c 'import time; print(time.time())')"
  set +e
  "$@" >"$logf" 2>&1
  local code=$?
  set -e
  seconds_of "$start" >"$dest"
  echo "$code" >"${dest%.time}.exit"
  return "$code"
}

ensure_registry() {
  if ! docker ps --format '{{.Names}}' | grep -qx livepatch-registry; then
    docker rm -f livepatch-registry >/dev/null 2>&1 || true
    docker run -d --name livepatch-registry -p 127.0.0.1:5000:5000 registry:2 >/dev/null
    sleep 1
  fi
}

do_upgrade() {
  local name=$1 kind=$2 user=$3
  local src dest start
  src="$(image_of "$name" orig)"
  dest="$(image_of "$name" upgrade)"
  mkdir -p "$RAW/$name"
  start="$(python3 -c 'import time; print(time.time())')"
  set +e
  if [[ "$kind" == alpine ]]; then
    docker build --provenance=false --sbom=false -t "$dest" -f - "$ROOT" >"$RAW/$name/upgrade.log" 2>&1 <<EOF
FROM $src
USER root
RUN apk update && apk upgrade --no-cache
USER ${user:-root}
EOF
  else
    docker build --provenance=false --sbom=false -t "$dest" -f - "$ROOT" >"$RAW/$name/upgrade.log" 2>&1 <<EOF
FROM $src
USER root
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get upgrade -y --no-install-recommends && rm -rf /var/lib/apt/lists/*
USER ${user:-root}
EOF
  fi
  local code=$?
  set -e
  seconds_of "$start" >"$RAW/$name/upgrade.time"
  echo "$code" >"$RAW/$name/upgrade.exit"
  echo "upgrade $name exit=$code seconds=$(cat "$RAW/$name/upgrade.time")"
}

do_copa() {
  local name=$1 timeout=$2
  local src dest
  src="$(image_of "$name" orig)"
  dest="$(image_of "$name" copa)"
  mkdir -p "$RAW/$name"
  if [[ ! -f "$RAW/$name/orig.trivy.json" ]]; then
    echo "no report" >"$RAW/$name/copa.log"
    echo 1 >"$RAW/$name/copa.exit"
    return 0
  fi
  # Default pkg type is os. Library findings in the report are ignored.
  if timed "$RAW/$name/copa.time" "$RAW/$name/copa.log" \
    copa patch --timeout "$timeout" --progress plain --loader docker \
    -i "$src" -r "$RAW/$name/orig.trivy.json" -t "$dest"; then
    echo "copa $name ok $(cat "$RAW/$name/copa.time")s"
  else
    echo "copa $name FAILED exit=$(cat "$RAW/$name/copa.exit") $(cat "$RAW/$name/copa.time")s"
  fi
}

do_rebase() {
  local name=$1 old_base=$2 new_base=$3
  local src="localhost:5000/livepatch/${name}:orig"
  local dest="localhost:5000/livepatch/${name}:rebase"
  mkdir -p "$RAW/$name"
  docker tag "$(image_of "$name" orig)" "$src"
  if ! timed "$RAW/$name/rebase-push.time" "$RAW/$name/rebase.log" docker push "$src"; then
    echo "rebase $name push failed"
    echo 1 >"$RAW/$name/rebase.exit"
    return 0
  fi
  local start code
  start="$(python3 -c 'import time; print(time.time())')"
  set +e
  crane rebase --insecure --platform linux/amd64 \
    --old_base "$old_base" --new_base "$new_base" --tag "$dest" "$src" \
    >>"$RAW/$name/rebase.log" 2>&1
  code=$?
  set -e
  # Time is the rebase itself, not the push of the original.
  seconds_of "$start" >"$RAW/$name/rebase.time"
  if [[ "$code" -eq 0 ]]; then
    if docker pull "$dest" >>"$RAW/$name/rebase.log" 2>&1; then
      docker tag "$dest" "$(image_of "$name" rebase)"
    else
      code=1
    fi
  fi
  echo "$code" >"$RAW/$name/rebase.exit"
  echo "rebase $name exit=$code seconds=$(cat "$RAW/$name/rebase.time")"
}

build_locks() {
  log "lockfiles"
  if [[ ! -f "$ROOT/apps/node/package-lock.json" ]]; then
    docker run --rm -v "$ROOT/apps/node:/app" -w /app --user "$(id -u):$(id -g)" \
      "$NODE_OLD" npm install --package-lock-only --omit=dev
  fi
  if [[ ! -f "$ROOT/apps/rails/Gemfile.lock" ]]; then
    docker run --rm -v "$ROOT/apps/rails:/app" -w /app --user "$(id -u):$(id -g)" \
      "$RUBY_OLD" bundle lock --add-platform x86_64-linux
  fi
}

build_images() {
  log "go binary"
  (
    cd "$ROOT/apps/go"
    GOTOOLCHAIN="$GO_TOOLCHAIN" CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -buildvcs=false -o app .
  )
  if want go-alpine; then
    docker build --provenance=false --sbom=false -t "$(image_of go-alpine orig)" -f "$ROOT/apps/go/Dockerfile.alpine" "$ROOT/apps/go"
  fi
  if want go-debian; then
    docker build --provenance=false --sbom=false -t "$(image_of go-debian orig)" -f "$ROOT/apps/go/Dockerfile.debian" "$ROOT/apps/go"
  fi
  if want go-distroless; then
    docker build --provenance=false --sbom=false -t "$(image_of go-distroless orig)" -f "$ROOT/apps/go/Dockerfile.distroless" "$ROOT/apps/go"
  fi
  if want go-scratch; then
    docker build --provenance=false --sbom=false -t "$(image_of go-scratch orig)" -f "$ROOT/apps/go/Dockerfile.scratch" "$ROOT/apps/go"
  fi
  if want node || want node-alpine; then
    build_locks
  fi
  if want node; then
    docker pull "$NODE_OLD"
    docker build --provenance=false --sbom=false -t "$(image_of node orig)" "$ROOT/apps/node"
  fi
  if want node-alpine; then
    docker pull "$NODE_ALPINE_OLD"
    docker build --provenance=false --sbom=false -t "$(image_of node-alpine orig)" -f "$ROOT/apps/node/Dockerfile.alpine" "$ROOT/apps/node"
  fi
  if want rails; then
    build_locks
    docker pull "$RUBY_OLD"
    docker build --provenance=false --sbom=false -t "$(image_of rails orig)" "$ROOT/apps/rails"
  fi
}

patch_images() {
  ensure_registry
  if want go-alpine; then
    do_copa go-alpine 15m
    do_upgrade go-alpine alpine 65534:65534
    do_rebase go-alpine "$ALPINE_OLD" "$ALPINE_NEW"
  fi
  if want go-debian; then
    do_copa go-debian 20m
    do_upgrade go-debian debian 65534:65534
    do_rebase go-debian "$DEBIAN_OLD" "$DEBIAN_NEW"
  fi
  if want go-distroless; then
    do_copa go-distroless 5m
  fi
  if want go-scratch; then
    do_copa go-scratch 5m
  fi
  if want node; then
    do_copa node 20m
    do_upgrade node debian root
    do_rebase node "$NODE_OLD" "$NODE_NEW"
  fi
  if want node-alpine; then
    do_copa node-alpine 15m
    do_upgrade node-alpine alpine root
    do_rebase node-alpine "$NODE_ALPINE_OLD" "$NODE_ALPINE_NEW"
  fi
  if want rails; then
    do_copa rails 20m
    do_upgrade rails debian root
    do_rebase rails "$RUBY_OLD" "$RUBY_NEW"
  fi
}

scan_all() {
  local name approach
  for name in rails node node-alpine go-alpine go-debian go-distroless go-scratch; do
    want "$name" || continue
    for approach in orig copa rebase upgrade; do
      scan_one "$name" "$approach"
    done
  done
}

health_all() {
  local name approach
  for name in rails node node-alpine; do
    want "$name" || continue
    for approach in orig copa rebase upgrade; do
      health_one "$name" "$approach" 3000
    done
  done
  for name in go-alpine go-debian go-distroless go-scratch; do
    want "$name" || continue
    for approach in orig copa rebase upgrade; do
      health_one "$name" "$approach" 8080
    done
  done
}

linkage() {
  mkdir -p "$RAW/linkage"
  docker run --rm --entrypoint ldd "$RUBY_OLD" /usr/local/bin/ruby >"$RAW/linkage/ruby.txt" 2>&1 || true
  docker run --rm --entrypoint ldd "$NODE_OLD" /usr/local/bin/node >"$RAW/linkage/node.txt" 2>&1 || true
  docker run --rm --entrypoint ldd "$NODE_ALPINE_OLD" /usr/local/bin/node >"$RAW/linkage/node-alpine.txt" 2>&1 || true
  file "$ROOT/apps/go/app" >"$RAW/linkage/go.txt"
}

need docker
need trivy
need copa
need crane
need python3

case "$STAGE" in
  locks) build_locks ;;
  build) build_images ;;
  scan) scan_all ;;
  patch) patch_images ;;
  health) health_all ;;
  linkage) linkage ;;
  summarize) python3 "$ROOT/scripts/summarize.py" "$RAW" "$ROOT/results/summary.json" ;;
  all)
    build_images
    linkage
    scan_all
    patch_images
    scan_all
    health_all
    python3 "$ROOT/scripts/summarize.py" "$RAW" "$ROOT/results/summary.json"
    ;;
  *)
    echo "usage: run.sh {locks|build|scan|patch|health|linkage|summarize|all} [name]" >&2
    exit 1
    ;;
esac
