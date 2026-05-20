// Auth-shaped errors thrown by AdminClient. The Connect-RPC client
// (when proto/admin.v1 lands) will translate `code: unauthenticated`
// and `code: permission_denied` into these; the router guard reacts
// to the `kind` field without caring whether it came from a mock or
// from the wire.

export type AdminAuthErrorKind = 'unauthenticated' | 'permission_denied'

export class AdminAuthError extends Error {
  readonly kind: AdminAuthErrorKind
  constructor(kind: AdminAuthErrorKind, message?: string) {
    super(message ?? kind)
    this.name = 'AdminAuthError'
    this.kind = kind
  }
}

export function isAdminAuthError(err: unknown): err is AdminAuthError {
  return err instanceof AdminAuthError
}
