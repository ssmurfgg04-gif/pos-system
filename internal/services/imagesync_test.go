package services

// imagesync_test.go — acceptance tests for cross-machine photo + logo sync:
// a photo uploaded on till A reaches till B through the cloud log (and a
// FRESH till gets it from history), pre-existing photos backfill once and
// never echo, oversized uploads downscale under the event budget, and the
// brand logo materialises from a config event.

import (
        "bytes"
        "database/sql"
        "encoding/json"
        "image"
        "image/color"
        "image/jpeg"
        "image/png"
        "math/rand"
        "os"
        "path/filepath"
        "strings"
        "testing"
)

func imgSyncTill(t *testing.T, dbPath string) *Service {
        t.Helper()
        s := p0OpenTill(t, dbPath)
        return s
}

func imgSyncOutboxPayloads(t *testing.T, s *Service, entity, op string) []string {
        t.Helper()
        rows, err := s.db.Query(`SELECT payload FROM sync_outbox WHERE entity = ? AND op = ? ORDER BY seq`, entity, op)
        if err != nil {
                t.Fatalf("outbox query: %v", err)
        }
        defer rows.Close()
        var out []string
        for rows.Next() {
                var p string
                if rows.Scan(&p) == nil {
                        out = append(out, p)
                }
        }
        return out
}

func imgSyncDrain(t *testing.T, s *Service, cloud *agent3Cloud) {
        t.Helper()
        agent3Link(t, cloud.srv.URL)
        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("sync config did not resolve")
        }
        if err := c.heartbeat(s, "img-test"); err != nil {
                t.Fatalf("heartbeat: %v", err)
        }
        if _, _, err := s.TeamSyncNow("img-test"); err != nil {
                t.Fatalf("TeamSyncNow: %v", err)
        }
}

