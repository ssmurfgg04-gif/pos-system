#!/usr/bin/env python3
"""E2E smoke test for the POS binary.

Usage: python3 scripts/smoke_test.py [base_url]
Default base_url: http://127.0.0.1:3000

41 checks against a running server seeded with demo data and
mpesa_env=mock. The sweeper (5s interval) completes STK payments after the
mock delay, exactly as it would against real Daraja stkpushquery.
"""
import json
import sys
import time
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
    return data(body).get("token"), st


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
                   body={"items": [{"productId": 1, "qty": 1}], "paymentMethod": "cash"})
    check("RBAC: designer cannot sell", st == 403, f"{st}")

    st, body = req("GET", "/api/v1/audit", cashier)
    check("RBAC: cashier cannot view audit", st == 403, f"{st}")

    # -- 3. Catalog ---------------------------------------------------------
    st, body = req("GET", "/api/v1/products", cashier)
    check("products list (cashier can read)", st == 200 and len(data(body)) >= 13, f"{st} n={len(data(body))}")

    p = get_product(cashier, "Classic Cotton Tee — Black")
    check("product search works", p is not None, "not found")

    st, body = req("GET", "/api/v1/products?categoryId=" + str(p["categoryId"]), cashier)
    check("category filter works", st == 200 and all(x["categoryId"] == p["categoryId"] for x in data(body)), f"{st}")

    st, body = req("GET", "/api/v1/products/low-stock", admin)
    check("low-stock report (admin)", st == 200, f"{st}")

    # -- 4. Cash checkout ----------------------------------------------------
    stock_before = p["stockQty"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qty": 2}],
        "paymentMethod": "cash",
        "clientUuid": "smoke-cash-1",
    })
    order = data(body)
    check("cash checkout -> PAID", st == 201 and order.get("status") == "PAID", f"{st} {body}")
    check("total is 2 x 550 = 1100.00 (VAT incl.)", order.get("totalCents") == 110000, f"{order.get('totalCents')}")

    p2 = get_product(cashier, "Classic Cotton Tee — Black")
    check("stock decremented 40 -> 38", p2["stockQty"] == stock_before - 2, f"{p2['stockQty']}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qty": 2}],
        "paymentMethod": "cash",
        "clientUuid": "smoke-cash-1",
    })
    check("idempotent replay returns same order", st == 201 and data(body)["id"] == order["id"], f"{st}")

    p3 = get_product(cashier, "Classic Cotton Tee — Black")
    check("replay did not double-deduct", p3["stockQty"] == stock_before - 2, f"{p3['stockQty']}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": 4, "qty": 99999}], "paymentMethod": "cash"})
    check("insufficient stock rejected 409", st == 409, f"{st}")

    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qty": 1, "unitPriceCents": 100}], "paymentMethod": "cash"})
    check("price override: cashier 403", st == 403, f"{st}")

    st, body = req("POST", "/api/v1/orders/checkout", admin, body={
        "items": [{"productId": p["id"], "qty": 1, "unitPriceCents": 1000}], "paymentMethod": "cash"})
    check("price override: admin honored", st == 201 and data(body)["totalCents"] == 1000, f"{st}")

    # -- 5. M-Pesa STK (mock + sweeper) --------------------------------------
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": p["id"], "qty": 1}],
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
        "items": [{"productId": 2, "qty": 1}],
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
        "items": [{"productId": 5, "qty": 1}],
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
        "items": [{"productId": 6, "qty": 1}], "paymentMethod": "mpesa", "paymentMode": "manual"})
    man2 = data(body)
    st, body = req("POST", f"/api/v1/orders/{man2['id']}/manual", cashier,
                   body={"receiptCode": "UC3HW8HDN2"})
    check("duplicate receipt code rejected 409", st == 409, f"{st}")
    # void that stuck order so reports stay tidy
    req("POST", f"/api/v1/orders/{man2['id']}/void", cashier, body={"reason": "test cleanup"})

    # -- 8. Offline sync ------------------------------------------------------
    sync_body = {"transactions": [
        {"items": [{"productId": 9, "qty": 2}], "paymentMethod": "cash", "clientUuid": "smoke-sync-1"},
        {"items": [{"productId": 10, "qty": 1}], "paymentMethod": "cash", "clientUuid": "smoke-sync-2"},
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
    before_void = wrist["stockQty"]
    st, body = req("POST", "/api/v1/orders/checkout", cashier, body={
        "items": [{"productId": wrist["id"], "qty": 3}], "paymentMethod": "cash"})
    void_order = data(body)
    st, body = req("POST", f"/api/v1/orders/{void_order['id']}/void", cashier,
                   body={"reason": "smoke void"})
    check("void succeeds", st == 200 and data(body)["status"] == "VOIDED", f"{st}")
    wrist_after = get_product(cashier, "Canvas Tote Bag")
    check("void restores stock", wrist_after["stockQty"] == before_void, f"{wrist_after['stockQty']} vs {before_void}")
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
        "items": [{"productId": 8, "qty": 1}], "paymentMethod": "cash", "clientUuid": "smoke-shift-sale"})
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

    # -- 12. Receipt HTML ---------------------------------------------------------
    st, raw = req("GET", f"/api/v1/orders/{order['id']}/receipt", cashier, raw=True)
    html = raw.decode()
    check("receipt HTML renders", st == 200 and "TOTAL" in html and order["number"] in html, f"{st}")

    # -- 13. Roles CRUD (dynamic RBAC) --------------------------------------------
    st, body = req("POST", "/api/v1/roles", admin, body={
        "name": "Stock Officer", "description": "inventory only",
        "permissions": ["products.view", "products.manage"]})
    check("custom role created", st == 201, f"{st} {body}")
    st, body = req("DELETE", "/api/v1/roles/1", admin)
    check("system role delete blocked 409", st == 409, f"{st}")
    st, body = req("PUT", "/api/v1/roles/2", admin, body={
        "name": "Cashier", "permissions": ["pos.sell", "pos.void", "orders.view", "payments.manual", "shifts.manage", "products.view"]})
    check("system role editable", st == 200, f"{st}")

    # -- 14. Design board ------------------------------------------------------------
    st, body = req("POST", "/api/v1/design", designer, body={
        "title": "Jersey concept", "productName": "Premium Heavyweight Tee",
        "customerName": "Acme FC", "notes": "two-color", "status": "queue"})
    job = data(body)
    check("design job created", st == 201 and job.get("id"), f"{st}")
    st, body = req("POST", f"/api/v1/design/{job['id']}/move", designer, body={"status": "in_progress"})
    check("design job moved", st == 200 and data(body)["status"] == "in_progress", f"{st}")

    # -- 15. Audit ---------------------------------------------------------------------
    st, body = req("GET", "/api/v1/audit", admin)
    entries = data(body)
    actions = {e["action"] for e in entries}
    check("audit log records key actions",
          st == 200 and "CHECKOUT_CASH" in actions and "ORDER_VOIDED" in actions and "PAYMENT_MANUAL" in actions,
          f"{sorted(actions)[:8]}")

    # -- 16. Settings masking ------------------------------------------------------------
    st, body = req("PUT", "/api/v1/settings", admin, body={
        "values": {"mpesa_consumer_secret": "top-secret", "store_name": "Smoke Shop"}})
    txt = json.dumps(body)
    check("settings update masks secrets", st == 200 and "top-secret" not in txt and "__SET__" in txt, f"{st}")
    st, body = req("PUT", "/api/v1/settings", admin, body={"values": {"jwt_secret": "nope"}})
    check("jwt_secret not writable via API", st == 400, f"{st}")

    # -- Summary ------------------------------------------------------------
    print(f"\n== {PASS} passed, {FAIL} failed ==")
    for f in FAILURES:
        print("  !! " + f)
    sys.exit(1 if FAIL else 0)


if __name__ == "__main__":
    main()
