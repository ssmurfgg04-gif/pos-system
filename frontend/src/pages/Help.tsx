// Help — in-app operational quick answers. Static content on purpose: it
// ships with the binary, works offline, and mirrors the printed
// docs/TROUBLESHOOTING.md. Everyone signed in can open it (no permission
// gate — knowing how the till behaves is not a privilege).

import { useState } from 'react'
import { ChevronDown, LifeBuoy } from 'lucide-react'

const TOPICS: { q: string; a: string }[] = [
  {
    q: 'The M-Pesa prompt is not arriving on the customer phone',
    a: 'Wait ~30 seconds and ask them to check "My SIP messages" — KCB prompts land there on some phones. Still nothing? Press "Check payment status", or switch to manual: they pay to the Paybill/Till and you key the 10-character code from their SMS.',
  },
  {
    q: 'The customer says they already paid but the sale is still pending',
    a: 'Press "Check payment status" — late payments land there. Otherwise take the receipt code from their M-Pesa SMS and enter it manually. The system only completes on real, matching money; a mismatch raises a discrepancy for the admin instead of guessing.',
  },
  {
    q: 'Why is "Resend prompt" locked?',
    a: 'A prompt is already open on the phone. KCB rejects a second one ("Duplicated MSISDN"). It unlocks as soon as the customer enters their PIN, cancels, or the push fails. Nothing was charged while you wait.',
  },
  {
    q: 'The internet is down — can I keep selling?',
    a: 'Yes. Cash sales work offline and are saved on this device; they sync automatically when the network returns. M-Pesa STK needs internet, so offline you use manual receipt entry or cash. Nothing is ever lost.',
  },
  {
    q: 'How do I void or correct a sale?',
    a: 'Your own sales: Orders → open the sale → Void, pick a reason. Voiding a paid sale restores stock and reverses credit/loyalty; if money actually moved, it is flagged "refund owed" for the admin. Someone else\'s sale needs an admin — staff only see their own sales by design (v1.4.4).',
  },
  {
    q: 'Closing the shift — what is "variance"?',
    a: 'Expected cash = opening float + cash sales taken during the shift. Counted is what is really in the drawer. The difference is the variance: small ones happen (change mistakes); big or recurring ones go to the admin the same day.',
  },
  {
    q: 'The receipt did not print',
    a: 'Reprint from the paid screen or Orders. If the printer is not responding at all, ask the admin to run Settings → Printing → Test print and check the paper width (58 vs 80 mm).',
  },
  {
    q: 'The app offers an update',
    a: 'Finish the current sale, then restart the app when the counter is quiet. Updates verify a signed manifest before installing and never interrupt a payment in flight.',
  },
  {
    q: 'I see "Saved offline — will sync when back online"',
    a: 'The sale is queued on this device with its own idempotency key — it cannot be double-counted when it syncs. Keep selling; you will hear/see "Synced N offline sales" when the connection returns.',
  },
  {
    q: 'Who can see which sales?',
    a: 'Staff see their own sales only. Admin/Owner can browse every sale from every till (that is the "Browse all individual sales" permission). The daily report\'s "By till" table gives admins per-till totals that match the orders list.',
  },
]

export function Help() {
  const [open, setOpen] = useState<number | null>(0)
  return (
    <div className="max-w-2xl mx-auto p-4 sm:p-6 space-y-4">
      <div className="flex items-center gap-3">
        <span className="w-10 h-10 rounded-input bg-brand-soft text-brand flex items-center justify-center" aria-hidden>
          <LifeBuoy size={19} strokeWidth={2.25} />
        </span>
        <div>
          <h1 className="font-black text-ink text-xl leading-tight">Help</h1>
          <p className="text-[12.5px] text-ink-muted">Quick answers at the counter. Full guides live with the owner (docs/ folder).</p>
        </div>
      </div>

      <div className="space-y-2">
        {TOPICS.map((t, i) => (
          <div key={i} className="border-2 border-line rounded-input bg-surface overflow-hidden">
            <button
              type="button"
              onClick={() => setOpen(open === i ? null : i)}
              aria-expanded={open === i}
              className="w-full flex items-center justify-between gap-3 px-3.5 py-3 text-left"
            >
              <span className="font-bold text-ink text-[13.5px]">{t.q}</span>
              <ChevronDown
                size={16}
                strokeWidth={2.5}
                aria-hidden
                className={`shrink-0 text-ink-muted transition-transform ${open === i ? 'rotate-180' : ''}`}
              />
            </button>
            {open === i && (
              <p className="px-3.5 pb-3.5 text-[13px] text-ink-muted leading-relaxed">{t.a}</p>
            )}
          </div>
        ))}
      </div>

      <p className="text-[12px] text-ink-muted">
        Something not covered here? Screenshots + the order number help the
        owner fix it fast — every action on this till is in the audit log.
      </p>
    </div>
  )
}
