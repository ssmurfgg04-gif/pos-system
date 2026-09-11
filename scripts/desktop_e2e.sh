#!/bin/bash
# Desktop-mode E2E: launch with no args (as a double-clicked app would),
# verify auto-setup, login, cash sale, admin quit, graceful exit.
set -u
BIN=${1:-/tmp/ledgerpos-test}
DATA="$HOME/.local/share/LedgerPOS"
URL="http://127.0.0.1:8765"

rm -rf "$DATA"

# Stale instances from earlier runs sometimes squat the desktop port range
# and hijack every check below. Kill anything listening on 127.0.0.1:8765-8799.
while read -r line; do
  p=$(echo "$line" | grep -oE '127\.0\.0\.1:[0-9]+' | head -1 | cut -d: -f2)
  pid=$(echo "$line" | grep -oE 'pid=[0-9]+' | head -1 | cut -d= -f2)
  if [ -n "$p" ] && [ -n "$pid" ] && [ "$p" -ge 8765 ] && [ "$p" -le 8799 ]; then
    echo "pre-clean: killing stale pid $pid on port $p"
    kill "$pid" 2>/dev/null
  fi
done < <(ss -tlnpH 2>/dev/null)
sleep 0.5

"$BIN" > /tmp/desktop-run.log 2>&1 &
PID=$!
PASS=0; FAIL=0
ck() { if [ "$1" = "0" ]; then PASS=$((PASS+1)); echo "PASS: $2"; else FAIL=$((FAIL+1)); echo "FAIL: $2"; fi }

# the app writes the port it picked into app.port — trust that, not a guess
for i in $(seq 1 40); do [ -s "$DATA/app.port" ] && break; sleep 0.25; done
PORT=$(cat "$DATA/app.port" 2>/dev/null)
[ -n "$PORT" ] ; ck $? "app.port readable (picked $PORT)"
URL="http://127.0.0.1:${PORT:-8765}"

# wait for readiness
for i in $(seq 1 40); do curl -s -m 1 $URL/api/v1/health >/dev/null 2>&1 && break; sleep 0.25; done
curl -s -m 2 $URL/api/v1/health | grep -q '"ok"' ; ck $? "health answers on $URL"

# desktop info
curl -s -m 2 $URL/api/v1/system/desktop | grep -q '"desktop":true' ; ck $? "desktop status endpoint"
curl -s -m 2 $URL/api/v1/system/desktop | grep -q '"firstRun":true' ; ck $? "first-run flag set"

# data dir auto-created
[ -f "$DATA/pos.db" ] ; ck $? "SQLite DB auto-created in user data dir"
[ -f "$DATA/app.port" ] ; ck $? "port file written"
[ -d "$DATA/logs" ] ; ck $? "log dir written"

# login
TOKEN=$(curl -s -m 3 -X POST $URL/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])" 2>/dev/null)
[ -n "$TOKEN" ] ; ck $? "admin login"

# cash sale
PROD=$(curl -s -m 3 $URL/api/v1/products -H "Authorization: Bearer $TOKEN" | python3 -c "import sys,json;print(json.load(sys.stdin)['data'][0]['id'])" 2>/dev/null)
SALE=$(curl -s -m 5 -X POST $URL/api/v1/orders/checkout -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"productId\":$PROD,\"qty\":2}],\"paymentMethod\":\"cash\",\"clientUuid\":\"desk-e2e-1\"}")
echo "$SALE" | grep -q '"PAID"' ; ck $? "cash sale completes (PAID)"

# second launch while running = single instance, just opens browser, exits 0
"$BIN" > /tmp/desktop-run2.log 2>&1
RC=$?
sleep 0.5
curl -s -m 2 $URL/api/v1/health >/dev/null ; ck $? "second launch defers to running instance"
grep -q "already running" "$DATA/logs/desktop.log" ; ck $? "second launch logged single-instance"
[ "$RC" = "0" ] ; ck $? "second launch exit code 0"

# unauthenticated quit is rejected
CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 3 -X POST $URL/api/v1/system/quit)
[ "$CODE" = "401" ] ; ck $? "quit without auth rejected (401)"

# admin quit
curl -s -m 3 -X POST $URL/api/v1/system/quit -H "Authorization: Bearer $TOKEN" | grep -q quitting ; ck $? "admin quit accepted"
sleep 2
kill -0 $PID 2>/dev/null; DEAD=$?
[ "$DEAD" != "0" ] ; ck $? "process exited after quit"
curl -s -m 1 $URL/api/v1/health >/dev/null 2>&1; LIVE=$?
[ "$LIVE" != "0" ] ; ck $? "port closed after quit"
grep -q "quit requested by admin" "$DATA/logs/desktop.log" ; ck $? "quit audited in log"

echo "-----"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ]
