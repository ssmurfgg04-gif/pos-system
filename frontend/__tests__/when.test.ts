import { describe, expect, it } from 'vitest'
import { fmtDate, fmtDateTime, fmtTime } from '../src/lib/when'

describe('when', () => {
  const iso = '2026-11-14T08:56:28'
  it('formats day-first', () => {
    expect(fmtDate(iso)).toBe('14 Nov 2026')
    expect(fmtTime(iso)).toBe('08:56')
    expect(fmtDateTime(iso)).toMatch(/^14 Nov 2026,? 08:56$/)
  })
  it('falls back on empty or invalid input', () => {
    expect(fmtDate(null)).toBe('-')
    expect(fmtDateTime('', 'never')).toBe('never')
    expect(fmtTime('garbage')).toBe('-')
  })
})

import { compactKes, shortDay } from '../src/pages/Reports'

describe('reports chart labels', () => {
  it('compacts shilling amounts from cents', () => {
    expect(compactKes(0)).toBe('–')
    expect(compactKes(45_000)).toBe('450')
    expect(compactKes(950_000)).toBe('9.5k')
    expect(compactKes(1_250_000)).toBe('13k')
    expect(compactKes(4_500_000)).toBe('45k')
    expect(compactKes(230_000_000)).toBe('2.3M')
  })
  it('labels a day as weekday + date', () => {
    expect(shortDay('2026-11-14')).toBe('Sat 14')
    expect(shortDay('bad')).toBe('bad'.slice(5))
  })
})
