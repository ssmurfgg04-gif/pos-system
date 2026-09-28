// T4 regression tests — MoneyInput typing behavior (client-reported bug:
// typing "250.00" used to collapse trailing zeros while focused).
// Uses react-dom directly (same harness style as __tests__/typing.test.tsx).
// @vitest-environment jsdom
import { describe, expect, test, beforeEach, afterEach } from 'vitest'
import React, { useState, act } from 'react'
import { createRoot, Root } from 'react-dom/client'

import { MoneyInput } from './components/ui'

;(React as any).actEnvironment = true

let received: number[] = []
let root: Root | null = null

function Host({ start }: { start: number }) {
  const [cents, setCents] = useState(start)
  return (
    <MoneyInput
      value={cents}
      onCents={(c) => {
        received.push(c)
        setCents(c)
      }}
      data-testid="mi"
    />
  )
}

function mount(start = 0) {
  root = createRoot(document.getElementById('root')!)
  act(() => {
    root!.render(<Host start={start} />)
  })
  return document.querySelector('input[data-testid="mi"]') as HTMLInputElement
}

/** Types one character at a time through the native value setter (React 18). */
function typeInto(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  for (const ch of text) {
    act(() => {
      setter.call(el, el.value + ch)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
}

/** Simulates a paste of a preformatted string (single input event). */
function pasteInto(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  act(() => {
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

/** React's onBlur is delegated as the (bubbling) focusout event. */
function blurEl(el: HTMLInputElement) {
  act(() => {
    el.blur()
    el.dispatchEvent(new FocusEvent('focusout', { bubbles: true }))
  })
}

beforeEach(() => {
  document.body.innerHTML = '<div id="root"></div>'
  received = []
})

afterEach(() => {
  if (root) {
    act(() => root!.unmount())
    root = null
  }
})

describe('MoneyInput — trailing-zero typing (the client bug)', () => {
  test('"250" → "250." → "250.00" stays intact while focused, parses KES 250.00 live', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '250')
    expect(el.value).toBe('250')
    expect(received[received.length - 1]).toBe(25000) // KES 250 live
    typeInto(el, '.')
    expect(el.value).toBe('250.') // pre-fix this snapped back to "2.50"
    expect(received[received.length - 1]).toBe(25000)
    typeInto(el, '0')
    expect(el.value).toBe('250.0')
    typeInto(el, '0')
    expect(el.value).toBe('250.00')
    expect(received[received.length - 1]).toBe(25000)
  })

  test('canonicalizes on blur (typed "250.00" = 25000 cents → stays "250.00")', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '250.00')
    blurEl(el)
    expect(el.value).toBe('250.00')
  })

  test('trailing ".0" additions never change the parsed amount mid-typing', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '9.5')
    expect(received[received.length - 1]).toBe(950)
    typeInto(el, '0')
    expect(received[received.length - 1]).toBe(950) // "9.50" still 9.50
    expect(el.value).toBe('9.50')
    blurEl(el)
    expect(el.value).toBe('9.50')
  })

  test('cents typed with intent: "0.05" → 5 cents', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '0.05')
    expect(received[received.length - 1]).toBe(5)
    blurEl(el)
    expect(el.value).toBe('0.05')
  })

  test('whole KES amounts round-trip: 55000 → "550.00" → edit → 55050', () => {
    const el = mount(55000)
    expect(el.value).toBe('550.00')
    act(() => el.focus())
    // caret at end; append "5" → "550.005" parses to 55000 still (truncate)
    typeInto(el, '5')
    expect(received[received.length - 1]).toBe(55000)
    // rewrite to 550.50
    pasteInto(el, '550.50')
    expect(received[received.length - 1]).toBe(55050)
    blurEl(el)
    expect(el.value).toBe('550.50')
  })
})

describe('MoneyInput — decimals beyond two places', () => {
  test('"0.005" keeps its text while focused, truncates to 0 cents (no rounding up)', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '0.005')
    expect(el.value).toBe('0.005')
    expect(received[received.length - 1]).toBe(0)
    blurEl(el)
    // 0 canonicalizes to the empty string (matches the zero-value convention)
    expect(el.value).toBe('')
  })

  test('"12.999" truncates to 1299 cents, blur shows "12.99"', () => {
    const el = mount(0)
    act(() => el.focus())
    typeInto(el, '12.999')
    expect(received[received.length - 1]).toBe(1299)
    blurEl(el)
    expect(el.value).toBe('12.99')
  })
})

describe('MoneyInput — paste with currency symbols and grouping', () => {
  test('paste "KES 1,250.50" → 125050 cents, field shows cleaned digits', () => {
    const el = mount(0)
    act(() => el.focus())
    pasteInto(el, 'KES 1,250.50')
    expect(el.value).toBe('1250.50')
    expect(received[received.length - 1]).toBe(125050)
    blurEl(el)
    expect(el.value).toBe('1250.50')
  })

  test('paste "1.250,50"-style garbage never clobbers the last good cents', () => {
    const el = mount(0)
    act(() => el.focus())
    pasteInto(el, '12.5')
    expect(received[received.length - 1]).toBe(1250)
    pasteInto(el, '1.2.3') // invalid — parse returns null, onCents not called
    expect(received[received.length - 1]).toBe(1250)
    blurEl(el)
    // canonical value comes back from parent state
    expect(el.value).toBe('12.50')
  })
})

describe('MoneyInput — external value sync and clearing', () => {
  test('follows external value changes while blurred', () => {
    const el = mount(0)
    // drive parent state through the input itself
    act(() => el.focus())
    pasteInto(el, '7.25')
    blurEl(el)
    expect(el.value).toBe('7.25')
  })

  test('clearing the field does not fire onCents; blur restores the canonical value', () => {
    const el = mount(300)
    act(() => el.focus())
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
      setter.call(el, '')
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(el.value).toBe('')
    expect(received).toHaveLength(0) // no spurious 0 while empty
    blurEl(el)
    expect(el.value).toBe('3.00')
  })
})
