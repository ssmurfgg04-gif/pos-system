// openWhatsAppChat (v1.6.2) — the browser-side chat opener behind the
// receipt Share button and the order-detail/design-board contact modals.
// Contract: the installed app (whatsapp://send?phone=&text=) is tried
// FIRST with the contact preloaded; wa.me (WhatsApp Web / mobile app) is
// the last-resort fallback only when nothing took focus after the app was
// given ~2s to appear — never a share sheet, never a dead click.
import { describe, expect, test, vi, afterEach } from 'vitest'
import { openWhatsAppChat, waAppDeepLink, waChatUrl, waDigits } from '../src/lib/whatsapp'

afterEach(() => {
  vi.useRealTimers()
})

describe('Kenyan phone normalization', () => {
  test('07…, 7…, +254…, spaces and 254… all become 2547… digits', () => {
    expect(waDigits('0711 222 333')).toBe('254711222333')
    expect(waDigits('711222333')).toBe('254711222333')
    expect(waDigits('+254711222333')).toBe('254711222333')
    expect(waDigits('254711222333')).toBe('254711222333')
    expect(waDigits('')).toBe('')
  })
})

describe('URL builders', () => {
  test('whatsapp://send carries phone + text', () => {
    expect(waAppDeepLink('254711222333', 'Hi')).toBe('whatsapp://send?phone=254711222333&text=Hi')
    expect(waAppDeepLink('', 'Hi')).toBe('whatsapp://send?text=Hi')
    expect(waAppDeepLink('254711222333', '')).toBe('whatsapp://send?phone=254711222333')
  })

  test('wa.me chat url works with and without a number', () => {
    expect(waChatUrl('254711222333', 'Thank you!')).toBe('https://wa.me/254711222333?text=Thank%20you!')
    expect(waChatUrl('', 'Thank you!')).toBe('https://wa.me/?text=Thank%20you!')
    expect(waChatUrl('', '')).toBe('https://wa.me/')
  })
})

describe('openWhatsAppChat', () => {
  test('deep-links the installed app first, contact preloaded', () => {
    const navigate = vi.fn()
    const open = vi.fn()
    openWhatsAppChat('254711222333', 'Thank you!', { navigate, open, delayMs: 1000, appOpened: () => true })
    expect(navigate).toHaveBeenCalledTimes(1)
    expect(navigate).toHaveBeenCalledWith('whatsapp://send?phone=254711222333&text=Thank%20you!')
  })

  test('falls back to wa.me when nothing took focus (no app installed)', () => {
    vi.useFakeTimers()
    const navigate = vi.fn()
    const open = vi.fn()
    openWhatsAppChat('254711222333', 'Thank you!', { navigate, open, delayMs: 2200, appOpened: () => false })
    expect(open).not.toHaveBeenCalled()
    vi.advanceTimersByTime(2300)
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith('https://wa.me/254711222333?text=Thank%20you!')
  })

  test('no fallback when the app took focus', () => {
    vi.useFakeTimers()
    const navigate = vi.fn()
    const open = vi.fn()
    openWhatsAppChat('254711222333', 'Thank you!', { navigate, open, delayMs: 2200, appOpened: () => true })
    vi.advanceTimersByTime(5000)
    expect(open).not.toHaveBeenCalled()
  })

  test('no phone on the order → app opens with a contact picker, fallback without a number', () => {
    vi.useFakeTimers()
    const navigate = vi.fn()
    const open = vi.fn()
    openWhatsAppChat('', 'Thank you!', { navigate, open, delayMs: 2200, appOpened: () => false })
    expect(navigate).toHaveBeenCalledWith('whatsapp://send?text=Thank%20you!')
    vi.advanceTimersByTime(2300)
    expect(open).toHaveBeenCalledWith('https://wa.me/?text=Thank%20you!')
  })
})
