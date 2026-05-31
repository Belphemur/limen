import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ContextJsonEditor from '../../../shared/src/components/ContextJsonEditor.vue'

function mk(initial = '') {
  return mount(ContextJsonEditor, {
    props: { modelValue: initial, 'onUpdate:modelValue': () => {} },
  })
}

describe('ContextJsonEditor', () => {
  it('treats empty input as valid', () => {
    const w = mk('')
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Empty/)
  })

  it('accepts a valid JSON object and reports byte length', () => {
    const w = mk('{"cloudId": "abc"}')
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Valid · \d+ B/)
  })

  it('rejects malformed JSON', () => {
    const w = mk('{not json')
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Invalid JSON/)
  })

  it('rejects non-object JSON', () => {
    const w = mk('[1, 2, 3]')
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Must be a JSON object/)
  })

  it('rejects invalid key shapes', () => {
    const w = mk('{"1bad": 1}')
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Invalid key/)
  })

  it('rejects payloads larger than the cap', () => {
    const big = '{"x":"' + 'a'.repeat(5000) + '"}'
    const w = mk(big)
    expect(w.get('[data-testid="context-json-status"]').text()).toMatch(/Too large/)
  })

  it('emits update:valid when the parse state flips', async () => {
    const w = mk('')
    await w.setProps({ modelValue: '{not json' })
    // Manually trigger an input event for the watcher in the component.
    const ta = w.get('textarea')
    await ta.setValue('{not json')
    const emitted = w.emitted('update:valid')
    expect(emitted).toBeTruthy()
    expect(emitted![emitted!.length - 1][0]).toBe(false)
  })
})
