// SessionAuthError is the typed exception every SessionService caller
// raises when the wire returns a Connect status code in the auth
// family. The router guard inspects `kind` to choose between
// hard-redirecting to the tenant login endpoint (`unauthenticated`)
// and routing to the in-SPA `/forbidden` page (`permission_denied`).
//
// Kept tiny and dependency-free: any module that needs to surface an
// auth-shaped failure (the SessionService client, future fixtures,
// per-SPA service clients on shared infrastructure) imports this
// without dragging in Connect or Pinia.

export type SessionAuthErrorKind = 'unauthenticated' | 'permission_denied'

export class SessionAuthError extends Error {
  readonly kind: SessionAuthErrorKind
  constructor(kind: SessionAuthErrorKind, message?: string) {
    super(message ?? kind)
    this.name = 'SessionAuthError'
    this.kind = kind
  }
}

export function isSessionAuthError(err: unknown): err is SessionAuthError {
  return err instanceof SessionAuthError
}
