# LedgerPOS vs SawaPOS, competitive review (2026-10)

Source: sawapos.com (home, /pricing, /offline-pos, /kenya, blog) read live
during the v1.3.0 cycle. SawaPOS is the closest regional competitor the
client asked us to benchmark: a Kenya-first cloud POS targeting the same
SME retail / services space, priced KES 650/month or KES 20,000 per 4
years.

## What SawaPOS advertises

| Capability | Their pitch |
|---|---|
| Offline mode | Auto-switching offline-first; sales queue in browser localStorage; receipts keep printing; idempotency keys prevent duplicates on reconnect |
| Payments | M-Pesa STK Push, Till/Paybill, Kopokopo, Stripe/Square/Flutterwave, Apple/Google Pay, cash |
| Inventory | Low-stock alerts, buying/selling prices, categories, CSV |
| Sales floor | Park & resume, customer capture, thermal receipt printing |
| Growth | Loyalty program, CRM, **quotations** (headline feature), public shopfront + online orders, email/WhatsApp alerts |
| Compliance | KRA eTIMS |
| Ops | Unlimited staff & branches, shifts, reports dashboard, verticals (retail/restaurant/pharmacy/supermarket), business-setup wizard, live homepage demo |

## Head-to-head

| Dimension | SawaPOS | LedgerPOS | Verdict |
|---|---|---|---|
| Offline architecture | Browser localStorage queue on a web app | Local Go server + real SQLite database per till; sells through ISP outages, power cuts, browser crashes | **LedgerPOS wins on durability**, a real DB with backups beats localStorage that a cache wipe destroys |
| Multi-till sync | Cloud, real-time while online | Cloud sync fabric with exactly-once applies, dead-letter quarantine, poison-push bisection, offline queue durability, photo/logo sync | **LedgerPOS wins on integrity** (their model has no quarantine, failures are silent drops or duplicates) |
| Payments | Wider consumer wallet spread (Apple/Google Pay via Stripe/Square) | M-Pesa STK + manual Paybill confirm, Paystack cards, cash, store credit/tabs, gift cards, split tender, loyalty redemption | SawaPOS broader wallet list; **LedgerPOS deeper tender mechanics** (tabs, credit limits, gift cards, split) |
| Quotations | Advertised headline | **Shipped v1.3.0**: pipeline + team sync + convert-to-till + A4 print | Parity |
| CRM | Purchase history, capture | Auto-capture at checkout, tabs/credit, per-staff reports, WhatsApp deep links | Parity-ish; their public shopfront is the differentiator they lead with |
| Public shopfront / online orders | Yes, live public storefront | Not built | **SawaPOS leads** (roadmap item, not a v1.3 concern) |
| Auto alerts (email/WhatsApp) | Yes (online only) | WhatsApp deep links (manual tap-to-send); no unattended sends yet | SawaPOS leads while online; LedgerPOS works offline |
| Desktop app / OTA | Web app only, browser tab | Electron desktop with signed SHA-256 OTA manifests, auto self-update on tills, install snapshots + undo | **LedgerPOS wins** for real till hardware |
| Roles | "Unlimited staff accounts" | RBAC catalog (27 permissions), dynamic roles, server-side scoping, audit log, PIN + password + recovery codes + cloud PIN-reset | **LedgerPOS wins on governance** |
| Kenya compliance | eTIMS fields | eTIMS buyer-PIN tax invoices + KRA PIN on branding | Parity |
| Resilience of identity | Passwords | PIN pad with lockout, recovery codes, cloud one-time-code PIN reset (v1.3.0), owner cloud sign-in, join links that heal revoked devices (v1.3.0) | **LedgerPOS wins**, recovery without email/SMS infrastructure |

## What we shipped in v1.3.0 because of this review

1. **Quotations**, their marketing leads with it; now we match (pipeline,
   sync, convert-to-till, print).
2. **Device un-brick**, the field incident (Salma's PC) had left a
   recovery hole SawaPOS's model doesn't even have; fixed at the trust
   fabric level (join token / owner credential / owner approve all heal).
3. **Forgot-PIN one-time code**, their equivalent is "email us"; ours now
   works with no email at all.

## Roadmap candidates (deliberately NOT in v1.3.0)

- **Public shopfront + online orders**, biggest genuine gap; needs its own
  design round (catalog visibility rules, order intake channel, payment
  handoff).
- **Apple/Google Pay**, reachable via Paystack channels; commercial
  decision first (fees vs client volume).
- **Unattended WhatsApp/email alerts**, needs a provider account + consent
  flow; the manual WhatsApp deep links stay the offline-safe default.
- **Restaurant kitchen view**, Creative Divine is retail/branding/cyber;
  not their shape.

Keep this file next to ERP-CRM-RESEARCH.md; revisit when the shopfront
conversation starts.
