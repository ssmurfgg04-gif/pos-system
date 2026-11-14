#!/usr/bin/env python3
"""E2E smoke test for the POS binary.

Usage: python3 scripts/smoke_test.py [base_url]
Default base_url: http://127.0.0.1:3000

115 checks against a running server seeded with demo data and
mpesa_env=mock. The sweeper (5s interval) completes STK payments after the
mock delay, exactly as it would against real Daraja stkpushquery.
"""
import json
import sys
import time
from datetime import datetime, timedelta
import urllib.request
import urllib.parse
import urllib.error

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:3000"
PASS = 0
FAIL = 0
FAILURES = []


def check(name, cond, detail=""):
    global PASS, FAIL
    if cond:
        PASS += 1
        print(f"  ok  {name}")
    else:
        FAIL += 1
        FAILURES.append(f"{name}: {detail}")
        print(f" FAIL {name}  {detail}")


def req(method, path, token=None, body=None, raw=False):
    url = BASE + path
    data = None
    headers = {}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    r = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            payload = resp.read()
            if raw:
                return resp.status, payload
            return resp.status, json.loads(payload) if payload else {}
    except urllib.error.HTTPError as e:
        payload = e.read()
        try:
            return e.code, json.loads(payload) if payload else {}
        except json.JSONDecodeError:
            return e.code, {"raw": payload.decode(errors="replace")}


def data(body):
    return body.get("data", body)


def login(user, pw):
    st, body = req("POST", "/api/v1/auth/login", body={"username": user, "password": pw})
    tok = data(body).get("token")
    # Seeded logins start flagged for rotation — take over the account like
    # the onboarding wizard does, so every later check runs in steady state.
    if tok:
        mst, mbody = req("GET", "/api/v1/me", tok)
        me = data(mbody)
        if mst == 200 and me.get("mustRotate"):
            rid = me.get("id")
            pst, _ = req("PUT", f"/api/v1/users/{rid}/password", tok,
                         body={"password": "smoke-" + user + "-1"})
            if pst == 200:
                _, body = req("POST", "/api/v1/auth/login",
                              body={"username": user, "password": "smoke-" + user + "-1"})
                tok = data(body).get("token")
    return tok, st


def get_product(token, name):
    st, body = req("GET", "/api/v1/products?search=" + urllib.parse.quote(name), token)
    items = data(body)
    for p in items:
        if name in p["name"]:
            return p
    return None


def wait_for_order(token, order_id, want_status, timeout=25):
    """Poll until the order reaches want_status (sweeper-driven)."""
    deadline = time.time() + timeout
    order = None
    while time.time() < deadline:
        st, body = req("GET", f"/api/v1/orders/{order_id}", token)
        if st == 200:
            order = data(body)
            if order["status"] == want_status:
                return order
        time.sleep(1)
    return order


