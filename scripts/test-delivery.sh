#!/bin/sh
# The shell test of the delivery script (phase 5 · stream D3; docs/spec/02-domain-projects-registry.md "Delivery
# bundle"). Needs Docker. It
#   1. writes a fixture promotion's bundle with a throwaway key (go test ./internal/delivery, CADENCE_DELIVERY_BUNDLE_OUT),
#   2. runs shellcheck over the generated deliver.sh,
#   3. runs control-plane/internal/delivery/testdata/deliver_test.sh inside the inference server's image (CPU only,
#      an identity-backend model): every refusal (no pin, another pin, edited record, replaced signature, changed
#      model files, failed smoke check) and then the delivery and its receipt.
# Images: GO_IMAGE (golang:1.27), SHELLCHECK_IMAGE (koalaman/shellcheck:stable), SERVER_IMAGE (the staging server's
# image, defaults.yaml serving; nvcr.io/nvidia/tritonserver:26.07-py3).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO_IMAGE=${GO_IMAGE:-golang:1.27}
SHELLCHECK_IMAGE=${SHELLCHECK_IMAGE:-koalaman/shellcheck:stable}
SERVER_IMAGE=${SERVER_IMAGE:-nvcr.io/nvidia/tritonserver:26.07-py3}
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
chmod 0777 "$OUT"

docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod \
	-e CADENCE_DELIVERY_BUNDLE_OUT=/out -v "$OUT":/out -v "$ROOT":/src -w /src/control-plane "$GO_IMAGE" \
	go test -count=1 -run 'TestBundleOfAPromotion$' ./internal/delivery/
docker run --rm -v "$OUT/bundle":/b:ro "$SHELLCHECK_IMAGE" -S style /b/deliver.sh
docker run --rm -v "$OUT":/work:ro -v "$ROOT/control-plane/internal/delivery/testdata":/t:ro "$SERVER_IMAGE" \
	sh /t/deliver_test.sh
