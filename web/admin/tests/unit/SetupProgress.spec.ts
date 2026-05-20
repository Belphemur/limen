import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import SetupProgress, { progressPercent } from '@/components/SetupProgress.vue'

describe('progressPercent', () => {
  it('renders 0% when nothing is done', () => {
    expect(progressPercent(0, 3)).toBe(0)
  })
  it('renders 33% at 1 of 3', () => {
    expect(progressPercent(1, 3)).toBe(33)
  })
  it('renders 100% when fully done', () => {
    expect(progressPercent(3, 3)).toBe(100)
  })
  it('clamps when completed exceeds total', () => {
    expect(progressPercent(5, 3)).toBe(100)
  })
  it('returns 0 for a zero-total guard', () => {
    expect(progressPercent(0, 0)).toBe(0)
  })
})

describe('SetupProgress component', () => {
  it('shows the X of N copy and percent', () => {
    const wrapper = mount(SetupProgress, { props: { completed: 1, total: 3 } })
    expect(wrapper.text()).toContain('1 of 3 steps completed')
    expect(wrapper.text()).toContain('33%')
  })

  it('sets the progress bar width to the computed percent', () => {
    const wrapper = mount(SetupProgress, { props: { completed: 3, total: 3 } })
    const bar = wrapper.find('[role="progressbar"] > div')
    expect(bar.attributes('style')).toContain('width: 100%')
  })
})
