import type { CapacitorConfig } from '@capacitor/cli';

/**
 * LedgerPOS Android client — a thin terminal for a till server on the shop
 * LAN (the audited Go binary keeps owning money logic; the APK is just the
 * frontend + WebView). The bundled SPA ships WITHOUT the demo backend: a
 * first-launch Connect screen captures the till address and the app never
 * falls back to fake data.
 *
 * - server.url allows cleartext ONLY to private LAN ranges (see
 *   android/app/src/res/xml/network_security_config.xml) — the shop Wi-Fi
 *   is plain http; a public https reverse proxy needs no exception.
 * - The WebView origin is https://appassets.androidplatform.net (Capacitor
 *   default) so localStorage/IndexedDB (cart + offline queue) persist and
 *   run as a secure context. Tills must add this origin to
 *   CORS_EXTRA_ORIGINS (docs/DEPLOYMENT.md → Android client).
 */
const config: CapacitorConfig = {
  appId: 'app.ledgerpos.android',
  appName: 'LedgerPOS',
  webDir: 'dist',
  server: {
    androidScheme: 'https',
    hostname: 'appassets.androidplatform.net',
  },
  android: {
    allowMixedContent: false,
    captureInput: true,
    webContentsDebuggingEnabled: false,
  },
};

export default config;
