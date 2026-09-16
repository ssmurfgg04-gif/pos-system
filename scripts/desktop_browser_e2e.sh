#!/bin/bash
# Desktop-app browser E2E (single invocation — the server cannot survive
# across harness commands). Ref-based locators, no networkidle waits.
set -u
BIN=${1:-/tmp/lptest/ledgerpos}
DATA="$HOME/.local/share/LedgerPOS"
URL="http://127.0.0.1:8765"
SHOTS=${SHOTS:-/tmp/ledgerpos-shots}
AB="timeout 25 agent-browser"
PASS=0; FAIL=0
ck() { if [ "$1" = "0" ]; then PASS=$((PASS+1)); echo "PASS: $2"; else FAIL=$((FAIL+1)); echo "FAIL: $2"; fi }

pkill -f agent-browser 2>/dev/null; pkill -f "chrome.*headless" 2>/dev/null; sleep 2
rm -rf "$DATA"
"$BIN" > /tmp/desktop-e2e.log 2>&1 &
PID=$!
for i in $(seq 1 40); do curl -s -m 1 $URL/api/v1/health >/dev/null 2>&1 && break; sleep 0.25; done

$AB open "$URL" >/dev/null 2>&1
$AB set viewport 1440 900 >/dev/null 2>&1
sleep 2

# 1b. first-run gates, API contract (rotation + onboarding flags).
TOKEN=$(curl -s -m 2 -X POST $URL/api/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin123"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['token'])")
[ -n "$TOKEN" ] ; ck $? "seeded login issues token"
ME=$(curl -s -m 2 $URL/api/v1/me -H "Authorization: Bearer $TOKEN")
echo "$ME" | grep -q '"mustRotate":true' ; ck $? "seeded admin flagged mustRotate"
curl -s -m 2 $URL/api/v1/products -H "Authorization: Bearer $TOKEN" | grep -q 'rotation required' ; ck $? "gated endpoint 403s pre-rotation"
AID=$(echo "$ME" | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['id'])")
curl -s -m 2 -X PUT $URL/api/v1/users/$AID/password -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"password":"e2e-admin-1"}' | grep -q '"updated":true' ; ck $? "self rotation clears flag"
curl -s -m 2 $URL/api/v1/settings -H "Authorization: Bearer $TOKEN" | grep -q '"onboarding_done":"false"' ; ck $? "fresh box reports onboarding pending"
curl -s -m 2 -X PUT $URL/api/v1/settings -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"values":{"onboarding_done":"true"}}' | grep -q '"onboarding_done":"true"' ; ck $? "onboarding completes via API"
# Browser flow below uses the rotated password on an onboarded box.
E2E_PW="e2e-admin-1"