def main():
    print(f"== POS E2E smoke test against {BASE} ==")

    # -- 1. Infrastructure -------------------------------------------------
    st, body = req("GET", "/api/v1/health")
    check("health responds", st == 200 and body.get("status") == "ok", f"{st} {body}")

    st, body = req("GET", "/api/v1/branding")
    b = data(body)
    check("branding is public white-label data",
          st == 200 and b.get("app_name") and "jwt_secret" not in json.dumps(body),
          f"{st}")

    admin, st = login("admin", "admin123")
    check("admin login", st == 200 and admin, f"{st}")
    cashier, st = login("cashier", "cashier123")
    check("cashier login", st == 200 and cashier, f"{st}")
    designer, st = login("designer", "designer123")
    check("designer login", st == 200 and designer, f"{st}")

    # The smoke drives the auto-succeeding mock STK provider on purpose.
    # Real tills refuse it (ALLOW_MOCK_PAYMENTS + settings gate), so the
    # test explicitly opts in through the admin settings API. buni_enabled
    # is the local kill-switch: without it a fresh box inherits the fleet's
    # buni_via_cloud row and the STK push leaves for the Buni proxy, which
    # (rightly) refuses a smoke device — the mock rail never gets the leg.
    st, body = req("PUT", "/api/v1/settings", admin, body={"values": {"mpesa_env": "mock", "buni_enabled": "false"}})
    check("mock provider opt-in (settings)", st == 200, f"{st}")

    st, body = req("POST", "/api/v1/auth/login", body={"username": "admin", "password": "wrong"})
    check("bad password rejected 401", st == 401, f"{st}")

    st, body = req("GET", "/api/v1/auth/pin-users")
    pin_users = data(body)
    check("pin-users lists staff", st == 200 and len(pin_users) >= 3, f"{st} n={len(pin_users)}")

    admin_id = next((u["id"] for u in pin_users if u["fullName"] == "System Admin"), None)
    st, body = req("POST", "/api/v1/auth/pin", body={"userId": admin_id, "pin": "1234"})
    check("PIN login works", st == 200 and data(body).get("token"), f"{st}")

    st, body = req("POST", "/api/v1/auth/pin", body={"userId": admin_id, "pin": "0000"})
    check("wrong PIN rejected", st in (401, 423), f"{st}")

    for _ in range(5):
        st, body = req("POST", "/api/v1/auth/pin", body={"userId": admin_id, "pin": "0000"})
    check("PIN lockout escalates", st == 423, f"{st} {body}")
    # Reset for later tests (wait out lock is too slow; admin token already in hand).

    st, body = req("GET", "/api/v1/me", admin)
    check("me returns principal with permissions",
          st == 200 and len(data(body).get("permissions", [])) >= 15, f"{st}")

    # -- 2. RBAC ------------------------------------------------------------
    st, body = req("POST", "/api/v1/products", cashier,
                   body={"name": "Hack", "categoryId": 1, "priceCents": 1})
    check("RBAC: cashier cannot create products", st == 403, f"{st}")

    st, body = req("POST", "/api/v1/orders/checkout", designer,
                   body={"items": [{"productId": 1, "qtyMilli": 1000}], "paymentMethod": "cash"})
    check("RBAC: designer cannot sell", st == 403, f"{st}")

    st, body = req("GET", "/api/v1/audit", cashier)
    check("RBAC: cashier cannot view audit", st == 403, f"{st}")

    # -- 3. Catalog ---------------------------------------------------------
    st, body = req("GET", "/api/v1/products", cashier)
    check("products list (cashier can read)", st == 200 and len(data(body)) >= 13, f"{st} n={len(data(body))}")

    p = get_product(cashier, "Classic Cotton Tee, Black")
    check("product search works", p is not None, "not found")

    st, body = req("GET", "/api/v1/products?categoryId=" + str(p["categoryId"]), cashier)
    check("category filter works", st == 200 and all(x["categoryId"] == p["categoryId"] for x in data(body)), f"{st}")

    st, body = req("GET", "/api/v1/products/low-stock", admin)
    check("low-stock report (admin)", st == 200, f"{st}")

    # -- 4. Cash checkout ----------------------------------------------------
    stock_before = p["stockQtyMilli"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qtyMilli": 2000}],
        "paymentMethod": "cash",
        "clientUuid": "smoke-cash-1",
    })
    order = data(body)
    check("cash checkout -> PAID", st == 201 and order.get("status") == "PAID", f"{st} {body}")
    check("total is 2 x 550 = 1100.00 (VAT incl.)", order.get("totalCents") == 110000, f"{order.get('totalCents')}")

    p2 = get_product(cashier, "Classic Cotton Tee, Black")
    check("stock decremented 40 -> 38", p2["stockQtyMilli"] == stock_before - 2000, f"{p2['stockQtyMilli']}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qtyMilli": 2000}],
        "paymentMethod": "cash",
        "clientUuid": "smoke-cash-1",
    })
    check("idempotent replay returns same order", st == 201 and data(body)["id"] == order["id"], f"{st}")

    p3 = get_product(cashier, "Classic Cotton Tee, Black")
    check("replay did not double-deduct", p3["stockQtyMilli"] == stock_before - 2000, f"{p3['stockQtyMilli']}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 4, "qtyMilli": 99999000}], "paymentMethod": "cash"})
    check("insufficient stock rejected 409", st == 409, f"{st}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qtyMilli": 1000, "unitPriceCents": 100}], "paymentMethod": "cash"})
    check("price override: cashier 403", st == 403, f"{st}")

    st, body = req("POST", "/api/v1/orders/checkout", admin, body={
        "items": [{"productId": p["id"], "qtyMilli": 1000, "unitPriceCents": 1000}], "paymentMethod": "cash"})
    check("price override: admin honored", st == 201 and data(body)["totalCents"] == 1000, f"{st}")

    # -- 5. M-Pesa STK (mock + sweeper) --------------------------------------
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qtyMilli": 1000}],
        "paymentMethod": "mpesa", "paymentMode": "stk",
        "customerPhone": "0722123456",
    })
    mp_order = data(body)
    check("STK checkout -> PENDING", st == 201 and mp_order.get("status") == "PENDING", f"{st} {mp_order.get('status')}")
    pay0 = mp_order["payments"][0] if mp_order.get("payments") else {}
    check("phone normalized to 2547…", pay0.get("phone") == "254722123456", f"{pay0.get('phone')}")

    completed = wait_for_order(cashier, mp_order["id"], "PAID", timeout=30)
    check("sweeper completes STK order (<=30s)", completed and completed["status"] == "PAID",
          f"{completed and completed['status']}")
    receipt = ""
    if completed:
        for pay in completed.get("payments", []):
            if pay.get("mpesaReceipt"):
                receipt = pay["mpesaReceipt"]
    check("M-Pesa receipt code stored (10-char)", len(receipt) == 10, f"{receipt!r}")

    # -- 6. Callback: amount mismatch flags discrepancy ----------------------
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 2, "qtyMilli": 1000}],
        "paymentMethod": "mpesa", "paymentMode": "stk", "customerPhone": "0711222334",
    })
    mm_order = data(body)
    mm_pay = mm_order["payments"][0]
    st, body = req("POST", "/api/v1/payments/mpesa/callback", body={
        "Body": {"stkCallback": {
            "MerchantRequestID": "smoke-1",
            "CheckoutRequestID": mm_pay["checkoutRequestId"],
            "ResultCode": 0, "ResultDesc": "success",
            "CallbackMetadata": {"Item": [
                {"Name": "Amount", "Value": 100.0},
                {"Name": "MpesaReceiptNumber", "Value": "ZZ11YY22XX"},
            ]}}}})
    st, body = req("GET", f"/api/v1/orders/{mm_order['id']}", cashier)
    mm_after = data(body)
    check("callback amount mismatch -> discrepancy flag",
          mm_after.get("discrepancy") is True, f"{mm_after.get('discrepancy')}")

    # -- 7. Manual receipt entry ---------------------------------------------
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 5, "qtyMilli": 1000}],
        "paymentMethod": "mpesa", "paymentMode": "manual",
        "clientUuid": "smoke-manual-1",
    })
    man_order = data(body)
    check("manual checkout -> PENDING (awaits code)", st == 201 and man_order["status"] == "PENDING", f"{st}")

    st, body = req("POST", f"/api/v1/orders/{man_order['id']}/manual", cashier,
                   body={"receiptCode": "SHORT"})
    check("invalid receipt format 422", st == 422, f"{st}")

    st, body = req("POST", f"/api/v1/orders/{man_order['id']}/manual", cashier,
                   body={"receiptCode": "uc3hw8hdn2"})
    check("manual confirm -> PAID", st == 200 and data(body)["status"] == "PAID", f"{st} {body}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 6, "qtyMilli": 1000}], "paymentMethod": "mpesa", "paymentMode": "manual"})
    man2 = data(body)
    st, body = req("POST", f"/api/v1/orders/{man2['id']}/manual", cashier,
                   body={"receiptCode": "UC3HW8HDN2"})
    check("duplicate receipt code rejected 409", st == 409, f"{st}")
    # void that stuck order so reports stay tidy
    req("POST", f"/api/v1/orders/{man2['id']}/void", cashier, body={"reason": "test cleanup"})

    # -- 8. Offline sync ------------------------------------------------------
    sync_body = {"transactions": [
        {"items": [{"productId": 9, "qtyMilli": 2000}], "paymentMethod": "cash", "clientUuid": "smoke-sync-1"},
        {"items": [{"productId": 10, "qtyMilli": 1000}], "paymentMethod": "cash", "clientUuid": "smoke-sync-2"},
    ]}
    st, body = req("POST", "/api/v1/sync", cashier, body=sync_body)
    results = data(body)
    check("offline sync accepts batch", st == 200 and len(results) == 2 and all(not r.get("error") for r in results), f"{st} {results}")
    first_ids = [r["orderId"] for r in results]

    st, body = req("POST", "/api/v1/sync", cashier, body=sync_body)
    results2 = data(body)
    check("sync replay is idempotent", [r["orderId"] for r in results2] == first_ids, f"{[r['orderId'] for r in results2]}")

    # -- 9. Void --------------------------------------------------------------
    wrist = get_product(cashier, "Canvas Tote Bag")
    before_void = wrist["stockQtyMilli"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": wrist["id"], "qtyMilli": 3000}], "paymentMethod": "cash"})
    void_order = data(body)
    st, body = req("POST", f"/api/v1/orders/{void_order['id']}/void", cashier,
                   body={"reason": "smoke void"})
    check("void succeeds", st == 200 and data(body)["status"] == "VOIDED", f"{st}")
    wrist_after = get_product(cashier, "Canvas Tote Bag")
    check("void restores stock", wrist_after["stockQtyMilli"] == before_void, f"{wrist_after['stockQtyMilli']} vs {before_void}")
    st, body = req("POST", f"/api/v1/orders/{void_order['id']}/void", cashier, body={"reason": "again"})
    check("double void rejected 409", st == 409, f"{st}")

    # -- 10. Shifts ------------------------------------------------------------
    st, body = req("POST", "/api/v1/shifts/open", cashier, body={"openingFloatCents": 200000})
    check("shift opens with float", st == 201, f"{st}")
    st, body = req("POST", "/api/v1/shifts/open", cashier, body={"openingFloatCents": 0})
    check("double-open rejected 409", st == 409, f"{st}")
    # cash sales so far for cashier: let's close counting expected exactly.
    st, body = req("GET", "/api/v1/shifts/current", cashier)
    cur = data(body)
    # make one more cash sale to have deterministic expected
    req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 8, "qtyMilli": 1000}], "paymentMethod": "cash", "clientUuid": "smoke-shift-sale"})
    st, body = req("POST", "/api/v1/shifts/close", cashier, body={"countedCents": cur["openingFloatCents"] + 15000})
    closed = data(body)
    check("shift close computes expected/variance",
          st == 200 and closed["expectedCents"] == cur["openingFloatCents"] + 15000 and closed["varianceCents"] == 0,
          f"expected={closed.get('expectedCents')}")

    # -- 11. Reports ------------------------------------------------------------
    st, body = req("GET", "/api/v1/reports/daily", admin)
    r = data(body)
    check("daily report aggregates", st == 200 and r["ordersPaid"] >= 5 and r["salesCents"] > 0, f"{st}")
    check("report splits cash vs mpesa", r["cashCents"] > 0 and r["mpesaCents"] > 0, f"cash={r['cashCents']} mpesa={r['mpesaCents']}")
    check("7-day series present", len(r["series"]) == 7, f"{len(r['series'])}")

    # -- 12. Receipt HTML (v1.6.0 content audit parity: the browser
    #        receipt reconciles like the paper + branded-image ones) ------
    st, raw = req("GET", f"/api/v1/orders/{order['id']}/receipt", cashier, raw=True)
    html = raw.decode()
    check("receipt HTML renders", st == 200 and "TOTAL" in html and order["number"] in html, f"{st}")
    check("receipt HTML audit rows", "Subtotal" in html and "VAT" in html
          and ("Served by" in html or "Customer" in html), f"sub={'Subtotal' in html} vat={'VAT' in html}")
    check("receipt HTML honest legs", "PAID: Cash" in html, "completed cash leg must say PAID")

    # -- 13. Roles CRUD (dynamic RBAC) --------------------------------------------
    st, body = req("POST", "/api/v1/roles", admin, body={
        "name": "Stock Officer", "description": "inventory only",
        "permissions": ["products.view", "products.manage"]})
    check("custom role created", st == 201, f"{st} {body}")
    st, body = req("DELETE", "/api/v1/roles/1", admin)
    check("system role delete blocked 409", st == 409, f"{st}")
    # Role ids are seed-order dependent — always resolve Cashier by name.
    st, body = req("GET", "/api/v1/roles", admin)
    cashier_role_id = next((r["id"] for r in data(body) if r["name"] == "Cashier"), None)
    st, body = req("PUT", f"/api/v1/roles/{cashier_role_id}", admin, body={
        "name": "Cashier", "permissions": ["pos.sell", "pos.void", "orders.view", "orders.notify",
                                            "payments.manual", "shifts.manage", "products.view",
                                            "customers.view", "quotes.view", "returns.manage", "expenses.view"]})
    check("system role editable", st == 200, f"{st} {body}")

    # -- 14. Design board ------------------------------------------------------------
    # The front desk (orders.assign) creates jobs; designers work them.
    st, body = req("POST", "/api/v1/design", admin, body={
        "title": "Jersey concept", "productName": "Premium Heavyweight Tee",
        "customerName": "Acme FC", "notes": "two-color", "status": "queue"})
    job = data(body)
    check("design job created", st == 201 and job.get("id"), f"{st}")
    # The assigner moves their own job; the designer's scope is enforced too.
    st, body = req("POST", f"/api/v1/design/{job['id']}/move", admin, body={"status": "in_progress"})
    check("design job moved", st == 200 and data(body)["status"] == "in_progress", f"{st}")
    st, body = req("POST", f"/api/v1/design/{job['id']}/move", designer, body={"status": "delivered"})
    check("designer blocked from others' jobs (403)", st == 403, f"{st}")
    st, body = req("POST", "/api/v1/design", designer, body={
        "title": "Nope", "productName": "Tee", "status": "queue"})
    check("designer cannot create jobs (403)", st == 403, f"{st}")

    # -- 15. Audit ---------------------------------------------------------------------
    st, body = req("GET", "/api/v1/audit", admin)
    entries = data(body)
    actions = {e["action"] for e in entries}
    check("audit log records key actions",
          st == 200 and "CHECKOUT_CASH" in actions and "ORDER_VOIDED" in actions and "PAYMENT_MANUAL" in actions,
          f"{sorted(actions)[:8]}")

    # -- 16. Settings masking ------------------------------------------------------------
    # whatsapp_api_key is the live secret-class key (Daraja keys retired v1.4.4).
    # auto-send is explicitly OFF here so the fake token can never reach the
    # real whapi endpoint from a smoke run.
    st, body = req("PUT", "/api/v1/settings", admin, body={
        "values": {"whatsapp_api_key": "top-secret", "whatsapp_auto_send": "false", "store_name": "Smoke Shop"}})
    txt = json.dumps(body)
    check("settings update masks secrets", st == 200 and "top-secret" not in txt and "__SET__" in txt, f"{st}")
    st, body = req("PUT", "/api/v1/settings", admin, body={"values": {"jwt_secret": "nope"}})
    check("jwt_secret not writable via API", st == 400, f"{st}")

    # -- 17. Tab for any customer + WhatsApp admin queue (v1.5.0) ---------
    st, body = req("POST", "/api/v1/customers", admin, body={
        "name": "Smoke Zero Limit", "phone": "0710000001", "creditLimitCents": 0})
    zero_cust = data(body)
    tab_prod = get_product(cashier, "Classic Cotton Tee, Black")
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": tab_prod["id"], "qtyMilli": 1000}], "paymentMethod": "account",
        "customerId": zero_cust["id"], "clientUuid": "smoke-tab-zero"})
    check("tab charge succeeds for a ZERO-limit customer (v1.5.0 policy)", st == 201, f"{st} {body}")
    tab_order = data(body)
    check("zero-limit tab is PENDING with an account leg",
          tab_order["status"] == "PENDING" and tab_order["payments"][0]["method"] == "account", f"{tab_order['status']}")
    st, body = req("POST", f"/api/v1/orders/{tab_order['id']}/void", admin, body={"reason": "smoke tab"})
    check("void reverses the zero-limit tab", st == 200, f"{st}")

    st, body = req("GET", "/api/v1/whatsapp/jobs?limit=50", admin)
    check("whatsapp jobs list (admin, one-place view)", st == 200 and isinstance(data(body).get("jobs"), list), f"{st}")
    st, body = req("GET", "/api/v1/whatsapp/jobs", cashier)
    check("whatsapp jobs list refuses cashier 403", st == 403, f"{st}")
    st, body = req("POST", "/api/v1/whatsapp/test", admin, body={"phone": "12345"})
    check("whatsapp test rejects garbage phone 422", st == 422, f"{st}")

    # -- 18. Orders query + receipt watermark (v1.6.0) ---------------------
    # The Orders page's server-side date/cashier filters + pagination, and
    # the receipt-image -> verify-receipt watermark roundtrip.
    st, body = req("GET", "/api/v1/orders/cashiers", admin)
    cashiers = data(body)
    check("orders cashiers roster (admin)", st == 200 and isinstance(cashiers, list) and len(cashiers) >= 1
          and all("id" in c and "name" in c for c in cashiers), f"{st}")
    st, _ = req("GET", "/api/v1/orders/cashiers", cashier)
    check("orders cashiers roster refuses cashier 403", st == 403, f"{st}")

    eat_today = time.strftime("%Y-%m-%d", time.gmtime(time.time() + 3 * 3600))
    st, body = req("GET", f"/api/v1/orders?from={eat_today}&to={eat_today}&limit=200", admin)
    todays = data(body)
    check("orders date filter (EAT today) includes today's sale",
          st == 200 and any(o["id"] == order["id"] for o in todays), f"{st} n={len(todays)}")
    st, body = req("GET", "/api/v1/orders?from=2030-01-01&to=2030-01-02", admin)
    check("orders future window is empty", st == 200 and data(body) == [], f"{st}")
    st, body = req("GET", "/api/v1/orders?cashierId=999999", admin)
    check("orders unknown cashier filter is empty", st == 200 and data(body) == [], f"{st}")

    st, raw = req("GET", f"/api/v1/orders/{order['id']}/receipt-image", admin, raw=True)
    check("receipt image renders (PNG)", st == 200 and raw[:8] == b"\x89PNG\r\n\x1a\n"
          and len(raw) > 2000, f"{st} len={len(raw)}")

    # Verify roundtrip: the image the Share button carries, handed back
    # like a customer's "proof", must verify as this shop's own.
    boundary = "smokev160"
    part = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"proof.png\"\r\n"
            f"Content-Type: image/png\r\n\r\n").encode() + raw + f"\r\n--{boundary}--\r\n".encode()
    url = BASE + "/api/v1/whatsapp/verify-receipt"
    r = urllib.request.Request(url, data=part, method="POST", headers={
        "Authorization": "Bearer " + admin, "Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            vst, vbody = resp.status, json.loads(resp.read())
    except urllib.error.HTTPError as e:
        vst, vbody = e.code, json.loads(e.read() or b"{}")
    v = data(vbody)
    check("watermark verify: genuine receipt passes",
          vst == 200 and v.get("valid") is True and v.get("orderId") == order["id"]
          and v.get("totalMatchesOrder") is True and v.get("number") == order["number"], f"{vst} {vbody}")

    part = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"fake.png\"\r\n"
            f"Content-Type: image/png\r\n\r\nnot-a-receipt\r\n--{boundary}--\r\n").encode()
    r = urllib.request.Request(url, data=part, method="POST", headers={
        "Authorization": "Bearer " + admin, "Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            vst = resp.status
    except urllib.error.HTTPError as e:
        vst = e.code
    check("watermark verify: fabricated image fails 422", vst == 422, f"{vst}")

    r = urllib.request.Request(url, data=part, method="POST", headers={
        "Authorization": "Bearer " + cashier, "Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            vst = resp.status
    except urllib.error.HTTPError as e:
        vst = e.code
    check("watermark verify refuses cashier 403", vst == 403, f"{vst}")

    # Share-via-WhatsApp server leg, on its own checkout (the customer
    # phone auto-captures the CRM record the send needs): no token →
    # 409 fallback (the UI then shares the image from the device);
    # token set → 200 + queued 2547…; re-send allowed ("I didn't get it").
    # (The secrets-class check in section 9 legitimately set a token —
    # clear it first so the no-token refusal is really tested.)
    st, _ = req("PUT", "/api/v1/settings", admin, body={"values": {"whatsapp_api_key": "__CLEAR__"}})
    check("clear token for no-gateway test", st == 200, f"{st}")
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qtyMilli": 1000}],
        "paymentMethod": "cash",
        "clientUuid": "smoke-wa-send-1",
        "customerPhone": "0733444555",
        "customerName": "WA Smoke",
    })
    wa_order = data(body)
    check("wa-send checkout -> PAID", st == 201 and wa_order.get("status") == "PAID", f"{st} {body}")
    st, body = req("POST", f"/api/v1/orders/{wa_order['id']}/whatsapp-send", cashier, body={})
    check("whatsapp-send without token 409 + fallback",
          st == 409 and data(body).get("fallback") is True, f"{st} {body}")
    st, _ = req("PUT", "/api/v1/settings", admin, body={"values": {
        "whatsapp_api_key": "smoke-tok",
        "whatsapp_api_url": "http://127.0.0.1:9",  # test seam: dead end, no live gateway call
    }})
    check("save whatsapp token (dead-end gateway)", st == 200, f"{st}")
    st, body = req("POST", f"/api/v1/orders/{wa_order['id']}/whatsapp-send", cashier, body={})
    queued = data(body)
    check("whatsapp-send queues the branded image (200, 2547…)",
          st == 200 and queued.get("queued") is True and str(queued.get("phone", "")).startswith("2547"), f"{st} {body}")
    st, body = req("POST", f"/api/v1/orders/{wa_order['id']}/whatsapp-send", cashier, body={})
    check("whatsapp-send re-send allowed (I didn't get it)", st == 200, f"{st}")
    st, _ = req("PUT", "/api/v1/settings", admin, body={"values": {
        "whatsapp_api_key": "__CLEAR__", "whatsapp_api_url": "__CLEAR__"}})
    check("clear smoke whatsapp token", st == 200, f"{st}")

    # -- v1.6.2: host-side WhatsApp app opener (contact preloaded, no share
    # sheet). The CASHIER token must be accepted — that is the whole point
    # of the permission widening (orders.notify OR pos.sell on this route,
    # Cashier preset + v21 backfill on legacy till roles).
    st, body = req("POST", "/api/v1/whatsapp-open", cashier, body={
        "phone": "0711 222 333", "text": "Thank you for your purchase!"})
    check("whatsapp-open: cashier allowed (200) + phone normalized 2547…",
          st == 200 and "phone=254711222333" in str(data(body).get("url", "")), f"{st} {body}")
    st, body = req("POST", "/api/v1/whatsapp-open", None, body={
        "phone": "254711222333", "text": "x"})
    check("whatsapp-open: anonymous 401", st == 401, f"{st}")


    # -- v1.6.3: retail expansion (stock ledger, returns, expenses,
    # wholesale tiers). Drives the real endpoints end-to-end.
    st, body = req("GET", "/api/v1/expenses/types", admin)
    check("expense types seeded (>=8)", st == 200 and len(data(body)) >= 8, f"{st} {len(data(body)) if st==200 else body}")
    exp_type_id = data(body)[0]["id"] if st == 200 else 0
    st, body = req("POST", "/api/v1/expenses", cashier, body={
        "expenseTypeId": exp_type_id, "amountCents": 3000, "method": "CASH", "note": "smoke airtime"})
    check("expenses: cashier create 403", st == 403, f"{st}")
    st, body = req("POST", "/api/v1/expenses", admin, body={
        "expenseTypeId": exp_type_id, "amountCents": 12500, "method": "MPESA",
        "reference": "SMK123", "note": "smoke internet"})
    exp_number = str(data(body).get("number", ""))
    check("expenses: admin create 201 EXP- number", st == 201 and exp_number.startswith("EXP-"), f"{st} {body}")
    st, body = req("GET", "/api/v1/expenses", cashier)
    check("expenses: cashier view 200", st == 200, f"{st}")
    st, body = req("GET", "/api/v1/reports/daily", admin)
    check("daily report books expensesCents >= 12500",
          st == 200 and data(body).get("expensesCents", 0) >= 12500, f"{st} {data(body).get('expensesCents') if st==200 else body}")
    check("daily report nets netSalesCents",
          st == 200 and "netSalesCents" in data(body) and "stockValueCents" in data(body), f"{st}")

    # Stock ledger: the cash checkouts earlier in this smoke left SALE rows.
    st, body = req("GET", "/api/v1/stock-movements?limit=50", admin)
    kinds = {m.get("kind") for m in data(body)} if st == 200 else set()
    check("stock ledger has SALE rows from this smoke's checkouts", st == 200 and "SALE" in kinds, f"{st} kinds={kinds}")
    st, body = req("GET", "/api/v1/returns", cashier)
    check("returns: cashier lists (orders.view)", st == 200, f"{st}")
    st, body = req("POST", "/api/v1/returns", designer, body={
        "orderId": 1, "lines": [{"orderItemId": 1, "qtyMilli": 1000}]})
    check("returns: designer 403 (no returns.manage)", st == 403, f"{st}")

    # Returns: fresh cash order, return one line, stock restocks, over-return 4xx.
    st, body = req("GET", "/api/v1/products", admin)
    prod1 = next(p for p in data(body) if p["id"] == 1)
    stock_before = prod1["stockQtyMilli"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 1, "qtyMilli": 3000}], "paymentMethod": "cash"})
    ret_order = data(body)
    check("v163 return-flow checkout -> PAID", st == 201 and ret_order.get("status") == "PAID", f"{st} {body}")
    item_id = ret_order["items"][0]["id"]
    st, body = req("POST", "/api/v1/returns", cashier, body={
        "orderId": ret_order["id"], "lines": [{"orderItemId": item_id, "qtyMilli": 1000}],
        "method": "CASH", "reason": "smoke return"})
    ret_doc = data(body)
    check("returns: cashier posts RET- (201)", st == 201 and str(ret_doc.get("number", "")).startswith("RET-"), f"{st} {body}")
    st, body = req("GET", "/api/v1/products", admin)
    prod1b = next(p for p in data(body) if p["id"] == 1)
    check("returns: restock landed (stock +1)", prod1b["stockQtyMilli"] == stock_before - 3000 + 1000,
          f"{prod1b['stockQtyMilli']} vs {stock_before}")
    st, body = req("GET", "/api/v1/stock-movements?limit=20", admin)
    kinds = {m.get("kind") for m in data(body)} if st == 200 else set()
    check("stock ledger has RETURN_RESTOCK", "RETURN_RESTOCK" in kinds, f"kinds={kinds}")
    st, body = req("POST", "/api/v1/returns", cashier, body={
        "orderId": ret_order["id"], "lines": [{"orderItemId": item_id, "qtyMilli": 3000}], "method": "CASH"})
    check("returns: over-return rejected 4xx", st in (400, 409, 422), f"{st} {body}")

    # Wholesale tier + per-product low-stock threshold persist.
    st, body = req("PUT", "/api/v1/products/2", admin, body={
        "name": "Smoke Wholesale Item", "priceCents": 20000, "costCents": 10000,
        "wholesalePriceCents": 16000, "lowStockUnits": 7, "stockQtyMilli": 6000})
    check("products: wholesale + low-stock fields persist", st == 200, f"{st} {body}")
    st, body = req("GET", "/api/v1/products", admin)
    prod2 = next(p for p in data(body) if p["id"] == 2)
    check("products: readback wholesalePriceCents=16000 lowStockUnits=7",
          prod2.get("wholesalePriceCents") == 16000 and prod2.get("lowStockUnits") == 7, f"{prod2}")
    st, body = req("POST", "/api/v1/customers", admin, body={
        "name": "Smoke Bulk Buyer", "phone": "0733999888", "priceTier": "wholesale"})
    check("customers: wholesale tier persists", st == 201 and data(body).get("priceTier") == "wholesale", f"{st} {body}")

    # ---- v1.7.0 — the shopkeeper's ledger --------------------------------
    # Line cost snapshot: a paid sale freezes cost, daily report shows margin.
    st, body = req("POST", "/api/v1/products", admin, body={
        "name": "Smoke Margin Item", "sku": "SMOKE-MARGIN", "priceCents": 25000,
        "costCents": 10000, "stockQtyMilli": 50000})
    check("v1.7.0: product with cost created", st in (200, 201), f"{st} {body}")
    margin_pid = data(body)["id"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": margin_pid, "qtyMilli": 3000}], "paymentMethod": "cash"})
    check("v1.7.0: checkout ok", st == 201, f"{st} {body}")
    items = data(body).get("items") or []
    check("v1.7.0: order line cost snapshot = 10000",
          items and items[0].get("costCents") == 10000, f"{items[:1]}")
    # The report buckets by EAST-AFRICA calendar day (UTC+3): a sale at
    # 21:xx UTC is already tomorrow in EAT — shift the stamp before slicing.
    day = (datetime.fromisoformat((data(body).get("createdAt") or "").replace("Z", "+00:00")) + timedelta(hours=3)).strftime("%Y-%m-%d") if data(body).get("createdAt") else ""
    st, body = req("GET", f"/api/v1/reports/daily?date={day}", admin)
    rep = data(body) if st == 200 else {}
    check("v1.7.0: daily report cogs includes 30000",
          rep.get("cogsCents", 0) >= 30000, f"cogs={rep.get('cogsCents')}")
    check("v1.7.0: daily report grossProfit/margin present",
          rep.get("grossProfitCents", 0) > 0 and rep.get("marginPct", 0) > 0,
          f"profit={rep.get('grossProfitCents')} margin={rep.get('marginPct')}")

    # Opening balance + soft reversal.
    st, body = req("POST", "/api/v1/customers", admin, body={"name": "Smoke Opening"})
    smoke_cust = data(body)["id"] if st in (200, 201) else 0
    st, body = req("POST", f"/api/v1/customers/{smoke_cust}/opening-balance", admin,
                   body={"amountCents": 5000, "note": "old book"})
    check("v1.7.0: opening balance 200 + balance 5000",
          st == 200 and data(body).get("balanceCents") == 5000, f"{st} {body}")
    st, body = req("GET", f"/api/v1/customers/{smoke_cust}/ledger", admin)
    rows = data(body) if st == 200 else []
    opening = next((r for r in rows if r.get("kind") == "opening"), None)
    check("v1.7.0: opening ledger row has balanceAfter=5000",
          opening is not None and opening.get("balanceAfter") == 5000, f"{opening}")
    st, body = req("POST", f"/api/v1/customers/{smoke_cust}/ledger/{opening['id']}/reverse", admin,
                   body={"reason": "smoke test reversal"})
    check("v1.7.0: reversal 200 + balance back to 0",
          st == 200 and data(body).get("balanceCents") == 0, f"{st} {body}")
    st, body = req("POST", f"/api/v1/customers/{smoke_cust}/ledger/{opening['id']}/reverse", admin,
                   body={"reason": "double"})
    check("v1.7.0: double reversal 4xx", st in (400, 409), f"{st}")

    # Disposal: guarded deduction + report (v1.8.0 milli quantities).
    st, body = req("POST", f"/api/v1/products/{margin_pid}/dispose", admin,
                   body={"qtyMilli": 4000, "reason": "spoilt", "note": "smoke fridge died"})
    check("v1.7.0: disposal 200 + stock 43000 milli",
          st == 200 and data(body).get("stockQtyMilli") == 43000, f"{st} {body}")
    st, body = req("POST", f"/api/v1/products/{margin_pid}/dispose", admin,
                   body={"qtyMilli": 999000, "reason": "spoilt"})
    check("v1.7.0: overstock disposal 4xx", st in (400, 409), f"{st}")
    st, body = req("GET", "/api/v1/reports/disposals", admin)
    dis = data(body) if st == 200 else []
    row = next((r for r in dis if r.get("productName") == "Smoke Margin Item"), None)
    check("v1.7.0: disposal report row loss=40000",
          row is not None and row.get("lossCents") == 40000, f"{row}")

    # ---- v1.8.0: fractional quantities + batch/expiry FEFO -----------------
    st, body = req("POST", "/api/v1/products", admin, body={
        "name": "Smoke Sugar", "sku": "SMOKE-SUGAR", "categoryId": 1,
        "priceCents": 20000, "costCents": 14000, "stockQtyMilli": 10000,
        "unit": "kg", "allowFraction": True})
    sugar_pid = data(body)["id"] if st in (200, 201) else 0
    check("v1.8.0: kg product created", st in (200, 201) and sugar_pid > 0, f"{st} {body}")
    st, body = req("POST", "/api/v1/orders/checkout", admin, body={
        "items": [{"productId": sugar_pid, "qtyMilli": 250}], "paymentMethod": "cash"})
    check("v1.8.0: 0.25 kg sale 201 + line 5000",
          st in (200, 201) and data(body)["items"][0]["lineTotalCents"] == 5000, f"{st} {body}")
    st2, body2 = req("GET", "/api/v1/products", admin)
    sugar2 = next((p for p in data(body2) if p.get("id") == sugar_pid), {})
    check("v1.8.0: fractional sale deducts 250 milli (9750 left)",
          sugar2.get("stockQtyMilli") == 9750, f"{sugar2.get('stockQtyMilli')}")
    st, body = req("POST", "/api/v1/orders/checkout", admin, body={
        "items": [{"productId": margin_pid, "qtyMilli": 1500}], "paymentMethod": "cash"})
    check("v1.8.0: whole-unit product rejects fractional qty", st in (400, 422), f"{st} {body}")

    # Batch/expiry FEFO.
    st, body = req("POST", "/api/v1/products", admin, body={
        "name": "Smoke Yoghurt", "sku": "SMOKE-YOG", "categoryId": 1,
        "priceCents": 12000, "costCents": 8000, "stockQtyMilli": 0, "trackBatches": True})
    yog_pid = data(body)["id"] if st in (200, 201) else 0
    st, body = req("POST", f"/api/v1/products/{yog_pid}/batches", admin, body={
        "batchNo": "SMK-FAR", "expiryDate": "2099-01-01", "qtyMilli": 10000, "costCents": 8000})
    check("v1.8.0: goods-in far lot 201", st == 201, f"{st} {body}")
    st, body = req("POST", f"/api/v1/products/{yog_pid}/batches", admin, body={
        "batchNo": "SMK-NEAR", "expiryDate": "2098-06-01", "qtyMilli": 5000, "costCents": 8000})
    check("v1.8.0: goods-in near lot 201", st == 201, f"{st} {body}")
    st, body = req("POST", "/api/v1/orders/checkout", admin, body={
        "items": [{"productId": yog_pid, "qtyMilli": 12000}], "paymentMethod": "cash"})
    check("v1.8.0: 12-unit sale on lots 201", st in (200, 201), f"{st} {body}")
    st, body = req("GET", f"/api/v1/products/{yog_pid}/batches", admin)
    lots = {b.get("batchNo"): b for b in (data(body) if st == 200 else [])}
    check("v1.8.0: FEFO drains NEAR lot first (0 left, EXHAUSTED)",
          lots.get("SMK-NEAR", {}).get("qtyRemainingMilli") == 0 and lots.get("SMK-NEAR", {}).get("status") == "EXHAUSTED",
          f"{lots.get('SMK-NEAR')}")
    check("v1.8.0: FEFO leaves FAR lot 3000",
          lots.get("SMK-FAR", {}).get("qtyRemainingMilli") == 3000, f"{lots.get('SMK-FAR')}")
    st, body = req("GET", "/api/v1/reports/expiry?days=3650", admin)
    exp = data(body) if st == 200 else {}
    soon_all = (exp.get("expired") or []) + (exp.get("soon") or [])
    smk = [r for r in soon_all if r.get("batchId") == lots.get("SMK-FAR", {}).get("id")]
    check("v1.8.0: expiry report lists remaining lot with value 24000",
          len(smk) == 1 and smk[0].get("valueCents") == 24000, f"{smk}")
    st, body = req("GET", "/api/v1/system/health", admin)
    h = data(body) if st == 200 else {}
    check("v1.8.0: health batch invariant holds (drift 0)",
          st == 200 and h.get("batchDriftCount") == 0, f"{h.get('batchDriftCount')} {h.get('batchDrift')}")

    # System health self-audit.
    st, body = req("GET", "/api/v1/system/health", admin)
    h = data(body) if st == 200 else {}
    check("v1.7.0: system health 200 schemaVersion=28 indexesOk",
          st == 200 and h.get("schemaVersion") == 28 and h.get("indexesOk") is True,
          f"{st} v={h.get('schemaVersion')} idx={h.get('indexesOk')}")
    st, body = req("GET", "/api/v1/system/health", cashier)
    check("v1.7.0: system health cashier 403", st == 403, f"{st}")

    # -- Summary ------------------------------------------------------------
    print(f"\n== {PASS} passed, {FAIL} failed ==")
    for f in FAILURES:
        print("  !! " + f)
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()
