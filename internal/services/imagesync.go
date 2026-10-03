package services

import (
        "bytes"
        "encoding/base64"
        "encoding/json"
        "database/sql"
        "fmt"
        "image"
        "image/draw"
        _ "image/gif" // register decoders for image.Decode
        "image/jpeg"
        _ "image/png" // register decoders for image.Decode
        "log"
        "os"
        "strings"

        xdraw "golang.org/x/image/draw"
)

// imagesync.go — product photos and the brand logo follow the team like
// every other catalog change. Both used to live only on the machine that
// uploaded them (photo = a data URL on the local products row, logo = a
// file in the local data dir), so a second till showed monograms and the
// stock login screen lost the logo. The fix:
//
//   - product photos ride a dedicated `product / image` event keyed by
//     SKU (the same identity applyProduct matches on), applied with the
//     usual LWW stamp so a stale queued photo can't clobber a newer one;
//   - the brand logo rides the existing config whitelist as a
//     `brand_logo_data` setting (a data URL); applyConfig materialises it
//     into the local logo file + flag so the existing /settings/logo
//     route, login screen and topbar keep working unchanged;
//   - payloads are DOWNSCALED before emission (photos ≤ 640px, logo
//     ≤ 512px, JPEG/PNG re-encode) — a 2 MB upload becomes a tens-of-KB
//     event that survives the cloud RPC body budget; the full-resolution
//     original stays on the machine that uploaded it;
//   - a one-time-per-device backfill (rate-limited per sync tick) pushes
//     photos that pre-date this feature; image_synced_at marks rows whose
//     current image has been emitted so received photos are never echoed
//     back (A → B → A → … ping-pong).

const (
        // syncImageMaxDim caps the longest edge of a synced photo.
        syncImageMaxDim = 640
        // syncLogoMaxDim caps the longest edge of a synced logo.
        syncLogoMaxDim = 512
        // syncImageTargetBytes is the soft budget for one synced image payload.
        // Budget-sized events keep a 200-event push batch well inside the
        // cloud's request body limits; the bisection only ever sees one.
        syncImageTargetBytes = 140 << 10 // 140 KB
        // backfillImagesPerTick rate-limits the pre-existing-photo backfill so
        // a large catalog doesn't shove megabytes into one push batch.
        backfillImagesPerTick = 24
        // brandLogoDataKey is the config-whitelisted setting that carries the
        // synced logo (a data URL). Empty means "logo cleared".
        brandLogoDataKey = "brand_logo_data"
        // brandLogoFile mirrors handlers.logoFileName — the logo lives next to
        // the shop database (the desktop process chdir's into the data dir).
        brandLogoFile = "brand-logo.png"
)

// decodeDataURL parses `data:<mime>;base64,<payload>` into raw bytes +
// mime. Anything else (http URL, bare base64, empty) is not an image we
// sync — the caller just skips it.
func decodeDataURL(dataURL string) (mime string, raw []byte, ok bool) {
        idx := strings.Index(dataURL, ";base64,")
        if idx < 0 || !strings.HasPrefix(dataURL, "data:") {
                return "", nil, false
        }
        mime = strings.TrimPrefix(dataURL[:idx], "data:")
        raw, err := base64.StdEncoding.DecodeString(dataURL[idx+len(";base64,"):])
        if err != nil || len(raw) == 0 {
                return "", nil, false
        }
        return mime, raw, true
}

// encodeDataURL is the inverse of decodeDataURL.
func encodeDataURL(mime string, raw []byte) string {
        return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(raw))
}

// sniffImageMime returns the real image format from magic bytes
// (Content-Type headers lie; this matches handlers.sniffedImage).
func sniffImageMime(b []byte) string {
        switch {
        case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 0x47:
                return "image/png"
        case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
                return "image/jpeg"
        case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
                return "image/gif"
        case len(b) >= 12 && string(b[8:12]) == "WEBP":
                return "image/webp"
        }
        return ""
}