# 1. first-run hint
$AB snapshot -i > /tmp/snap-login.txt 2>&1
grep -qi "First run" /tmp/snap-login.txt ; ck $? "login shows first-run starter-login hint"
$AB screenshot $SHOTS/desk-01-login-firstrun.png >/dev/null 2>&1
U=e$(grep -o 'textbox "Username" \[required, ref=e[0-9]*\]' /tmp/snap-login.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
P=e$(grep -o 'textbox "Password" \[required, ref=e[0-9]*\]' /tmp/snap-login.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
S=e$(grep -o 'button "Sign in" \[ref=e[0-9]*\]' /tmp/snap-login.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
echo "refs: user=$U pass=$P submit=$S"

# 2. admin login → Quit button
$AB fill @$U "admin" >/dev/null 2>&1
$AB fill @$P "${E2E_PW:-admin123}" >/dev/null 2>&1
$AB click @$S >/dev/null 2>&1
sleep 3
$AB snapshot -i > /tmp/snap-admin.txt 2>&1
grep -qi "Quit application" /tmp/snap-admin.txt ; ck $? "admin topbar shows Quit button"
grep -qi "Demo mode" /tmp/snap-admin.txt ; [ $? -ne 0 ] ; ck $? "no demo pill (real backend)"
$AB screenshot $SHOTS/desk-02-admin-shell-quit.png >/dev/null 2>&1

# 3. quit via button (the real confirm dialog pauses the renderer until
# accepted — eval overrides can't reach it, so accept it properly).
Q=e$(grep -o 'button "Quit application" \[ref=e[0-9]*\]' /tmp/snap-admin.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
echo "quit ref: $Q"
$AB click @$Q >/dev/null 2>&1
$AB dialog accept >/dev/null 2>&1
sleep 3
$AB snapshot > /tmp/snap-stopped.txt 2>&1
kill -0 $PID 2>/dev/null; QALIVE=$?
# Overlay text is timing-sensitive (logout navigates to Login right after);
# accept it OR a dead process. A live process with no overlay fails below too.
grep -qi "has stopped" /tmp/snap-stopped.txt ; OVERLAY=$?
[ "$OVERLAY" = "0" ] || [ "$QALIVE" != "0" ] ; ck $? "quit overlay shown or app exited"
grep -qi "close this browser tab" /tmp/snap-stopped.txt ; OVERLAY2=$?
[ "$OVERLAY2" = "0" ] || [ "$QALIVE" != "0" ] ; ck $? "overlay: safe to close tab (or exited)"
$AB screenshot $SHOTS/desk-03-quit-overlay.png >/dev/null 2>&1
kill -0 $PID 2>/dev/null; DEAD=$?
[ "$DEAD" != "0" ] ; ck $? "app process exited after Quit click"
grep -q "quit requested by admin" "$DATA/logs/desktop.log" ; ck $? "quit audited"

# 4. relaunch → cashier has no Quit + no first-run hint
"$BIN" > /tmp/desktop-e2e2.log 2>&1 &
PID2=$!
for i in $(seq 1 40); do curl -s -m 1 $URL/api/v1/health >/dev/null 2>&1 && break; sleep 0.25; done
$AB open "$URL" >/dev/null 2>&1
sleep 2
# Cashier still on seeded creds → rotate via API so the browser exercises
# the steady-state shell (rotation itself is covered in section 1b).
CTOK=$(curl -s -m 2 -X POST $URL/api/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"cashier","password":"cashier123"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['token'])")
CID=$(curl -s -m 2 $URL/api/v1/me -H "Authorization: Bearer $CTOK" | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['id'])")
curl -s -m 2 -X PUT $URL/api/v1/users/$CID/password -H "Authorization: Bearer $CTOK" -H 'Content-Type: application/json' -d '{"password":"e2e-cashier-1"}' | grep -q '"updated":true' ; ck $? "cashier rotation via API"
$AB snapshot -i > /tmp/snap-login2.txt 2>&1
grep -qi "First run" /tmp/snap-login2.txt ; [ $? -ne 0 ] ; ck $? "second run: no first-run hint"
U2=e$(grep -o 'textbox "Username" \[required, ref=e[0-9]*\]' /tmp/snap-login2.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
P2=e$(grep -o 'textbox "Password" \[required, ref=e[0-9]*\]' /tmp/snap-login2.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
S2=e$(grep -o 'button "Sign in" \[ref=e[0-9]*\]' /tmp/snap-login2.txt | grep -o 'ref=e[0-9]*' | grep -o '[0-9]\+$')
$AB fill @$U2 "cashier" >/dev/null 2>&1
$AB fill @$P2 "e2e-cashier-1" >/dev/null 2>&1
$AB click @$S2 >/dev/null 2>&1
sleep 3
$AB snapshot -i > /tmp/snap-cashier.txt 2>&1
grep -qi "Quit application" /tmp/snap-cashier.txt ; [ $? -ne 0 ] ; ck $? "cashier has no Quit button"
$AB screenshot $SHOTS/desk-04-cashier-noquit.png >/dev/null 2>&1

kill $PID2 2>/dev/null
$AB close >/dev/null 2>&1
pkill -f agent-browser 2>/dev/null
echo "-----"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = "0" ]
