#!/usr/bin/env bash
# Drives the stack running in Kubernetes over HTTP. Every code carries a run id, so a kept Postgres volume never collides.
# Needs: curl, python3. The stack must be up: make cluster-up
set -euo pipefail

API="${API:-http://127.0.0.1:5230}"
RABBIT="${RABBIT:-http://127.0.0.1:15720}"
RUN="$(date +%s)$((RANDOM % 900 + 100))"
JSON='content-type: application/json'

say() { printf '  %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
jget() { python3 -c "import sys,json; d=json.load(sys.stdin); print(d$1)"; }

ADMIN_EMAIL="${ADMIN_EMAIL:-admin@pledge.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-PledgeAdmin2026}"
CUSTOMER_PASSWORD="Smoke-Passw0rd1"
code_of() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
token_of() { python3 -c "import sys,json; print(json.load(sys.stdin)['accessToken'])"; }

open_pool() { # code openingCents -> prints nothing
  curl -sf -X POST "$API/api/pools" -H "$JSON" -H "$ADM" \
    -d "{\"code\":\"$1\",\"sponsor\":\"Smoke Treasury\",\"currency\":\"USD\",\"cause\":\"Main Cause\",\"openingCents\":$2}" >/dev/null \
    || fail "could not open pool $1"
}
send() { # code reference amountCents -> prints donation id
  curl -sf -X POST "$API/api/donations" -H "$JSON" -H "$CUS" \
    -d "{\"poolCode\":\"$1\",\"reference\":\"$2\",\"amountCents\":$3}" | jget "['id']"
}
donation_field() { curl -sf -H "$ADM" "$API/api/donations/$1" | jget "['$2']"; }
remaining() { curl -sf -H "$ADM" "$API/api/pools/$1" | jget "['availableCents']"; }

wait_for() { # description command... : polls up to 30s until the command succeeds
  local what="$1"; shift
  for _ in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "timed out waiting for $what"
}
status_is() { [ "$(donation_field "$1" status)" = "$2" ]; }
remaining_is() { [ "$(remaining "$1")" = "$2" ]; }
terminal() { case "$(donation_field "$1" status)" in Completed|Rejected|Refunded) return 0 ;; *) return 1 ;; esac; }
event_types() { curl -sf -H "$ADM" "$API/api/donations/$1/events" | python3 -c "import sys,json; print(' '.join(e['type'] for e in json.load(sys.stdin)))"; }
dlq_is() { [ "$(dlq_count)" = "$1" ]; }
dlq_count() { curl -sf -u platform:platform "$RABBIT/api/queues/%2F/receipts.work.dlq" | jget "['messages']"; }

curl -sf "$API/api/health" >/dev/null || fail "API is not healthy at $API; run: make cluster-up"

echo "0. authentication and ownership"
[ "$(code_of "$API/api/pools")" = 401 ] || fail "an anonymous request should be 401"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-Passw0rd\"}")" = 401 ] || fail "a wrong password should be 401"
ADM="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" | token_of)" || fail "the administrator could not sign in"
CUS="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"smoke-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)" || fail "could not register a customer"
OTHER="Authorization: Bearer $(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"other-$RUN@example.com\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -X POST "$API/api/pools" -H "$JSON" -H "$CUS" -d '{"code":"X"}')" = 403 ] || fail "a customer must not open pools"
open_pool "A-$RUN" 5
IDA="$(send "A-$RUN" "PAY-$RUN-OWN" 1)"
[ "$(code_of -H "$OTHER" "$API/api/donations/$IDA")" = 404 ] || fail "another customer must get 404 for someone else's donation"
[ "$(code_of -H "$CUS" "$API/api/donations/$IDA/events")" = 403 ] || fail "the event log is staff only"
[ "$(code_of -X POST -H "$OTHER" "$API/api/donations/$IDA/refund")" = 404 ] || fail "another customer must not refund it"
say "401 anonymous, 401 bad password, 403 customer opening a pool, 404 for another customer, 403 on the event log"