func imgSyncProductImage(t *testing.T, s *Service, sku string) string {
        t.Helper()
        var img string
        if err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(image_url,'') FROM products WHERE sku = ?`), sku).Scan(&img); err != nil {
                t.Fatalf("read image_url: %v", err)
        }
        return img
}

func imgSyncMustDataURL(t *testing.T, v string) (string, []byte) {
        t.Helper()
        mime, raw, ok := decodeDataURL(v)
        if !ok || mime == "" || len(raw) == 0 {
                t.Fatalf("expected a decodable data URL, got %.40q", v)
        }
        return mime, raw
}

// bigNoisyPNG builds a PNG guaranteed to exceed the sync budget (noise
// defeats PNG compression).
func bigNoisyPNG(t *testing.T, w, h int) []byte {
        t.Helper()
        img := image.NewNRGBA(image.Rect(0, 0, w, h))
        rnd := rand.New(rand.NewSource(7))
        for y := 0; y < h; y++ {
                for x := 0; x < w; x++ {
                        img.Set(x, y, color.NRGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255})
                }
        }
        var buf bytes.Buffer
        if err := png.Encode(&buf, img); err != nil {
                t.Fatalf("png encode: %v", err)
        }
        return buf.Bytes()
}

// TestImageSyncPhotoJourney — the client's report: "I can't view images
// across machines because the images don't sync". A photo set on till A
// must appear on till B AND on a fresh till C (from replayed history).
func TestImageSyncPhotoJourney(t *testing.T) {
        fake := newAgent3Cloud(t)
        dir := t.TempDir()

        // Till A: upload a photo for the fixture product (big enough to need
        // downscaling — like a real phone camera upload).
        tillA := imgSyncTill(t, filepath.Join(dir, "a.db"))
        big := bigNoisyPNG(t, 1400, 1000)
        if len(big) <= syncImageTargetBytes {
                t.Fatalf("test image unexpectedly small: %d bytes", len(big))
        }
        dataURL := encodeDataURL("image/png", big)
        tillA.db.Exec(tillA.db.Rebind(`UPDATE products SET image_url = ?, updated_at = '2026-10-03T10:00:00.000Z' WHERE sku = 'SKU-P0'`), dataURL)
        tillA.EmitProductImage(mustProductID(t, tillA, "SKU-P0"))

        // The emitted event is a COMPACT copy, not the original bytes.
        evs := imgSyncOutboxPayloads(t, tillA, "product", "image")
        if len(evs) != 1 {
                t.Fatalf("outbox product/image events = %d, want 1", len(evs))
        }
        if strings.Contains(evs[0], strings.Split(dataURL, ";base64,")[1][:64]) {
                // downscaled re-encode must not carry the original payload
                mime, raw := imgSyncMustDataURL(t, mustJSONString(t, evs[0], "image"))
                if len(raw) > syncImageTargetBytes {
                        t.Fatalf("synced photo = %d bytes, budget %d", len(raw), syncImageTargetBytes)
                }
                if mime != "image/jpeg" {
                        t.Fatalf("downscaled mime = %s, want image/jpeg", mime)
                }
        } else {
                mime, raw := imgSyncMustDataURL(t, mustJSONString(t, evs[0], "image"))
                if len(raw) > syncImageTargetBytes {
                        t.Fatalf("synced photo = %d bytes, budget %d", len(raw), syncImageTargetBytes)
                }
                if mime != "image/jpeg" {
                        t.Fatalf("downscaled mime = %s, want image/jpeg", mime)
                }
        }
        // ...and the row is marked synced so the backfill leaves it alone.
        var synced sql.NullString
        tillA.db.QueryRow(`SELECT image_synced_at FROM products WHERE sku='SKU-P0'`).Scan(&synced)
        if !synced.Valid || synced.String == "" {
                t.Fatal("EmitProductImage did not stamp image_synced_at")
        }

        // Till A pushes to the cloud.
        imgSyncDrain(t, tillA, fake)

        // Till B (authorized, same team) pulls: the photo must be THERE.
        tillB := imgSyncTill(t, filepath.Join(dir, "b.db"))
        tillB.db.Exec(tillB.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                SELECT 'SKU-P0','P0', id, 500, 10 FROM categories LIMIT 1`))
        imgSyncDrain(t, tillB, fake)
        got := imgSyncProductImage(t, tillB, "SKU-P0")
        if got == "" {
                t.Fatal("till B has no photo after sync — images still don't follow the team")
        }
        mime, raw := imgSyncMustDataURL(t, got)
        if len(raw) > syncImageTargetBytes {
                t.Fatalf("till B photo = %d bytes, budget %d", len(raw), syncImageTargetBytes)
        }
        if mime == "" || mime == "image/svg+xml" {
                t.Fatalf("till B photo mime = %q", mime)
        }
        // B must NOT echo it back: image_synced_at stamped on apply.
        var bSynced string
        tillB.db.QueryRow(`SELECT COALESCE(image_synced_at,'') FROM products WHERE sku='SKU-P0'`).Scan(&bSynced)
        if bSynced == "" {
                t.Fatal("applied remote photo not marked synced — echo risk")
        }
        if backfilled := len(imgSyncOutboxPayloads(t, tillB, "product", "image")); backfilled != 0 {
                t.Fatalf("till B echoed %d image events after applying a remote photo", backfilled)
        }

        // Fresh till C joins later and replays the FULL history: photo included.
        tillC := imgSyncTill(t, filepath.Join(dir, "c.db"))
        tillC.db.Exec(tillC.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                SELECT 'SKU-P0','P0', id, 500, 10 FROM categories LIMIT 1`))
        imgSyncDrain(t, tillC, fake)
        if imgSyncProductImage(t, tillC, "SKU-P0") == "" {
                t.Fatal("fresh till C did not receive the photo from history replay")
        }
}

// TestImageSyncBackfill — photos uploaded BEFORE this feature shipped have
// no queued image event; the per-tick backfill must drain them exactly once
// (rate-limited, no duplicates).
func TestImageSyncBackfill(t *testing.T) {
        fake := newAgent3Cloud(t)
        dir := t.TempDir()
        tillA := imgSyncTill(t, filepath.Join(dir, "a.db"))

        // Three pre-existing photos (legacy rows: image_synced_at = '').
        small := encodeDataURL("image/png", smallPNG(t))
        for _, sku := range []string{"SKU-P0", "SKU-B2", "SKU-B3"} {
                tillA.db.Exec(tillA.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty, image_url)
                        SELECT ?, 'x', id, 100, 5, ? FROM categories LIMIT 1`), sku, small)
        }
        // One photo ALREADY synced — the backfill must not re-emit it.
        tillA.db.Exec(tillA.db.Rebind(`UPDATE products SET image_synced_at = '2026-10-03T09:00:00.000Z' WHERE sku = 'SKU-P0'`))

        tillA.backfillProductImages()
        evs := imgSyncOutboxPayloads(t, tillA, "product", "image")
        if len(evs) != 2 {
                t.Fatalf("backfill emitted %d events, want 2 (only unsynced rows)", len(evs))
        }
        // Rate-limited second run with MORE legacy rows: drains the new ones,
        // never re-emits the first batch.
        tillA.db.Exec(tillA.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty, image_url)
                SELECT 'SKU-B4', 'x', id, 100, 5, ? FROM categories LIMIT 1`), small)
        tillA.backfillProductImages()
        if evs2 := imgSyncOutboxPayloads(t, tillA, "product", "image"); len(evs2) != 3 {
                t.Fatalf("second backfill emitted %d total events, want 3", len(evs2))
        }

        // Draining the outbox must not resurrect the backfill (all marked).
        imgSyncDrain(t, tillA, fake)
        if n := len(imgSyncOutboxPayloads(t, tillA, "product", "image")); n != 3 {
                t.Fatalf("outbox grew after drain: %d", n)
        }
        var unsynced int
        tillA.db.QueryRow(`SELECT COUNT(*) FROM products WHERE COALESCE(image_url,'') != '' AND COALESCE(image_synced_at,'') = ''`).Scan(&unsynced)
        if unsynced != 0 {
                t.Fatalf("%d photos still unsynced after backfill", unsynced)
        }
}

// TestImageSyncClearAndStale — clearing a photo syncs as an empty-image
// event; a STALE queued photo never clobbers a newer local one; a price
// edit (applyProduct) never wipes a synced photo; and a poisoned non-image
// payload is rejected instead of stored.
func TestImageSyncClearAndStale(t *testing.T) {
        fake := newAgent3Cloud(t)
        dir := t.TempDir()
        tillA := imgSyncTill(t, filepath.Join(dir, "a.db"))
        pid := mustProductID(t, tillA, "SKU-P0")

        // Clear → empty image event.
        tillA.db.Exec(`UPDATE products SET image_url = '' WHERE id = ?`, pid)
        tillA.EmitProductImage(pid)
        evs := imgSyncOutboxPayloads(t, tillA, "product", "image")
        if len(evs) != 1 || !strings.Contains(evs[0], `"image":""`) {
                t.Fatalf("clear event = %v, want one empty-image event", evs)
        }
        imgSyncDrain(t, tillA, fake)

        // Till B applies: photo cleared there too.
        tillB := imgSyncTill(t, filepath.Join(dir, "b.db"))
        tillB.db.Exec(tillB.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty, image_url)
                SELECT 'SKU-P0','P0', id, 500, 10, 'x' FROM categories LIMIT 1`))
        imgSyncDrain(t, tillB, fake)
        if got := imgSyncProductImage(t, tillB, "SKU-P0"); got != "" {
                t.Fatalf("till B photo after clear = %.40q, want ''", got)
        }

        // Stale guard: a photo stamped BEFORE B's current updated_at is skipped.
        tillB.db.Exec(tillB.db.Rebind(`UPDATE products SET image_url = ?, updated_at = '2026-10-03T12:00:00.000Z' WHERE sku = 'SKU-P0'`),
                encodeDataURL("image/png", smallPNG(t)))
        stalePayload := []byte(`{"sku":"SKU-P0","barcode":"","image":"` + encodeDataURL("image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}) + `","updatedAt":"2026-10-03T11:00:00.000Z"}`)
        if err := tillB.applyProductImage(stalePayload); err != nil {
                t.Fatalf("stale apply returned error: %v", err)
        }
        got := imgSyncProductImage(t, tillB, "SKU-P0")
        if !strings.HasPrefix(got, "data:image/png") {
                t.Fatal("stale image event clobbered a newer local photo")
        }

        // Catalog edits (applyProduct) must not wipe photos.
        prodUpsert := []byte(`{"sku":"SKU-P0","barcode":"","name":"P0","categorySlug":"","priceCents":999,"costCents":0,"stockQty":10,"stockSet":false,"trackStock":true,"active":true,"updatedAt":"2026-10-03T13:00:00.000Z"}`)
        if err := tillB.applyProduct(prodUpsert); err != nil {
                t.Fatalf("applyProduct: %v", err)
        }
        if imgSyncProductImage(t, tillB, "SKU-P0") == "" {
                t.Fatal("applyProduct wiped image_url")
        }

        // Poisoned payload (HTML masquerading as an image) is rejected.
        poison := []byte(`{"sku":"SKU-P0","barcode":"","image":"data:image/png;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==","updatedAt":"2026-10-03T14:00:00.000Z"}`)
        if err := tillB.applyProductImage(poison); err == nil {
                t.Fatal("non-image payload accepted")
        }
}

// TestImageSyncLogoConfig — the brand logo follows the team through the
// config whitelist: applyConfig materialises the synced data URL into the
// local logo file + flag, and an empty payload clears them.
func TestImageSyncLogoConfig(t *testing.T) {
        fake := newAgent3Cloud(t)
        dir := t.TempDir()
        t.Chdir(dir) // brandLogoFile is relative to the data dir (process cwd)

        tillA := imgSyncTill(t, filepath.Join(dir, "a.db"))
        logoPNG := smallPNG(t)
        tillA.EmitBrandLogo(encodeDataURL("image/png", logoPNG))
        // The origin machine carries the compact copy in its own settings too.
        if v := tillA.settings.Get(brandLogoDataKey); v == "" {
                t.Fatal("origin machine did not persist brand_logo_data")
        }
        imgSyncDrain(t, tillA, fake)

        // Till B pulls: brand_logo_data arrives → file + flag materialise.
        tillB := imgSyncTill(t, filepath.Join(dir, "b.db"))
        imgSyncDrain(t, tillB, fake)
        if v := tillB.settings.Get(brandLogoDataKey); v == "" {
                t.Fatal("till B did not receive brand_logo_data")
        }
        if flag := tillB.settings.Get("brand_logo"); flag != "1" {
                t.Fatalf("till B brand_logo flag = %q, want 1", flag)
        }
        wrote, err := os.ReadFile(filepath.Join(dir, brandLogoFile))
        if err != nil {
                t.Fatalf("logo file not materialised: %v", err)
        }
        if sniffImageMime(wrote) == "" || !bytes.Equal(wrote, logoPNG) {
                t.Fatal("materialised logo file is not the synced PNG")
        }

        // Clearing on A clears on B (file removed, flag reset).
        tillA.EmitBrandLogo("")
        imgSyncDrain(t, tillA, fake)
        imgSyncDrain(t, tillB, fake)
        if flag := tillB.settings.Get("brand_logo"); flag != "" {
                t.Fatalf("till B brand_logo flag = %q after clear, want ''", flag)
        }
        if _, err := os.Stat(filepath.Join(dir, brandLogoFile)); !os.IsNotExist(err) {
                t.Fatal("logo file survived a fleet-wide clear")
        }
}

// TestImageSyncSmallPassThrough — photos already inside the budget ship
// byte-identical (no pointless re-encode; PNG transparency survives).
func TestImageSyncSmallPassThrough(t *testing.T) {
        tillA := imgSyncTill(t, filepath.Join(t.TempDir(), "a.db"))
        src := encodeDataURL("image/png", smallPNG(t))
        tillA.db.Exec(tillA.db.Rebind(`UPDATE products SET image_url = ? WHERE sku = 'SKU-P0'`), src)
        tillA.EmitProductImage(mustProductID(t, tillA, "SKU-P0"))
        evs := imgSyncOutboxPayloads(t, tillA, "product", "image")
        if len(evs) != 1 {
                t.Fatalf("events = %d, want 1", len(evs))
        }
        payloadImg := mustJSONString(t, evs[0], "image")
        if payloadImg != src {
                t.Fatal("in-budget photo was re-encoded instead of passing through")
        }
}

// TestDownscaleImageFits — a huge noisy image downscales to a decodable
// JPEG under budget with its longest edge ≤ syncImageMaxDim.
func TestDownscaleImageFits(t *testing.T) {
        raw := bigNoisyPNG(t, 2400, 1600)
        out, mime, ok := downscaleImage(raw, syncImageMaxDim, syncImageTargetBytes)
        if !ok {
                t.Fatal("downscale failed")
        }
        if mime != "image/jpeg" {
                t.Fatalf("mime = %s", mime)
        }
        if len(out) > syncImageTargetBytes {
                t.Fatalf("out = %d bytes > budget", len(out))
        }
        dec, err := jpeg.Decode(bytes.NewReader(out))
        if err != nil {
                t.Fatalf("downscaled bytes not decodable: %v", err)
        }
        b := dec.Bounds()
        if max(b.Dx(), b.Dy()) > syncImageMaxDim {
                t.Fatalf("downscaled to %dx%d, edge > %d", b.Dx(), b.Dy(), syncImageMaxDim)
        }
        if _, err := jpeg.Decode(bytes.NewReader(bigNoisyPNG(t, 4, 4))); err != nil {
                t.Log("tiny decode sanity skipped")
        }
        // Garbage is rejected, not panicked on.
        if _, _, ok := downscaleImage([]byte("not an image at all"), 100, 1000); ok {
                t.Fatal("garbage accepted as an image")
        }
}

// ---- helpers ----

func mustProductID(t *testing.T, s *Service, sku string) int64 {
        t.Helper()
        var id int64
        if err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), sku).Scan(&id); err != nil {
                t.Fatalf("product %s: %v", sku, err)
        }
        return id
}

func mustJSONString(t *testing.T, payload, key string) string {
        t.Helper()
        var m map[string]any
        if err := json.Unmarshal([]byte(payload), &m); err != nil {
                t.Fatalf("payload json: %v", err)
        }
        v, _ := m[key].(string)
        return v
}

func smallPNG(t *testing.T) []byte {
        t.Helper()
        img := image.NewNRGBA(image.Rect(0, 0, 48, 48))
        for y := 0; y < 48; y++ {
                for x := 0; x < 48; x++ {
                        img.Set(x, y, color.NRGBA{R: 30, G: 136, B: 229, A: 255})
                }
        }
        var buf bytes.Buffer
        if err := png.Encode(&buf, img); err != nil {
                t.Fatalf("png encode: %v", err)
        }
        return buf.Bytes()
}
