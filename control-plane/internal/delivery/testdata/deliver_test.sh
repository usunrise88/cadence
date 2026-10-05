#!/bin/sh
# The shell test of deliver.sh (scripts/test-delivery.sh runs it inside the inference server's image, CPU only).
# /work holds what TestBundleOfAPromotion wrote with CADENCE_DELIVERY_BUNDLE_OUT: bundle/ (a promotion's bundle with an
# identity-backend model and a fixture smoke client), pin.pem (the throwaway key that signed it), other.pem (another
# key) and expected.txt (the record hash and the model directory's manifest). It starts the server with an empty
# repository in explicit model-control mode and runs the bundle against it: every refusal first, then the delivery.
set -u
IN=/work
REPO=/tmp/repo
mkdir -p "$REPO"
tritonserver --model-repository="$REPO" --model-control-mode=explicit --http-port=8000 --grpc-port=8001 \
	--metrics-port=8002 >/tmp/server.log 2>&1 &
i=0
until curl -fsS -o /dev/null localhost:8000/v2/health/live 2>/dev/null; do
	i=$((i + 1))
	if [ "$i" -gt 120 ]; then
		echo "the server did not start"
		cat /tmp/server.log
		exit 1
	fi
	sleep 1
done

read -r RECORD_HASH MANIFEST <"$IN/expected.txt"
MODEL=$(sed -n "s/^MODEL_NAME='\(.*\)'$/\1/p" "$IN/bundle/deliver.sh")
PASS=0
FAILED=0

fresh() {
	rm -rf /tmp/b
	cp -R "$IN/bundle" /tmp/b
}
# deliver PIN: runs the bundle in /tmp/b with PIN; stdout to /tmp/out, stderr to /tmp/err; answers its exit status.
deliver() {
	(cd /tmp/b && CADENCE_PIN="$1" sh deliver.sh) >/tmp/out 2>/tmp/err
}
ok() {
	echo "ok   $1"
	PASS=$((PASS + 1))
}
bad() {
	echo "FAIL $1"
	sed 's/^/     /' /tmp/err /tmp/out
	FAILED=$((FAILED + 1))
}
# refused NAME TEXT: the last run failed, printed no receipt and said TEXT.
refused() {
	if [ "$STATUS" -ne 0 ] && ! grep -q CADENCE-RECEIPT /tmp/out && grep -q "$2" /tmp/err; then ok "$1"; else bad "$1"; fi
}
ready() { curl -fsS -o /dev/null "localhost:8000/v2/models/$MODEL/ready" 2>/dev/null; }

# The bundle names /tmp/repo as its repository path (the fixture target's).
fresh
deliver /nonexistent/instance.pub
STATUS=$?
refused "no pin: stops and says how to install one" "no pinned instance key"

fresh
deliver "$IN/other.pem"
STATUS=$?
refused "another key pinned" "but this host pins"

fresh
sed -i 's/"stage":"canary"/"stage":"production"/' /tmp/b/record.json
deliver "$IN/pin.pem"
STATUS=$?
refused "record edited" "does not hash"

fresh
sed -i 's/"stage":"canary"/"stage":"production"/' /tmp/b/record.json
NEW=$(sha256sum </tmp/b/record.json | cut -c1-64)
sed -i "s/^RECORD_HASH='.*'$/RECORD_HASH='$NEW'/" /tmp/b/deliver.sh
deliver "$IN/pin.pem"
STATUS=$?
refused "record edited and the script's hash with it" "does not verify under the pinned key"

fresh
head -c 64 /dev/zero | openssl base64 -A >/tmp/b/record.sig
deliver "$IN/pin.pem"
STATUS=$?
refused "signature replaced" "does not verify under the pinned key"

fresh
cp "$IN/other.pem" /tmp/b/instance.pub
deliver "$IN/other.pem"
STATUS=$?
refused "the bundle's own key pinned instead of the instance's" "but this host pins"

fresh
printf '# changed\n' >>"/tmp/b/models/$MODEL/config.pbtxt"
deliver "$IN/pin.pem"
STATUS=$?
refused "model file changed" "does not match the record's files"

fresh
printf 'extra\n' >"/tmp/b/models/$MODEL/1/extra"
deliver "$IN/pin.pem"
STATUS=$?
refused "model file added" "does not match the record's files"

fresh
sed -i 's/	shalom$/	salaam/; s/	toda$/	merci/' /tmp/b/smoke/manifest.tsv
deliver "$IN/pin.pem"
STATUS=$?
refused "smoke check fails" "the smoke check passed 1 of 3"
if ready; then bad "the failed model was unloaded"; else ok "the failed model was unloaded"; fi

fresh
deliver "$IN/pin.pem"
STATUS=$?
LINE=$(grep '^CADENCE-RECEIPT ' /tmp/out)
# shellcheck disable=SC2086 # split the receipt into its fields
set -- $LINE
if [ "$STATUS" -eq 0 ] && [ "$#" -eq 7 ] && [ "$2" = 1 ] && [ "$3" = "$RECORD_HASH" ] && [ "$4" = "$MANIFEST" ] && [ "$5" = 3/3 ]; then
	ok "delivered: $LINE"
else
	bad "delivered (receipt: $LINE)"
fi
if ready; then ok "the new model serves"; else bad "the new model serves"; fi
if [ "$(cd "$REPO/$MODEL" && find . -type f | sed 's|^\./||' | LC_ALL=C sort | while IFS= read -r f; do sha256sum "$f"; done | sha256sum | cut -c1-64)" = "$MANIFEST" ]; then
	ok "the installed directory hashes to the record's manifest"
else
	bad "the installed directory hashes to the record's manifest"
fi

fresh
deliver "$IN/pin.pem"
STATUS=$?
if [ "$STATUS" -eq 0 ] && grep -q "already installed" /tmp/err && grep -q "^CADENCE-RECEIPT 1 $RECORD_HASH $MANIFEST 3/3 " /tmp/out; then
	ok "a second run finds the model installed and prints the same receipt"
else
	bad "a second run"
fi

echo "$PASS passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
