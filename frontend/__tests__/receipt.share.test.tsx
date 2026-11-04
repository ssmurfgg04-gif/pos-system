// Receipt share button (v1.6.0) — the manual WhatsApp path while the
// whapi gateway sits in review. Covers:
//   1. the button renders with the right label;
//   2. it fetches the branded receipt image via the authenticated blob
//      channel (NOT a plain fetch — the demo and auth differ);
//   3. the Web Share path is used when the device supports file sharing;
//   4. otherwise: download + wa.me chat with the thank-you caption.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

;(React as any).actEnvironment = true

const blobMock = vi.fn(async () => new Blob(['PNG'], { type: 'image/png' }))
// Server leg (v1.6.0): default = gateway refusal (no token on the till),
// which is the state the shop actually runs in while whapi is in review.
const apiPostMock = vi.fn(async () => { throw new Error('WhatsApp gateway not connected') })

vi.mock('../src/lib/api', () => ({
  blob: (p: string) => blobMock(p),
  api: { post: (path: string, body?: unknown) => apiPostMock(path, body) },
  isDemoSync: () => false,
}))

import { ReceiptModal } from '../src/components/Receipt'

let root: Root | null = null
let container: HTMLDivElement

function mount(el: React.ReactElement) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
}

const order = (over: Partial<any> = {}) => ({
  id: 42,
  number: 'ORD-1042',
  status: 'PAID',
  totalCents: 55000,
  customerName: 'Amina Njeri',
  createdAt: '2026-10-29T09:00:00.000Z',
  items: [{ id: 1, productId: 1, name: 'Mug', sku: 'M1', qty: 1, unitPriceCents: 55000, lineTotalCents: 55000 }],
  payments: [{ id: 1, method: 'mpesa', status: 'COMPLETED', amountCents: 55000, phone: '254722123456', mpesaReceipt: 'QGH7XYZ1' }],
  ...over,
})

beforeEach(() => {
  blobMock.mockClear()
  apiPostMock.mockClear()
  apiPostMock.mockImplementation(async () => { throw new Error('WhatsApp gateway not connected') })
  if (root) {
    act(() => root!.unmount())
    root = null
  }
  document.body.innerHTML = ''
})

