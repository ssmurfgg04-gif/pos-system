// Receipt share button — v1.6.0 manual path, v1.6.2 open-the-app flow.
// The customer complaints behind v1.6.2: on the desktop app (WebView2) the
// v1.6.0 flow never opened the installed WhatsApp, and in a browser the
// share sheet asked "which app?" with no contact loaded. The contract now:
//   1. the gateway leg still wins for PAID orders (image queued server-side);
//   2. the image ALWAYS downloads first — WhatsApp can't receive a link;
//   3. HOST leg: POST /whatsapp-open opened=true → no browser fallback;
//   4. HOST opened=false → honest hint, no silent nothing;
//   5. HOST endpoint missing (old server / browser-only) → openWhatsAppChat
//      deep link with the customer's digits + caption;
//   6. secondary "Send image…" rides the Web Share sheet (files + caption);
//   7. stored 07… numbers normalize to 2547…;
//   8. no phone on the order → chat without a number.
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

const waChatMock = vi.fn()
vi.mock('../src/lib/whatsapp', () => ({
  openWhatsAppChat: (...a: unknown[]) => waChatMock(...a),
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

const branding = { store_name: 'Creative Divine', app_name: 'LedgerPOS', brand_color: '#10B981', currency_symbol: 'KES' }

beforeEach(() => {
  blobMock.mockClear()
  waChatMock.mockClear()
  apiPostMock.mockClear()
  apiPostMock.mockImplementation(async () => { throw new Error('WhatsApp gateway not connected') })
  if (root) {
    act(() => root!.unmount())
    root = null
  }
  document.body.innerHTML = ''
  URL.createObjectURL = vi.fn(() => 'blob:mock') as unknown as typeof URL.createObjectURL
  URL.revokeObjectURL = vi.fn() as unknown as typeof URL.revokeObjectURL
})

function findBtn(text: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll('button')].find((b) => b.textContent?.includes(text))
}

describe('Receipt share via WhatsApp', () => {
  test('PAID order: the gateway leg queues the IMAGE first and skips the device share', async () => {
    apiPostMock.mockImplementation(async (path: string) => {
      if (path.endsWith('/whatsapp-send')) return { queued: true, phone: '254722123456' }
      throw new Error('unexpected: ' + path)
    })

    mount(React.createElement(ReceiptModal, { order: order(), branding, open: true, onClose: () => {} }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    // Server send ONLY: the customer receives the branded image from the
    // shop's own number — no download, no app launch, no deep link.
    expect(apiPostMock).toHaveBeenCalledWith('/api/v1/orders/42/whatsapp-send', {})
    expect(blobMock).not.toHaveBeenCalled()
    expect(waChatMock).not.toHaveBeenCalled()
    expect(findBtn('Image queued')).toBeTruthy()
  })

  test('HOST leg: the till opens WhatsApp with the chat preloaded — no browser fallback', async () => {
    apiPostMock.mockImplementation(async (path: string) => {
      if (path.endsWith('/whatsapp-send')) throw new Error('gateway not connected')
      if (path.endsWith('/whatsapp-open')) return { opened: true, url: 'whatsapp://send?phone=254722123456' }
      throw new Error('unexpected: ' + path)
    })
    const downloadSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    mount(React.createElement(ReceiptModal, { order: order(), branding, open: true, onClose: () => {} }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    // Image saved FIRST (WhatsApp can't receive a file over a link)…
    expect(blobMock).toHaveBeenCalledWith('/api/v1/orders/42/receipt-image')
    expect(downloadSpy).toHaveBeenCalled()
    // …then the HOST launches the app; digits + caption ride the payload.
    expect(apiPostMock).toHaveBeenCalledWith('/api/v1/whatsapp-open', {
      phone: '254722123456',
      text: expect.stringContaining('Thank you for shopping with Creative Divine'),
    })
    // No browser deep link when the app opened.
    expect(waChatMock).not.toHaveBeenCalled()
    expect(findBtn('Image saved — WhatsApp opened for 254722123456')).toBeTruthy()
    downloadSpy.mockRestore()
  })

  test('HOST opened=false (WhatsApp not installed) → honest hint, no silent nothing', async () => {
    apiPostMock.mockImplementation(async (path: string) => {
      if (path.endsWith('/whatsapp-send')) throw new Error('gateway not connected')
      if (path.endsWith('/whatsapp-open')) return { opened: false, hint: 'install WhatsApp Desktop' }
      throw new Error('unexpected: ' + path)
    })

    mount(React.createElement(ReceiptModal, { order: order(), branding, open: true, onClose: () => {} }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    expect(container.textContent).toContain('WhatsApp is not installed')
    expect(waChatMock).not.toHaveBeenCalled()
  })

  test('HOST endpoint missing (old server / browser-only) → deep link with digits + caption', async () => {
    // Default mock: every api.post throws (no gateway, no whatsapp-open).
    const downloadSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    mount(React.createElement(ReceiptModal, { order: order(), branding, open: true, onClose: () => {} }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    expect(blobMock).toHaveBeenCalledWith('/api/v1/orders/42/receipt-image')
    expect(waChatMock).toHaveBeenCalledTimes(1)
    expect(waChatMock).toHaveBeenCalledWith('254722123456', expect.stringContaining("we're glad you chose us"))
    downloadSpy.mockRestore()
  })

  test('secondary Send image… rides the Web Share sheet with the file + caption', async () => {
    const share = vi.fn(async () => {})
    Object.defineProperty(navigator, 'share', { value: share, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: () => true, configurable: true })
    const downloadSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    mount(React.createElement(ReceiptModal, { order: order(), branding, open: true, onClose: () => {} }))
    expect(findBtn('Send image…')).toBeTruthy()
    await act(async () => {
      findBtn('Send image…')!.click()
    })
    const arg = share.mock.calls[0][0] as ShareData & { files: File[] }
    expect(arg.files?.[0].name).toBe('receipt-ORD-1042.png')
    expect(arg.text).toContain('Hi Amina!')
    // The share-sheet path never asks the host to launch the app.
    const openedCalls = apiPostMock.mock.calls.filter((c) => String(c[0]).endsWith('/whatsapp-open'))
    expect(openedCalls).toHaveLength(0)
    downloadSpy.mockRestore()
    Object.defineProperty(navigator, 'share', { value: undefined, configurable: true })
    Object.defineProperty(navigator, 'canShare', { value: undefined, configurable: true })
  })

  test('a stored 07… number normalizes to 2547… for the chat (the server rule)', async () => {
    const downloadSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    mount(React.createElement(ReceiptModal, {
      order: order({ payments: [{ id: 2, method: 'cash', status: 'COMPLETED', amountCents: 55000, phone: '0711 222 333' }] }),
      branding, open: true, onClose: () => {},
    }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    expect(waChatMock).toHaveBeenCalledWith('254711222333', expect.any(String))
    downloadSpy.mockRestore()
  })

  test('no customer phone on the order → chat opens without a number', async () => {
    const downloadSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    mount(React.createElement(ReceiptModal, {
      order: order({ payments: [{ id: 1, method: 'cash', status: 'COMPLETED', amountCents: 55000 }] }),
      branding, open: true, onClose: () => {},
    }))
    await act(async () => {
      findBtn('Share via WhatsApp')!.click()
    })
    expect(waChatMock).toHaveBeenCalledWith('', expect.any(String))
    downloadSpy.mockRestore()
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
})