echo "0b. password reset ends the old sessions (needs the dev mailbox the demo stack turns on)"
RESET_EMAIL="reset-$RUN@example.com"
OLD="$(curl -sf -X POST "$API/api/auth/register" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"$CUSTOMER_PASSWORD\"}" | token_of)"
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 200 ] || fail "the new session should work"
[ "$(code_of -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"nobody-$RUN@example.com\"}")" = 202 ] || fail "an unknown address must look the same"
curl -sf -X POST "$API/api/auth/forgot-password" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\"}" >/dev/null || fail "forgot-password failed"
LINK="$(curl -sf "$API/api/dev/mailbox?to=$RESET_EMAIL" | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['link'])")"
link_param() { python3 -c "import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query)['$1'][0])" "$LINK"; }
[ "$(code_of -X POST "$API/api/auth/reset-password" -H "$JSON" -d "{\"userId\":\"$(link_param reset)\",\"token\":\"$(link_param token)\",\"newPassword\":\"Another-Passw0rd9\"}")" = 204 ] || fail "reset failed"
sleep 3 # the session version is cached for a moment
[ "$(code_of -H "Authorization: Bearer $OLD" "$API/api/auth/me")" = 401 ] || fail "the old access token should be revoked"
[ "$(code_of -X POST "$API/api/auth/login" -H "$JSON" -d "{\"email\":\"$RESET_EMAIL\",\"password\":\"Another-Passw0rd9\"}")" = 200 ] || fail "the new password should work"
say "reset link works once, the old access token is revoked, the new password signs in"

echo "1. input is validated at the door"
open_pool "V-$RUN" 1000000
for body in \
  "{\"poolCode\":\"V-$RUN\",\"reference\":\"\",\"amountCents\":1}" \
  "{\"poolCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"amountCents\":0}" \
  "{\"poolCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"amountCents\":5000001}" \
  "{\"poolCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"amountCents\":4294967297}" \
  "{\"poolCode\":\"V-$RUN\",\"reference\":\"R-$RUN\",\"amountCents\":9223372036854775807}"; do
  code="$(code_of -X POST "$API/api/donations" -H "$JSON" -H "$CUS" -d "$body")"
  [ "$code" = 400 ] || fail "expected 400 for $body, got $code"
done
say "empty, zero, over-limit and overflowing amounts are all 400"

echo "2. happy path"
open_pool "H-$RUN" 100000
ID="$(send "H-$RUN" "PAY-$RUN-1" 30000)"
wait_for "donation to be Completed" status_is "$ID" Completed
[ "$(donation_field "$ID" feeCents)" = 200 ] || fail "fee should be 50 cents + 0.5% of 30000 = 200"
[ "$(donation_field "$ID" matchedCents)" = 30000 ] || fail "the pool had plenty, so the whole 30000 should be matched"
[ "$(curl -sf -H "$ADM" "$API/api/receipts/$ID" | jget "['status']")" = Issued ] || fail "receipt not issued"
wait_for "available cents to be 70000" remaining_is "H-$RUN" 70000
TYPES="$(event_types "$ID")"
for t in match.reserve match.held receipt.issued; do
  case "$TYPES" in *"$t"*) ;; *) fail "Kafka log is missing $t (has: $TYPES)" ;; esac
done
say "completed, fee 200, matched 30000, available 70000, Kafka log has the three steps"

echo "3. a small pool, three concurrent donations: each completes, the pool matches 10100 in total and not a cent more"
open_pool "L-$RUN" 10100
TMP="$(mktemp -d)"
for n in A B C; do ( send "L-$RUN" "PAY-$RUN-$n" 10000 >"$TMP/$n" ) & done
wait
IDS=""
for n in A B C; do [ -s "$TMP/$n" ] || fail "donation $n was not accepted"; IDS="$IDS $(cat "$TMP/$n")"; done
rm -rf "$TMP"
for i in $IDS; do wait_for "donation $i to complete" terminal "$i"; done
COMPLETED=0; MATCHED=0; FULL=0; PARTIAL=0; NONE=0
for i in $IDS; do
  [ "$(donation_field "$i" status)" = Completed ] && COMPLETED=$((COMPLETED + 1))
  m="$(donation_field "$i" matchedCents)"
  MATCHED=$((MATCHED + m))
  if [ "$m" = 10000 ]; then FULL=$((FULL + 1)); elif [ "$m" = 0 ]; then NONE=$((NONE + 1)); else PARTIAL=$((PARTIAL + 1)); fi
