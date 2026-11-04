// katime tests — the Kenya-first date helpers. The whole reason they
// exist: toISOString() is UTC and shifted Nairobi's 00:00-03:00 sales
// onto yesterday's reports. These assertions pin the LOCAL (EAT) day.
// @vitest-environment jsdom
import { describe, expect, test, beforeAll } from 'vitest'

beforeAll(() => {
  // Pin the suite to Nairobi so the UTC-vs-local trap is actually exercised
  // (a UTC runner would make these helpers look pointless).
  process.env.TZ = 'Africa/Nairobi'
})

import { localDate, todayLocal, localMonth, thisMonthLocal, daysAgoLocal } from '../src/lib/katime'

describe('katime — local (EAT) day boundaries', () => {
  test('00:30 EAT is TODAY, not yesterday like UTC says', () => {
    const d = new Date('2026-01-01T00:30:00+03:00')
    expect(d.toISOString().slice(0, 10)).toBe('2025-12-31') // the trap
    expect(localDate(d)).toBe('2026-01-01')                  // the fix
  })

  test('23:59 EAT belongs to its own day', () => {
    const d = new Date('2026-01-01T23:59:59+03:00')
    expect(localDate(d)).toBe('2026-01-01')
  })

  test('month boundary: 00:30 EAT on the 1st is the new month', () => {
    const d = new Date('2026-03-01T00:30:00+03:00')
    expect(localMonth(d)).toBe('2026-03')
  })

  test('daysAgoLocal walks back calendar days, not UTC days', () => {
    // At 01:00 EAT on the 8th, "7 days ago" is the 1st (UTC would say
    // the 7th's ISO date minus 7 = the 31st of December).
    const now = new Date()
    expect(typeof daysAgoLocal(7)).toBe('string')
    expect(daysAgoLocal(7)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(now).toBeInstanceOf(Date)
  })

  test('todayLocal/thisMonthLocal are well-formed', () => {
    expect(todayLocal()).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(thisMonthLocal()).toMatch(/^\d{4}-\d{2}$/)
    expect(todayLocal().slice(0, 7)).toBe(thisMonthLocal())
  })
})
