# LedgerPOS feature checklist

The list to run through before calling any release good. Each item says what
should happen and how to check it fast. If a change touches one of these
areas, re-test that whole section, not just the one line you edited.

How to read the levels:

- **Critical**: money or data. A failure here means lost sales, lost stock
  truth, or a till that cannot open. Never ship with one of these broken.
- **Core**: daily work for the owner or cashiers. Annoying when broken, and
  customers notice.
- **Supporting**: nice-to-have paths that must degrade honestly (show an
  error) rather than lie (silent success).

## 1. Sign-in and accounts

| Level | Feature | How to check |
|---|---|---|
| Critical | Password sign-in | Sign in with owner and cashier accounts. Wrong password shows a clear message and stays on the form. |
| Critical | PIN sign-in | Sign in with a 4-digit PIN. Wrong PIN shakes/marks red, no crash. Password sign-in link still visible. |
| Critical | Session survives wrong passwords | While signed in, a failed login or a failed owner-approval must NOT throw you to the login screen. |
| Critical | Forced rotation | A fresh seed account must land on the rotation screen before anything else. |
| Core | Register button | Login screen shows Register beside Sign in. It opens the welcome choice (sign in / register a business / join link). |
| Core | Welcome choice | A till with zero accounts shows the welcome choice, never a dead login form. |

## 2. Team, invites, and the owner gate

| Level | Feature | How to check |
|---|---|---|
| Critical | Invite stays signed in | Settings → Team → Create join link, while signed in as owner. You must remain signed in the whole time. A wrong portal password keeps the dialog open with a retry, never a logout. |
| Critical | Join link redeem (fresh till) | Open the app's Join screen, paste the link, name + PIN, join. You land in the POS with the right role; products and settings start syncing. |
| Critical | Join link is single-use | A second redemption of the same link must be refused with a clear message. |
| Core | Join link self-fill | Opening a link with the app installed pre-fills the join box (fragment `#t=...&r=...&k=...`). |
| Core | Link shape | The link contains team code, role and token only. No username anywhere. |
| Core | Owner approval dialog | Invites, device approvals and store actions prompt once for the owner password, then ride for about 30 minutes. Prefilled with the signed-in owner's username. PIN-reset approval deliberately has NO password prompt (one tap; the v1.3.2 challenge made every approve fail on shops whose portal credential was never seeded). |
| Critical | Unrevoke | Revoked tills stay visible in the roster with a Revoked pill. Unrevoke brings the till back (it syncs again on its next heartbeat). |
| Core | Revoke | Revoking a till cuts it off immediately; the row stays listed as Revoked. |
| Core | PIN reset approval | A locked-out worker requests a reset; the owner taps Approve and the one-time 8-digit code appears immediately (no password challenge). The worker types the code under "Forgot PIN?" and sets a new PIN. |
| Core | WiFi code hand-off | Tills on the same WiFi find each other (mDNS + restricted LAN listener). When the owner till approves, the code is delivered straight to the waiting till, which pre-fills it and asks only for the new PIN. The code still burns at the cloud (single-use, device-bound). |
| Core | LAN delivery fallback | If the WiFi blocks discovery (firewall prompt dismissed, AP isolation), the owner still sees the code on screen and reads it aloud; nothing else changes. First LAN listen may pop a one-time Windows allow prompt. |

## 3. Cross-till sync (two machines)

| Level | Feature | How to check |
|---|---|---|
| Critical | Store name change | Change the store name on till A → Save shows success (no "unknown setting key" error) → till B shows the new name after its sync tick. |
| Critical | Brand logo set + clear | Upload a logo on A → appears on B; remove it on A → disappears on B. |
| Critical | Product create + price change | Add a product on A (with photo) → appears on B; mark a price cut / sale price on A → B shows it. |
| Critical | Orders and payments sync | A sale completed on A appears in B's history/reports. |
| Core | Exactly-once | Restarting either till, or a sync retry, must not duplicate products, orders, or ledger movements. |
| Core | Offline recovery | Sell offline on A, come back online: the sale pushes, nothing is lost, nothing duplicated (check Settings → Team for honest pending/failed counts). |
| Core | Quarantine on team change | A till that switches teams must not push its old team's data into the new team. |

## 4. Checkout and M-Pesa (Paystack STK)

