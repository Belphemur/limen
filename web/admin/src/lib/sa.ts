import { ServiceAccountRole } from '@/gen/limen/admin/v1/admin_pb'

export function formatDate(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString('en-US', { year: 'numeric', month: 'short', day: 'numeric' })
}

export function roleLabel(role: ServiceAccountRole): string {
  switch (role) {
    case ServiceAccountRole.ADMIN:
      return 'Admin'
    case ServiceAccountRole.MEMBER:
      return 'Member'
    default:
      return 'Unknown'
  }
}

export function roleClass(role: ServiceAccountRole): string {
  switch (role) {
    case ServiceAccountRole.ADMIN:
      return 'bg-primary/15 text-primary'
    case ServiceAccountRole.MEMBER:
      return 'bg-surface-variant text-on-surface-variant'
    default:
      return 'bg-surface-variant text-on-surface-variant'
  }
}