// downscaleImage re-encodes raw image bytes to fit maxDim on the longest
// edge and stay under targetBytes. Photos smaller than the budget pass
// through untouched (byte-identical). Oversized ones are decoded, fitted
// onto a white canvas (flattening transparency the way a receipt/catalog
// card would) and re-encoded as JPEG with progressive quality steps. A
// source the stdlib can't decode (corrupt upload, exotic GIF) returns
// ok=false — callers skip the emission rather than break a sale path.
func downscaleImage(raw []byte, maxDim int, targetBytes int) (out []byte, mime string, ok bool) {
        srcMime := sniffImageMime(raw)
        if srcMime == "" {
                return nil, "", false
        }
        // Within budget → ship as-is (keeps GIF/PNG quirks the till already renders).
        if len(raw) <= targetBytes {
                return raw, srcMime, true
        }
        src, _, err := image.Decode(bytes.NewReader(raw))
        if err != nil {
                return nil, "", false
        }
        b := src.Bounds()
        w, h := b.Dx(), b.Dy()
        if w <= 0 || h <= 0 {
                return nil, "", false
        }
        // Scale only when the longest edge actually exceeds maxDim.
        nw, nh := w, h
        if w > maxDim || h > maxDim {
                if w >= h {
                        nw, nh = maxDim, h*maxDim/w
                } else {
                        nw, nh = w*maxDim/h, maxDim
                }
                if nw < 1 {
                        nw = 1
                }
                if nh < 1 {
                        nh = 1
                }
        }
        // White canvas: JPEG has no alpha; transparent product shots flatten
        // exactly like they render on the light catalog surface.
        dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
        draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
        xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)

        for _, q := range []int{80, 65, 50, 38} {
                var buf bytes.Buffer
                if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q}); err != nil {
                        return nil, "", false
                }
                if buf.Len() <= targetBytes || q == 38 {
                        return buf.Bytes(), "image/jpeg", true
                }
        }
        return nil, "", false // unreachable
}

// downscaleForSync downscales + returns the ready-to-send data URL, or ""
// when nothing should be emitted (undecodable, empty source).
func downscaleForSync(dataURL string, maxDim, targetBytes int) string {
        if dataURL == "" {
                return ""
        }
        _, raw, ok := decodeDataURL(dataURL)
        if !ok {
                return ""
        }
        out, mime, ok := downscaleImage(raw, maxDim, targetBytes)
        if !ok {
                // Undecodable at full size — still try shipping the original when it
                // is small enough to be within budget (downscaleImage handles that;
                // reaching here means decode failed on a large file). Give up.
                return ""
        }
        return encodeDataURL(mime, out)
}

// EmitProductImage queues the product's CURRENT photo (or its clearing) as
// a compact `product / image` event and stamps image_synced_at so the
// backfill never re-emits the same bytes. Best-effort like every Emit.
func (s *Service) EmitProductImage(productID int64) {
        var sku, barcode, img string
        err := s.db.QueryRow(s.db.Rebind(
                `SELECT COALESCE(sku,''), COALESCE(barcode,''), COALESCE(image_url,'') FROM products WHERE id = ?`), productID).
                Scan(&sku, &barcode, &img)
        if err != nil {
                return
        }
        if sku == "" && barcode == "" {
                return // applyProductImage keys on sku/barcode — nothing to match on
        }
        payload := map[string]any{
                "sku": sku, "barcode": barcode, "image": "", "updatedAt": nowStamp(),
        }
        if img != "" {
                small := downscaleForSync(img, syncImageMaxDim, syncImageTargetBytes)
                if small == "" {
                        // Never emit garbage; also mark it synced so the backfill does
                        // not retry the same undecodable blob every tick.
                        s.markImageSynced(productID)
                        return
                }
                payload["image"] = small
        }
        s.Emit("product", "image", payload)
        s.markImageSynced(productID)
}

// markImageSynced records that this row's current image bytes are in the
// outbox (prevents echo + pointless re-emission).
func (s *Service) markImageSynced(productID int64) {
        s.db.Exec(s.db.Rebind(`UPDATE products SET image_synced_at = ? WHERE id = ?`), nowStamp(), productID)
}

// backfillProductImages emits image events for photos uploaded BEFORE this
// feature shipped (image_synced_at still ''), rate-limited per call so a
// big catalog drains over several sync ticks instead of one huge batch.
// Called from the sync loop; cheap no-op when there is nothing to do.
func (s *Service) backfillProductImages() {
        rows, err := s.db.Query(`SELECT id FROM products WHERE COALESCE(image_url,'') != '' AND COALESCE(image_synced_at,'') = '' LIMIT ?`, backfillImagesPerTick)
        if err != nil {
                return
        }
        var ids []int64
        for rows.Next() {
                var id int64
                if rows.Scan(&id) == nil {
                        ids = append(ids, id)
                }
        }
        rows.Close()
        for _, id := range ids {
                s.EmitProductImage(id)
        }
        if len(ids) > 0 {
                log.Printf("[sync] image backfill: queued %d product photo(s)", len(ids))
        }
}