describe('Receipt share via WhatsApp', () => {
  test('PAID order: the gateway leg queues the IMAGE first and skips the device share', async () => {
    const share = vi.fn(async () => {})
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: () => true, configurable: true })
    apiPostMock.mockImplementation(async () => ({ queued: true, phone: '254722123456' }))

    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    await act(async () => {
      btn!.click()
    })
    // Server send ONLY: the customer receives the branded image from the
    // shop's own number — no share sheet, no image fetch needed.
    expect(apiPostMock).toHaveBeenCalledWith('/api/v1/orders/42/whatsapp-send', {})
    expect(blobMock).not.toHaveBeenCalled()
    expect(share).not.toHaveBeenCalled()
    const queued = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Image queued'))
    expect(queued).toBeTruthy()
  })

  test('gateway refusal (under review / no phone) falls through to the device image share', async () => {
    const share = vi.fn(async () => {})
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: () => true, configurable: true })

    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    await act(async () => {
      btn!.click()
    })
    expect(apiPostMock).toHaveBeenCalledTimes(1)
    // Same image, device path: Web Share carries the FILE + caption.
    expect(blobMock).toHaveBeenCalledWith('/api/v1/orders/42/receipt-image')
    expect(share).toHaveBeenCalledTimes(1)
    const arg = share.mock.calls[0][0] as ShareData & { files: File[] }
    expect(arg.files?.[0].name).toBe('receipt-ORD-1042.png')
  })

  test('button renders; Web Share carries the image + caption when supported', async () => {
    const share = vi.fn(async () => {})
    const canShare = vi.fn(() => true)
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: canShare, configurable: true })

    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    expect(btn).toBeTruthy()
    await act(async () => {
      btn!.click()
    })
    expect(blobMock).toHaveBeenCalledWith('/api/v1/orders/42/receipt-image')
    expect(canShare).toHaveBeenCalled()
    expect(share).toHaveBeenCalledTimes(1)
    const arg = share.mock.calls[0][0] as ShareData & { files: File[] }
    expect(arg.files?.length).toBe(1)
    expect(arg.files?.[0].name).toBe('receipt-ORD-1042.png')
    expect(arg.text).toContain("we're glad you chose us")
    expect(arg.text).toContain('Hi Amina!')
  })

  test('fallback: downloads the image and opens the customer wa.me chat', async () => {
    // No Web Share API → the drag-and-drop path.
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: undefined, configurable: true })
    const created: string[] = []
    URL.createObjectURL = vi.fn(() => 'blob:mock') as unknown as typeof URL.createObjectURL
    URL.revokeObjectURL = vi.fn() as unknown as typeof URL.revokeObjectURL
    const open = vi.fn()
    window.open = open as unknown as typeof window.open
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      created.push(this.download)
    })

    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    await act(async () => {
      btn!.click()
    })
    expect(blobMock).toHaveBeenCalledWith('/api/v1/orders/42/receipt-image')
    expect(created).toEqual(['receipt-ORD-1042.png'])
    expect(open).toHaveBeenCalledTimes(1)
    const url = open.mock.calls[0][0] as string
    expect(url.startsWith('https://wa.me/254722123456?text=')).toBe(true)
    expect(decodeURIComponent(url)).toContain('Thank you for shopping with Creative Divine')

    clickSpy.mockRestore()
  })

  test('a stored 07… number normalizes to 2547… for wa.me (the server rule)', async () => {
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: undefined, configurable: true })
    URL.createObjectURL = vi.fn(() => 'blob:mock') as unknown as typeof URL.createObjectURL
    URL.revokeObjectURL = vi.fn() as unknown as typeof URL.revokeObjectURL
    const open = vi.fn()
    window.open = open as unknown as typeof window.open
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    mount(React.createElement(ReceiptModal, {
      order: order({ payments: [{ id: 2, method: 'cash', status: 'COMPLETED', amountCents: 55000, phone: '0711 222 333' }] }),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    await act(async () => {
      btn!.click()
    })
    const url = open.mock.calls[0][0] as string
    expect(url.startsWith('https://wa.me/254711222333?text=')).toBe(true)
    clickSpy.mockRestore()
  })

  test('the configured receipt footer rides branding; generic line only as fallback', () => {
    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine', receipt_footer: 'Karibu tena!' } as any,
      open: true,
      onClose: () => {},
    }))
    expect(container.textContent).toContain('Karibu tena!')

    container.innerHTML = ''
    mount(React.createElement(ReceiptModal, {
      order: order(),
      branding: { store_name: 'Creative Divine' } as any,
      open: true,
      onClose: () => {},
    }))
    expect(container.textContent).toContain('Goods remain the property of the store until fully paid.')
  })

  test('zero-tax orders show no VAT row', () => {
    mount(React.createElement(ReceiptModal, {
      order: order({ taxCents: 0, taxPercent: 0 }),
      branding: { store_name: 'Creative Divine' } as any,
      open: true,
      onClose: () => {},
    }))
    expect(container.textContent).not.toContain('VAT')
  })

  test('no customer phone on the order → wa.me without a number still opens', async () => {
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: undefined, configurable: true })
    URL.createObjectURL = vi.fn(() => 'blob:mock') as unknown as typeof URL.createObjectURL
    URL.revokeObjectURL = vi.fn() as unknown as typeof URL.revokeObjectURL
    const open = vi.fn()
    window.open = open as unknown as typeof window.open
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    mount(React.createElement(ReceiptModal, {
      order: order({ payments: [{ id: 1, method: 'cash', status: 'COMPLETED', amountCents: 55000 }] }),
      branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' },
      open: true,
      onClose: () => {},
    }))
    const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Share via WhatsApp'))
    await act(async () => {
      btn!.click()
    })
    const url = open.mock.calls[0][0] as string
    expect(url.startsWith('https://wa.me/?text=')).toBe(true)
    clickSpy.mockRestore()
  })
})