| Level | Feature | How to check |
|---|---|---|
| Critical | STK push completes | Cash sale via M-Pesa STK: prompt arrives, customer enters PIN, order flips to PAID with a receipt. |
| Critical | STK cancel grace | Let the customer sit on the prompt: the cashier screen must NOT fail in the first seconds. Give the customer roughly 10 extra seconds after Paystack first reports abandoned/failed before the leg shows failed. Money landing inside that window still completes the sale. |
| Critical | Late-payment recovery | Money that arrives after a failure (within the 15-minute watch, the 90-minute failed-sweep, the 24-hour recheck, or via webhook) still completes the original order exactly once. |
| Critical | Never void paid money | An order that was voided cannot be completed afterwards; a payment after void is flagged for refund, not booked. |
| Core | Popup card checkout | Card payments through the popup flow: init, pay, verify, complete. Popup legs fail fast when abandoned (no grace). |
| Core | Manual M-Pesa entry | Keying a real M-Pesa code completes the sale; duplicate codes are refused. |
| Core | Split tender | Cash + M-Pesa + credit legs sum to the total; the async leg still finishes the order. |
| Core | Honest waiting UI | The STK panel shows the phone number, the progress steps, and never claims paid before Paystack confirms. |

## 5. Products and inventory

| Level | Feature | How to check |
|---|---|---|
| Core | Product CRUD | Create, edit, photo upload, archive. Photo survives a restart. |
| Core | Stock movements | Receiving stock, sale deduction, void restoration all land in the ledger with running balances. |
| Core | Low stock | Threshold warnings fire; zero-priced items are sellable and flagged honestly. |
| Core | Categories | Products always have a category; uncategorised items still sell and sync. |

## 6. Settings, branding, receipts

| Level | Feature | How to check |
|---|---|---|
| Critical | Settings save | Every tab saves: store, tax, currency, receipt, printer, payments, backup. Success toast is honest; a real failure shows the reason. |
| Core | Receipt | Test print works; receipt shows store name, items, tax, footer, logo, KRA PIN when set. |
| Core | Secrets | Saved keys show as masked dots; saving without touching them keeps them; Remove clears them. Secrets never appear in GET responses or logs. |
| Core | Payment status card | The Payments tab shows cloud-managed status with no secret inputs on tills. |

## 7. Users, roles, permissions

| Level | Feature | How to check |
|---|---|---|
| Core | Role gating | Cashier cannot reach Settings/Users; designer sees design board only; every API route re-checks server-side. |
| Core | User management | Create user with role + PIN, deactivate, reset PIN (owner-approved across tills). |
| Core | Audit log | Sensitive actions (settings, users, payments, voids, sync) land in the audit log with who/what/when. |

## 8. Multi-store (owner cloud)

| Level | Feature | How to check |
|---|---|---|
| Core | Store registry | Add a store, see device counts; assign a pending till to a store. |
| Core | Isolation | One store's till never sees another store's products, orders, or events (DB-enforced, not just UI). |

## 9. Updates and packaging

| Level | Feature | How to check |
|---|---|---|
| Critical | OTA manifest | The cloud manifest points at the new version with SHA-256 verified assets; a till on the old version sees the update and can install + roll back. |
| Core | Installers | All four packages (Windows x64, macOS arm64, macOS Intel, Linux x64) download from the site and from the GitHub release, and checksums match. |
| Core | Demo site | awesomeposs.netlify.app loads the demo; /join shows instructions with the copy box; /portal is the owner portal; /downloads serves the current installers. |

## 10. Language and behaviour standard (every screen)

| Level | Feature | How to check |
|---|---|---|
| Standard | No em dashes | No user-visible text anywhere contains an em dash (—). Comments in code are exempt. |
| Standard | Plain words | No jargon on staff screens (no "endpoint", "payload", "provider", "manifest"). |
| Standard | One next step | Every screen makes the next action obvious; errors say what happened and what to do next; never a bare "Operation failed". |
| Standard | Honest states | Saving vs saved vs syncing vs synced vs failed are distinct and truthful; the app never fakes success. |
| Standard | Readable | Headings are dark ink on light surfaces (Join and PIN screens especially), buttons are tall enough, contrast passes. |

## Fast smoke path (10 minutes)

1. Sign in as owner (password), then PIN screen as cashier.
2. Settings → change store name → Save → success toast.
3. Settings → Team → create a join link (stays signed in) → copy it.
4. Join screen: paste link, name + PIN → lands in POS.
5. Sell one item with M-Pesa STK → completes. Start a second push and let it sit → the screen does not cancel early.
6. Revoke the test till → row shows Revoked → Unrevoke → back to Approved.
7. Reports show today's two sales; the second till (or a re-pull) shows the same data.