done
[ "$COMPLETED" = 3 ] || fail "every donation should complete, got $COMPLETED"
[ "$MATCHED" = 10100 ] || fail "the matches should add up to exactly the pool (10100), got $MATCHED"
[ "$FULL" = 1 ] && [ "$PARTIAL" = 1 ] && [ "$NONE" = 1 ] || fail "expected one full, one partial and one empty match, got $FULL/$PARTIAL/$NONE"
[ "$(remaining "L-$RUN")" = 0 ] || fail "the pool should be empty"
say "3 completed, matched 10000 + 100 + 0 = 10100, pool at 0"

echo "4. poison: committed as rejected, dead-lettered, the match come back"
open_pool "P-$RUN" 50000
BEFORE="$(dlq_count)"
IDP="$(send "P-$RUN" "PAY-POISON-$RUN" 20000)"
wait_for "poison donation to be Rejected" status_is "$IDP" Rejected
wait_for "available cents to be restored to 50000" remaining_is "P-$RUN" 50000
wait_for "receipts.work.dlq to gain one message" dlq_is $((BEFORE + 1))
[ "$(curl -sf -H "$ADM" "$API/api/receipts/$IDP" | jget "['status']")" = Rejected ] || fail "rejection should be committed on the receipt row"
[ "$(donation_field "$IDP" matchReleasedAt)" != None ] || fail "donation should record the release"
say "rejected, dead-lettered (+1 in receipts.work.dlq), available back to 50000"

echo "4b. dead letters can be listed by staff only, and replayed"
[ "$(code_of -H "$CUS" "$API/api/admin/dead-letters")" = 403 ] || fail "dead letters are staff only"
curl -sf -H "$ADM" "$API/api/admin/dead-letters" | python3 -c "import sys,json; q={x['queue']:x for x in json.load(sys.stdin)}; assert q['receipts.work']['count']>=1, q" || fail "the dead-letter queue should list the poison message"
BEFORE_REPLAY="$(dlq_count)"
REPLAYED="$(curl -sf -X POST -H "$ADM" "$API/api/admin/dead-letters/receipts.work/replay?max=100" | jget "['replayed']")" || fail "replay failed"
[ "$REPLAYED" -ge 1 ] || fail "replay should have taken at least one message"
# The poison reference still fails for the same reason, so it is retried and dead-lettered again: replay is for fixed causes.
wait_for "the replayed poison message to be dead-lettered again" dlq_is "$BEFORE_REPLAY"
say "listed, replayed $REPLAYED, dead-lettered again because the cause is still there"

echo "5. refund races the final mark: the refund wins, the issued receipt stays"
open_pool "R-$RUN" 60000
WON=0
for attempt in 1 2 3; do
  IDR="$(send "R-$RUN" "PAY-$RUN-R$attempt" 20000)"
  # The receipt exists before the donation is marked Completed (the final mark is delayed in the demo cluster).
  wait_for "receipt to exist" curl -sf -H "$ADM" "$API/api/receipts/$IDR"
  if status_is "$IDR" Held; then WON=1; break; fi
  say "window missed on attempt $attempt (donation already Completed); the refund still has to hold; retrying"
  curl -sf -X POST -H "$CUS" "$API/api/donations/$IDR/refund" >/dev/null || true
  wait_for "donation to be Refunded" status_is "$IDR" Refunded
done
[ "$WON" = 1 ] || fail "never caught the donation between receipt issued and final mark"
curl -sf -X POST -H "$CUS" "$API/api/donations/$IDR/refund" >/dev/null || fail "refund was refused"
wait_for "donation to be Refunded" status_is "$IDR" Refunded
sleep 3 # longer than the final-mark delay: the late mark must not undo the refund
status_is "$IDR" Refunded || fail "the final mark overwrote the refund"
[ "$(curl -sf -H "$ADM" "$API/api/receipts/$IDR" | jget "['status']")" = Issued ] || fail "the issued receipt was rolled back"
[ "$(donation_field "$IDR" receiptNumber)" = None ] || fail "the late final mark should have been skipped"
wait_for "match to come back" remaining_is "R-$RUN" 60000
code="$(code_of -X POST -H "$CUS" "$API/api/donations/$IDR/refund")"
[ "$code" = 409 ] || fail "second refund should be 409, got $code"
say "donation Refunded, receipt still Issued, available 60000, second refund 409"

echo "golang-pledge smoke passed"
