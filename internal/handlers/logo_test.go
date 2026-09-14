package handlers_test

import (
        "bytes"
        "mime/multipart"
        "net/http"
        "net/http/httptest"
        "strings"
        "testing"
)

// Minimal 1x1 PNG (magic bytes + IHDR + IDAT + IEND).
var tinyPNG = []byte{
        0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
        0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
        0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
        0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
        0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
        0x54, 0x08, 0xD7, 0x63, 0xF8, 0xFF, 0xFF, 0x3F,
        0x00, 0x05, 0xFE, 0x02, 0xFE, 0xDC, 0xCC, 0x59,
        0xE7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
        0x44, 0xAE, 0x42, 0x60, 0x82,
}

func postLogo(t *testing.T, engine interface {
        ServeHTTP(http.ResponseWriter, *http.Request)
}, token, filename string, content []byte,
) *httptest.ResponseRecorder {
        t.Helper()
        var buf bytes.Buffer
        w := multipart.NewWriter(&buf)
        fw, err := w.CreateFormFile("logo", filename)
        if err != nil {
                t.Fatal(err)
        }
        if _, err := fw.Write(content); err != nil {
                t.Fatal(err)
        }
        w.Close()
        req := httptest.NewRequest("POST", "/api/v1/settings/logo", &buf)
        req.Header.Set("Content-Type", w.FormDataContentType())
        if token != "" {
                req.Header.Set("Authorization", "Bearer "+token)
        }
        rec := httptest.NewRecorder()
        engine.ServeHTTP(rec, req)
        return rec
}

func TestBrandLogoRoundTrip(t *testing.T) {
        t.Chdir(t.TempDir()) // logo file lands in an isolated cwd
        engine, admin, _, _ := newTestServer(t)

        // Absent logo: public GET 404s, branding carries no URL.
        w := do(t, engine, "GET", "/api/v1/settings/logo", "", nil)
        if w.Code != 404 {
                t.Fatalf("expected 404 with no logo, got %d", w.Code)
        }

        // Non-image upload rejected.
        w = postLogo(t, engine, admin, "evil.txt", []byte("not an image"))
        if w.Code != 400 {
                t.Fatalf("expected 400 for text upload, got %d: %s", w.Code, w.Body.String())
        }

        // Oversize upload rejected.
        w = postLogo(t, engine, admin, "big.png", make([]byte, 2<<20+1))
        if w.Code != 400 {
                t.Fatalf("expected 400 for oversize upload, got %d", w.Code)
        }

        // Unauthenticated upload rejected.
        w = postLogo(t, engine, "", "logo.png", tinyPNG)
        if w.Code != 401 {
                t.Fatalf("expected 401 without token, got %d", w.Code)
        }

        // Valid PNG round-trips with its content type.
        w = postLogo(t, engine, admin, "logo.png", tinyPNG)
        if w.Code != 200 {
                t.Fatalf("expected 200 for PNG upload, got %d: %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/settings/logo", "", nil)
        if w.Code != 200 {
                t.Fatalf("expected 200 fetching logo, got %d", w.Code)
        }
        if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
                t.Fatalf("expected image/png content type, got %q", ct)
        }
        if !bytes.Equal(w.Body.Bytes(), tinyPNG) {
                t.Fatal("served logo bytes differ from upload")
        }

        // Delete clears it; GET 404s again.
        w = do(t, engine, "DELETE", "/api/v1/settings/logo", admin, nil)
        if w.Code != 200 {
                t.Fatalf("expected 200 deleting logo, got %d", w.Code)
        }
        w = do(t, engine, "GET", "/api/v1/settings/logo", "", nil)
        if w.Code != 404 {
                t.Fatalf("expected 404 after delete, got %d", w.Code)
        }
}