// applyProductImage applies a remote product photo/clearing. Matches the
// same identity applyProduct matches (sku, else barcode) and guards with
// the event stamp so a photo queued BEFORE a newer local upload never
// clobbers it. The received image is marked synced on arrival so this
// device's backfill won't echo it back to the team.
func (s *Service) applyProductImage(payload json.RawMessage) error {
        var pr struct {
                SKU       string `json:"sku"`
                Barcode   string `json:"barcode"`
                Image     string `json:"image"`
                UpdatedAt string `json:"updatedAt"`
        }
        if err := json.Unmarshal(payload, &pr); err != nil {
                return err
        }
        if pr.SKU == "" && pr.Barcode == "" {
                return fmt.Errorf("product image event has no sku/barcode")
        }
        if pr.Image != "" {
                // Defence in depth: a poisoned/malformed payload must not smuggle
                // non-image bytes into a row that an <img> tag will render.
                if !strings.HasPrefix(pr.Image, "data:image/") ||
                        strings.Contains(pr.Image, "svg") ||
                        sniffImageMime(dataURLRawBytes(pr.Image)) == "" {
                        return fmt.Errorf("product image event payload is not a recognised image")
                }
                if len(pr.Image) > 4<<20 { // 4 MB data-URL ceiling (~3 MB raw)
                        return fmt.Errorf("product image event too large")
                }
        }
        var localID int64
        var localUpdated string
        q := `SELECT id, COALESCE(updated_at,'') FROM products WHERE sku = ?`
        args := []any{pr.SKU}
        if pr.SKU == "" {
                q = `SELECT id, COALESCE(updated_at,'') FROM products WHERE barcode = ?`
                args = []any{pr.Barcode}
        }
        err := s.db.QueryRow(s.db.Rebind(q), args...).Scan(&localID, &localUpdated)
        if err == sql.ErrNoRows {
                // The catalog row hasn't landed yet (a device that created the
                // product is still pushing behind this one — cloud order is push
                // order, not event-time order). Quarantine via a retryable error:
                // retryFailedApplies re-runs it next cycle, by which time the
                // product upsert that precedes it in every sender's outbox has
                // arrived and the photo applies.
                return fmt.Errorf("product %s%s not found yet (image event arrived early)", pr.SKU, pr.Barcode)
        }
        if err != nil {
                return err
        }
        if pr.UpdatedAt != "" && localUpdated != "" && pr.UpdatedAt < localUpdated {
                return nil // stale — a newer local image/edition wins
        }
        if _, err := s.db.Exec(s.db.Rebind(
                `UPDATE products SET image_url = ?, updated_at = COALESCE(NULLIF(?,''), updated_at), image_synced_at = ? WHERE id = ?`),
                pr.Image, pr.UpdatedAt, nowStamp(), localID); err != nil {
                return err
        }
        return nil
}

// isLogoMime matches the logo file contract (PNG or JPEG only — same
// formats handlers.logoContentType accepts).
func isLogoMime(mime string) bool {
        return mime == "image/png" || mime == "image/jpeg"
}

// dataURLRawBytes extracts the raw bytes of a data URL for sniffing
// (empty on any parse failure — sniffImageMime then rejects).
func dataURLRawBytes(dataURL string) []byte {
        _, raw, ok := decodeDataURL(dataURL)
        if !ok {
                return nil
        }
        return raw
}

// applyBrandLogoData materialises a synced logo into this machine's local
// state: the file the /settings/logo route serves, plus the brand_logo
// flag the route (and login screen) check. Empty data clears both.
func (s *Service) applyBrandLogoData(v string) {
        if v == "" {
                _ = os.Remove(brandLogoFile)
                _ = s.settings.Set("brand_logo", "")
                _ = s.settings.Set(brandLogoDataKey, "")
                return
        }
        _, raw, ok := decodeDataURL(v)
        if !ok || !isLogoMime(sniffImageMime(raw)) {
                return // never write a non-image payload into the logo file
        }
        if err := os.WriteFile(brandLogoFile, raw, 0o644); err != nil {
                log.Printf("[sync] brand logo write: %v", err)
                return
        }
        _ = s.settings.Set("brand_logo", "1")
        _ = s.settings.Set(brandLogoDataKey, v)
}

// EmitBrandLogo queues the CURRENT logo (or its clearing) on the config
// channel. Called by the logo upload/delete handlers.
func (s *Service) EmitBrandLogo(dataURL string) {
        if dataURL != "" {
                if small := downscaleForSync(dataURL, syncLogoMaxDim, syncImageTargetBytes); small != "" {
                        dataURL = small
                } else {
                        dataURL = "" // undecodable logo — clear rather than ship garbage
                }
        }
        // Persist locally as well: the origin machine's flag/data stay in lockstep
        // with what the rest of the team received.
        if dataURL == "" {
                _ = s.settings.Set(brandLogoDataKey, "")
        } else {
                _ = s.settings.Set(brandLogoDataKey, dataURL)
        }
        s.EmitConfig([]string{brandLogoDataKey})
}
