import { describe, it, expect } from 'vitest'
import { newRow, rowsToFields, validateRows } from './KeyValueFields.jsx'

describe('rowsToFields', () => {
  it('converts rows to a key/value map, trimming keys', () => {
    const rows = [newRow('  a  ', '1'), newRow('b', '2')]
    expect(rowsToFields(rows)).toEqual({ a: '1', b: '2' })
  })

  it('drops rows with a blank/whitespace-only key', () => {
    const rows = [newRow('a', '1'), newRow('   ', 'orphaned-value')]
    expect(rowsToFields(rows)).toEqual({ a: '1' })
  })

  it('collapses duplicate trimmed keys to the last row (last-one-wins)', () => {
    const rows = [newRow('a', 'first'), newRow('a', 'second')]
    expect(rowsToFields(rows)).toEqual({ a: 'second' })
  })
})

describe('validateRows', () => {
  it('accepts valid rows and returns the same map rowsToFields would', () => {
    const rows = [newRow('a', '1'), newRow('b', '2')]
    const { fields, error } = validateRows(rows)
    expect(error).toBeNull()
    expect(fields).toEqual({ a: '1', b: '2' })
  })

  it('allows a still-empty row (blank key, blank value) — the common "just added a row" state', () => {
    const rows = [newRow('a', '1'), newRow('', '')]
    const { fields, error } = validateRows(rows)
    expect(error).toBeNull()
    expect(fields).toEqual({ a: '1' })
  })

  it('rejects two rows with the same trimmed key', () => {
    const rows = [newRow('a', 'first'), newRow(' a ', 'second')]
    const { fields, error } = validateRows(rows)
    expect(fields).toBeNull()
    expect(error).toMatch(/duplicate/i)
    expect(error).toContain('"a"')
  })

  it('rejects a row with a value but a blank key', () => {
    const rows = [newRow('a', '1'), newRow('   ', 'orphaned-value')]
    const { fields, error } = validateRows(rows)
    expect(fields).toBeNull()
    expect(error).toMatch(/key/i)
  })
})
