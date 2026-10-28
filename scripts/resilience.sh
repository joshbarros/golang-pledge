#!/usr/bin/env bash
# What Kubernetes buys: kill an API replica and the RabbitMQ pod in the middle of a match and check that nothing is lost or
# done twice. Donations are accepted, the pool is never matched beyond what it holds, and every message the broker held
# comes back from its persistent volume. Needs the cluster from `make cluster-up`.
set -euo pipefail

API="${API:-http://127.0.0.1:5230}"
CTX="${CTX:-kind-golang-pledge}"
K="kubectl --context $CTX -n pledge"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'
say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }
# every call retries a few times: for a moment after a pod dies the Service may still send a request to it
retry() { for _ in $(seq 1 20); do if out="$("$@" 2>/dev/null)"; then printf '%s' "$out"; return 0; fi; sleep 1; done; return 1; }

login() { retry curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"admin@pledge.local\",\"password\":\"${ADMIN_PASSWORD:-PledgeAdmin2026}\"}" | token_of; }
register() { retry curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$1-$RUN@example.com\",\"password\":\"Resil-Passw0rd1\"}" | token_of; }
ADM="Authorization: Bearer $(login)"
A="Authorization: Bearer $(register a)"
B="Authorization: Bearer $(register b)"
C="Authorization: Bearer $(register c)"

echo "1. a matching pool of 10,000 cents, and two donations of 6,000 in flight"
retry curl -sf -X POST "$API/api/pools" -H "$JSON" -H "$ADM" -d "{\"code\":\"RES-$RUN\",\"sponsor\":\"Acme\",\"currency\":\"USD\",\"cause\":\"Resilience\",\"openingCents\":10000}" >/dev/null || fail "could not open the pool"
donate() { retry curl -sf -X POST "$API/api/donations" -H "$JSON" -H "$1" -d "{\"poolCode\":\"RES-$RUN\",\"reference\":\"INV-$RUN-$2\",\"amountCents\":6000}" | jget "['id']"; }
IDA="$(donate "$A" a)"; IDB="$(donate "$B" b)"

echo "2. kill an API replica and the RabbitMQ pod while the donations are still in flight"
$K delete "$($K get pods -l app.kubernetes.io/name=api -o name | head -1)" --wait=false | sed 's/^/  /'
$K delete pod rabbitmq-0 --wait=false | sed 's/^/  /'
IDC="$(donate "$C" c)" # accepted even now: the donation and its outbox row are one local transaction
say "a third donation was accepted while the broker was down"

echo "3. everything comes back: all three go through, and the matches add up to exactly the pool"
$K rollout status statefulset/rabbitmq --timeout=180s >/dev/null
$K rollout status deployment/api --timeout=180s >/dev/null
field() { body="$(retry curl -sf -H "$1" "$API/api/donations/$2")" && printf '%s' "$body" | jget "['$3']" || echo unknown; }
for _ in $(seq 1 240); do
  if [ "$(field "$A" "$IDA" status)" = Completed ] && [ "$(field "$B" "$IDB" status)" = Completed ] && [ "$(field "$C" "$IDC" status)" = Completed ]; then break; fi
  sleep 1
done
MA="$(field "$A" "$IDA" matchedCents)"; MB="$(field "$B" "$IDB" matchedCents)"; MC="$(field "$C" "$IDC" matchedCents)"
for pair in "$A|$IDA" "$B|$IDB" "$C|$IDC"; do
  [ "$(field "${pair%%|*}" "${pair##*|}" status)" = Completed ] || fail "a donation did not complete"
done
[ "$((MA + MB + MC))" = 10000 ] || fail "the matches must add up to the pool (10000), got $MA + $MB + $MC"
SORTED="$(printf '%s\n' "$MA" "$MB" "$MC" | sort -n | tr '\n' ' ')"
[ "$SORTED" = "0 4000 6000 " ] || fail "expected 0, 4000 and 6000, got $SORTED"
[ "$(retry curl -sf -H "$ADM" "$API/api/pools/RES-$RUN" | jget "['availableCents']")" = 0 ] || fail "the pool must be empty, and never negative"
RECEIPTS=0
for id in "$IDA" "$IDB" "$IDC"; do
  if curl -sf -H "$ADM" "$API/api/receipts/$id" >/dev/null 2>&1; then RECEIPTS=$((RECEIPTS + 1)); fi
done
[ "$RECEIPTS" = 3 ] || fail "three receipts must exist, found $RECEIPTS"
say "matched $MA, $MB and $MC (sum 10000), pool empty, 3 receipts"

echo "4. a refund gives back exactly what was matched, not what was donated"
for pair in "$A|$MA|$IDA" "$B|$MB|$IDB" "$C|$MC|$IDC"; do
  IFS='|' read -r WHO MATCHED ID <<<"$pair"
  if [ "$MATCHED" = 4000 ]; then
    retry curl -sf -X POST -H "$WHO" "$API/api/donations/$ID/refund" >/dev/null || fail "refund failed"
    for _ in $(seq 1 60); do [ "$(retry curl -sf -H "$ADM" "$API/api/pools/RES-$RUN" | jget "['availableCents']")" = 4000 ] && break; sleep 1; done
    [ "$(retry curl -sf -H "$ADM" "$API/api/pools/RES-$RUN" | jget "['availableCents']")" = 4000 ] || fail "exactly 4000 cents must be back (the donation was 6000)"
  fi
done
say "the 4000 that were matched came back"
echo "resilience passed"
