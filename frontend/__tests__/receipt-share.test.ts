// receipt-share — the one decision tree that carries the branded receipt
// IMAGE to WhatsApp on every device. These tests pin the contract:
// server queue wins when it queues; the native share sheet is used when
// the browser can share FILES; everything else is the desktop hop
// (clipboard + download + whatsapp:// deep link into the installed app).
import { describe, expect, it, vi } from 'vitest'
import {
  shareReceiptWhatsapp, waMeLink, whatsappDeepLink,
  receiptPhoneDigits, receiptCaption, ShareDeps,
} from '../src/lib/receipt-share'
import type { Order } from '../src/lib/api'

// Node's Blob is global; File landed as a global later — polyfill the
// same shape for the shop-floor test boxes if it is missing.
if (typeof (globalThis as { File?: unknown }).File === 'undefined') {
  class FakeFile extends Blob {
    name: string
    constructor(parts: BlobPart[], name: string) {
      super(parts)
      this.name = name
    }
  }
  ;(globalThis as { File?: unknown }).File = FakeFile
}

const order = (over: Partial<Order> = {}): Order => ({
  id: 42,
  number: 'L-0123',
  status: 'PAID',
  customerName: 'Amina Wanjiru',
  items: [],
  payments: [{ id: 1, method: 'mpesa', status: 'COMPLETED', amountCents: 0, phone: '0712 345 678' }],
  ...over,
} as unknown as Order)

const pngBlob = (): Blob => new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: 'image/png' })

function deps(over: Partial<ShareDeps> = {}): ShareDeps {
  return {
    apiPost: vi.fn().mockRejectedValue(new Error('gateway off')),
    fetchImage: vi.fn().mockResolvedValue(pngBlob()),
    share: vi.fn().mockResolvedValue(undefined),
    canShareFiles: vi.fn().mockReturnValue(false),
    copyPng: vi.fn().mockResolvedValue(undefined),
    download: vi.fn(),
    openExternal: vi.fn(),
    ...over,
  }
}

describe('receipt-share phone + link builders', () => {
  it('normalizes a stored 07… number to 2547… (wa.me needs the international form)', () => {
    expect(receiptPhoneDigits(order())).toBe('254712345678')
  })
  it('accepts an already-international 2547… and falls back to "" without a phone', () => {
    expect(receiptPhoneDigits(order({ payments: [{ id: 1, method: 'cash', status: 'COMPLETED', amountCents: 0, phone: '+254712345678' }] } as unknown as Partial<Order>))).toBe('254712345678')
    expect(receiptPhoneDigits(order({ payments: [] }))).toBe('')
  })
  it('wa.me keeps the chat on WhatsApp Web when there is no app', () => {
    expect(waMeLink('254712345678', 'Hi!')).toBe('https://wa.me/254712345678?text=Hi!')
    expect(waMeLink('', 'Hi!')).toBe('https://wa.me/?text=Hi!')
  })
  it('whatsapp:// opens the INSTALLED app straight in the chat', () => {
    expect(whatsappDeepLink('254712345678', 'Hi Amina!')).toBe('whatsapp://send?phone=254712345678&text=Hi%20Amina!')
    expect(whatsappDeepLink('', 'x')).toBe('whatsapp://send?text=x')
  })
  it('caption greets the customer by first name with the store name', () => {
    const c = receiptCaption(order(), 'Duka Kubwa')
    expect(c.startsWith('Hi Amina!')).toBe(true)
    expect(c).toContain('Duka Kubwa')
  })
})

describe('receipt-share decision tree', () => {
  it('PAID order + gateway connected → the SERVER queues the image, nothing else runs', async () => {
    const d = deps({ apiPost: vi.fn().mockResolvedValue({ queued: true, phone: '254712345678' }) })
    const r = await shareReceiptWhatsapp(order(), 'Duka', d)
    expect(r.kind).toBe('queued')
    expect(r.phone).toBe('254712345678')
    expect(d.fetchImage).not.toHaveBeenCalled()
    expect(d.openExternal).not.toHaveBeenCalled()
  })

  it('gateway off + browser can share FILES → native share sheet carries image + caption', async () => {
    const d = deps({ canShareFiles: vi.fn().mockReturnValue(true) })
    const r = await shareReceiptWhatsapp(order(), 'Duka', d)
    expect(r.kind).toBe('shared')
    expect(d.share).toHaveBeenCalledTimes(1)
    const call = (d.share as ReturnType<typeof vi.fn>).mock.calls[0][0] as { files: File[]; text: string }
    expect(call.files).toHaveLength(1)
    expect(call.files[0].name).toBe('receipt-L-0123.png')
    expect(call.files[0].type).toBe('image/png')
    expect(call.text).toContain('Duka')
    expect(d.download).not.toHaveBeenCalled()
    expect(d.openExternal).not.toHaveBeenCalled()
  })

  it('no file share (desktop PC) → clipboard + download + whatsapp:// opens the APP', async () => {
    const d = deps()
    const r = await shareReceiptWhatsapp(order(), 'Duka', d)
    expect(r.kind).toBe('app-opened')
    expect(r.clipboardOk).toBe(true)
    expect(d.copyPng).toHaveBeenCalledTimes(1)
    expect(d.download).toHaveBeenCalledTimes(1)
    expect((d.download as ReturnType<typeof vi.fn>).mock.calls[0][1]).toBe('receipt-L-0123.png')
    expect(d.openExternal).toHaveBeenCalledWith("whatsapp://send?phone=254712345678&text=Hi%20Amina!%20Thank%20you%20for%20shopping%20with%20Duka%20%E2%80%94%20we're%20glad%20you%20chose%20us.")
  })

  it('clipboard blocked → still opens the app; the downloaded PNG is the attach path', async () => {
    const d = deps({ copyPng: vi.fn().mockRejectedValue(new Error('not focused')) })
    const r = await shareReceiptWhatsapp(order(), 'Duka', d)
    expect(r.kind).toBe('app-opened')
    expect(r.clipboardOk).toBe(false)
    expect(d.openExternal).toHaveBeenCalledTimes(1)
    expect(d.download).toHaveBeenCalledTimes(1)
  })

  it('cashier cancels the share sheet → nothing more opens (that is normal)', async () => {
    const d = deps({
      canShareFiles: vi.fn().mockReturnValue(true),
      share: vi.fn().mockRejectedValue(new DOMException('user cancelled', 'AbortError')),
    })
    const r = await shareReceiptWhatsapp(order(), 'Duka', d)
    expect(r.kind).toBe('cancelled')
    expect(d.download).not.toHaveBeenCalled()
    expect(d.openExternal).not.toHaveBeenCalled()
  })

  it('non-PAID orders skip the server leg entirely (no gateway spam)', async () => {
    const apiPost = vi.fn().mockResolvedValue({ queued: true, phone: '254712345678' })
    const d = deps({ apiPost })
    await shareReceiptWhatsapp(order({ status: 'PENDING' }), 'Duka', d)
    expect(apiPost).not.toHaveBeenCalled()
  })
})
