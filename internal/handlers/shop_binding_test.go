package handlers_test

// v1.3.2 shop-binding tests (review 8.13 / P0#2).
//
// Per-shop user ids are autoincrements and COLLIDE: the first user in every
// shop is id 1. Sign-in must therefore bind to an EXPLICIT shop — never to
// a row-count heuristic. These tests build two shops whose owner accounts
// share username, password and user id, then prove:
//
//  1. password login with the same credentials in both shops answers 409
//     needsShop (pick one) instead of silently picking the "bigger" shop;
//  2. the retried login with an explicit shopId lands in THAT shop;
//  3. PIN login bound to shopId authenticates against that shop only —
//     the other shop's PIN is rejected even for the colliding id;
//  4. the legacy (no shopId) PIN path still resolves for old clients.

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func shopBindingSetup(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()
	t.Setenv("ALLOW_SIGNUP", "true")
	engine, _, _, _ := newTestServer(t)
	// Two shops; the SAME username+password+userId(1) exists in both.
	_, shopA := signup(t, engine, "bindingowner", "binding-pass-1", "Binding Shop A")
	w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1", "shopName": "Binding Shop B",
	})
	if w.Code != 201 {
		t.Fatalf("second signup: %d %s", w.Code, w.Body.String())
	}
	shopB := dataMap(t, w)["shop"].(map[string]any)["id"].(string)
	return engine, shopA, shopB
}

func TestLoginAmbiguousCredentialsRequiresShopChoice(t *testing.T) {
	engine, shopA, shopB := shopBindingSetup(t)

	// Identical credentials in both shops → 409 with the picker payload.
	w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1",
	})
	if w.Code != 409 {
		t.Fatalf("ambiguous login must 409, got %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"needsShop", "Binding Shop A", "Binding Shop B"} {
		if !strings.Contains(body, want) {
			t.Fatalf("409 payload missing %q: %s", want, body)
		}
	}
	// _ = shopA (used above through the payload check)
	_ = shopA

	// Explicit shopId resolves deterministically.
	w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1", "shopId": shopB,
	})
	if w.Code != 200 {
		t.Fatalf("explicit-shop login: %d %s", w.Code, w.Body.String())
	}
	user := dataMap(t, w)["user"].(map[string]any)
	if user["shopId"] != shopB {
		t.Fatalf("session bound to %v, want %s", user["shopId"], shopB)
	}

	// A shop the user does NOT belong to is refused (no discovery).
	w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1", "shopId": "sh-doesnotexist",
	})
	if w.Code != 404 {
		t.Fatalf("unknown shop must 404, got %d", w.Code)
	}
}

func TestPinLoginBoundToExplicitShop(t *testing.T) {
	engine, shopA, shopB := shopBindingSetup(t)

	// Give the colliding admins DIFFERENT PINs (id 1 in both shops).
	// Login to shop A first to set its PIN, then shop B.
	setPin := func(token, pin string) {
		t.Helper()
		w := do(t, engine, "PUT", "/api/v1/users/1/pin", token, map[string]any{"pin": pin})
		if w.Code != 200 {
			t.Fatalf("set pin %s: %d %s", pin, w.Code, w.Body.String())
		}
	}
	w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1", "shopId": shopA,
	})
	tokA := dataMap(t, w)["token"].(string)
	setPin(tokA, "1357")
	w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
		"username": "bindingowner", "password": "binding-pass-1", "shopId": shopB,
	})
	tokB := dataMap(t, w)["token"].(string)
	setPin(tokB, "9042")
	// PIN bound to shop B with shop B's PIN: lands in shop B.
	w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{
		"userId": 1, "pin": "9042", "shopId": shopB,
	})
	if w.Code != 200 {
		t.Fatalf("pin login (shopB pin): %d %s", w.Code, w.Body.String())
	}
	if u := dataMap(t, w)["user"].(map[string]any); u["shopId"] != shopB {
		t.Fatalf("pin session in %v, want %s", u["shopId"], shopB)
	}

	// Shop A's PIN against shop B's binding: refused.
	w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{
		"userId": 1, "pin": "1357", "shopId": shopB,
	})
	if w.Code != 401 {
		t.Fatalf("wrong-shop PIN must 401, got %d", w.Code)
	}

	// Shop B's PIN against shop A's binding: refused.
	w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{
		"userId": 1, "pin": "9042", "shopId": shopA,
	})
	if w.Code != 401 {
		t.Fatalf("wrong-shop PIN (A) must 401, got %d", w.Code)
	}

	// Legacy path (no shopId) still resolves for pre-1.3.2 clients.
	w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{
		"userId": 1, "pin": "1234",
	})
	if w.Code != 200 {
		t.Fatalf("legacy pin login: %d %s", w.Code, w.Body.String())
	}

	// pin-users rows carry the shop binding the client echoes back.
	w = do(t, engine, "GET", "/api/v1/auth/pin-users?shop="+shopA, tokA, nil)
	if w.Code != 200 {
		t.Fatalf("pin-users: %d %s", w.Code, w.Body.String())
	}
	usersList, _ := decode(t, w)["data"].([]any)
	if len(usersList) == 0 {
		t.Fatalf("pin-users returned no rows: %s", w.Body.String())
	}
	first, _ := usersList[0].(map[string]any)
	if first["shopId"] != shopA {
		t.Fatalf("pin-users shop binding = %v, want %s", first["shopId"], shopA)
	}
}
