// Regression: the Paystack popup MUST use the Popup V2 access-code contract
// — new PaystackPop().resumeTransaction(accessCode, callbacks) — and must
// NEVER pass a client-side amount. The v1 setup({key, email, access_code})
// call ignored the access_code and threw "Transaction amount not set."
// (the client's reported bug); the amount now lives server-side on the
// initialized transaction, so the popup cannot show or charge anything else.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import { openPaystackPopup, type PaystackInitResult } from '../src/lib/api'

const init: PaystackInitResult = {
  reference: 'LP-ORD-100-ab12',
  accessCode: 'ACCESS_CODE_XYZ',
  authorizationUrl: 'https://checkout.paystack.com/abc123',
  publicKey: 'pk_test_xxx',
  currency: 'KES',
  amountCents: 60000, // KES 600.00 in subunits — server-owned
}

let resumeSpy: ReturnType<typeof vi.fn>

beforeEach(() => {
  resumeSpy = vi.fn(() => ({ openIframe: vi.fn() }))
  ;(window as any).PaystackPop = class {
    resumeTransaction = resumeSpy
    cancelTransaction = vi.fn()
    isLoaded = () => true
  }
})

describe('openPaystackPopup (Popup V2 contract)', () => {
  test('resumes the server-minted access code — never a client amount', () => {
    const onSuccess = vi.fn()
    const onCancelled = vi.fn()
    openPaystackPopup(init, 'cust@example.com', { onSuccess, onCancelled })
    expect(resumeSpy).toHaveBeenCalledTimes(1)
    const [accessCode, callbacks] = resumeSpy.mock.calls[0]
    // The ONLY transaction identifier is the access code from the backend.
    expect(accessCode).toBe('ACCESS_CODE_XYZ')
    // V2 resume takes no transaction payload: no amount, key, email or
    // currency may be smuggled through — the server owns the amount.
    expect(callbacks.amount).toBeUndefined()
    expect(callbacks.key).toBeUndefined()
    expect(callbacks.email).toBeUndefined()
    expect(typeof callbacks.onSuccess).toBe('function')
    expect(typeof callbacks.onCancel).toBe('function')
    expect(typeof callbacks.onError).toBe('function')
  })

  test('onSuccess callback resolves with the reference', () => {
    const onSuccess = vi.fn()
    const onCancelled = vi.fn()
    openPaystackPopup(init, 'cust@example.com', { onSuccess, onCancelled })
    const callbacks = resumeSpy.mock.calls[0][1]
    callbacks.onSuccess({ id: 1, reference: 'LP-ORD-100-ab12', message: 'Approved' })
    expect(onSuccess).toHaveBeenCalledWith('LP-ORD-100-ab12')
    expect(onCancelled).not.toHaveBeenCalled()
  })

  test('onCancel callback surfaces as onCancelled', () => {
    const onSuccess = vi.fn()
    const onCancelled = vi.fn()
    openPaystackPopup(init, 'cust@example.com', { onSuccess, onCancelled })
    resumeSpy.mock.calls[0][1].onCancel()
    expect(onCancelled).toHaveBeenCalledTimes(1)
    expect(onSuccess).not.toHaveBeenCalled()
  })

  test('onError surfaces the gateway message and cancels', () => {
    const onSuccess = vi.fn()
    const onCancelled = vi.fn()
    const onError = vi.fn()
    openPaystackPopup(init, 'cust@example.com', { onSuccess, onCancelled, onError })
    resumeSpy.mock.calls[0][1].onError({ message: 'Transaction initialization failed' })
    expect(onError).toHaveBeenCalledWith('Transaction initialization failed')
    expect(onCancelled).toHaveBeenCalledTimes(1)
    expect(onSuccess).not.toHaveBeenCalled()
  })

  test('missing library degrades to onError + onCancelled without throwing', () => {
    delete (window as any).PaystackPop
    const onSuccess = vi.fn()
    const onCancelled = vi.fn()
    const onError = vi.fn()
    expect(() =>
      openPaystackPopup(init, 'cust@example.com', { onSuccess, onCancelled, onError }),
    ).not.toThrow()
    expect(onError).toHaveBeenCalled()
    expect(onCancelled).toHaveBeenCalled()
    expect(onSuccess).not.toHaveBeenCalled()
  })
})
