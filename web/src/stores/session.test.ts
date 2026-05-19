import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useSessionStore } from './session'

describe('useSessionStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('starts unloaded and unauthenticated', () => {
    const session = useSessionStore()
    expect(session.loaded).toBe(false)
    expect(session.authenticated).toBe(false)
    expect(session.user).toBeNull()
  })
})
