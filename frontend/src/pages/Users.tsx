// People — the team hub. Team tab: who is on the roster, what they sold
// today, and their account status (with the full user-edit admin actions).
// Roles & Dashboards tab: dynamic roles (permission sets the admin can edit
// freely — the seeded Admin/Cashier/Designer are editable system roles;
// custom roles can be created and deleted) plus a per-role dashboard editor
// (landing page + which nav sections its members see).

import { Fragment, useEffect, useState } from 'react'
import { api, PermissionDef, Role, TeamMember, User } from '../lib/api'
import { useAuth } from '../stores/auth'
import { formatMoney } from '../lib/money'
import { Button, Card, EmptyState, Field, Input, Modal, Select, Spinner, StatusPill, Table, Tabs, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Users as UsersIcon, LayoutDashboard, ChevronDown, ChevronUp } from 'lucide-react'

// Landing-page keys accepted by PUT /roles/:id/dashboard ("" = follow the
// permission cascade). Keep in sync with the SPA routes.
const HOME_PAGES: { value: string; label: string }[] = [
  { value: '', label: 'Default (first page their permissions allow)' },
  { value: 'pos', label: 'Sell (POS)' },
  { value: 'orders', label: 'Orders' },
  { value: 'inventory', label: 'Inventory' },
  { value: 'customers', label: 'Customers' },
  { value: 'suppliers', label: 'Suppliers' },
  { value: 'shifts', label: 'Shifts & cash' },
  { value: 'reports', label: 'Reports' },
  { value: 'design', label: 'Design board' },
  { value: 'team', label: 'Team' },
  { value: 'settings', label: 'Settings' },
  { value: 'users', label: 'People' },
]

// Main-nav keys the dashboard editor can show/hide per role (checked = visible).
const NAV_KEYS: { key: string; label: string }[] = [
  { key: 'pos', label: 'Sell' },
  { key: 'orders', label: 'Orders' },
  { key: 'customers', label: 'Customers' },
  { key: 'design', label: 'Design' },
  { key: 'inventory', label: 'Inventory' },
  { key: 'suppliers', label: 'Suppliers' },
  { key: 'shifts', label: 'Shifts' },
  { key: 'reports', label: 'Reports' },
  { key: 'users', label: 'People' },
  { key: 'settings', label: 'Settings' },
]

// GET /roles/:id wire shape: the server speaks homePage + dashboard.hiddenNav
// (Go models.Role) while the shared client type names them home_page +
// dashboard_config.hideNav — accept both, so the editor works either way.
type RoleDetail = Role & {
  homePage?: string
  dashboard?: { hiddenNav?: string[]; hiddenStats?: string[]; widgetsOrder?: string[] }
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

export function Users() {
  const { user: me } = useAuth()
  const [tab, setTab] = useState<'team' | 'roles'>('team')
  const [members, setMembers] = useState<TeamMember[] | null>(null)
  const [roles, setRoles] = useState<Role[] | null>(null)
  const [catalog, setCatalog] = useState<PermissionDef[]>([])
  const [editing, setEditing] = useState<User | 'new' | null>(null)
  const [pwFor, setPwFor] = useState<User | null>(null)
  const [pinFor, setPinFor] = useState<User | null>(null)
  const [roleEditor, setRoleEditor] = useState<Role | 'new' | null>(null)
  const [dashFor, setDashFor] = useState<number | null>(null)

  const load = async () => {
    try {
      const [team, r, p] = await Promise.all([
        api.get<TeamMember[]>('/api/v1/team').catch(() =>
          // Older/demo backends without the overview endpoint still work —
          // just without the per-member sales stats.
          api.get<User[]>('/api/v1/users').then((us) =>
            us.map((u) => ({ ...u, salesToday: 0, salesTodayCents: 0, lastOrderAt: '' })),
          ),
        ),
        api.get<Role[]>('/api/v1/roles'),
        api.get<{ catalog: PermissionDef[] }>('/api/v1/permissions'),
      ])
      setMembers(team)
      setRoles(r)
      setCatalog(p.catalog)
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const deactivate = async (u: User) => {
    if (!confirm(`Deactivate ${u.fullName || u.username}? They won't be able to sign in.`)) return
    try {
      await api.del(`/api/v1/users/${u.id}`)
      toast.success('User deactivated', u.username)
      load()
    } catch (e: any) {
      toast.error('Failed', e?.message)
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="People"
        sub="The team, what they moved today, and what they're allowed to do"
        actions={
          tab === 'team' ? (
            <Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ User</Button>
          ) : (
            <Button variant="primary" size="sm" onClick={() => setRoleEditor('new')}>+ Role</Button>
          )
        }
        pad={false}
      >
        <div className="px-4 py-3">
          <Tabs
            tabs={[
              { key: 'team' as const, label: `Team${members ? ` (${members.length})` : ''}` },
              { key: 'roles' as const, label: `Roles & Dashboards${roles ? ` (${roles.length})` : ''}` },
            ]}
            value={tab}
            onChange={setTab}
          />
        </div>

        {tab === 'team' ? (
          !members ? (
            <div className="py-12 flex justify-center"><Spinner /></div>
          ) : members.length === 0 ? (
            <EmptyState icon={<UsersIcon size={24} strokeWidth={2.25} />} title="No users" />
          ) : (
            <Table head={['Member', 'Role', 'Today', 'Last order', 'Status', '']}>
              {members.map((u) => (
                <tr key={u.id} className={u.active ? '' : 'opacity-50'}>
                  <td className="px-3 py-2.5">
                    <div className="flex items-center gap-2.5">
                      <span
                        className="w-9 h-9 rounded-input bg-brand border-2 border-brand-strong text-brand-ink font-black text-[12px] flex items-center justify-center shrink-0"
                        aria-hidden
                      >
                        {initials(u.fullName || u.username)}
                      </span>
                      <span className="min-w-0">
                        <p className="font-bold text-ink text-[13px] leading-tight">{u.fullName || u.username}</p>
                        <p className="text-[11px] text-ink-subtle">@{u.username}</p>
                      </span>
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    <span className="inline-block bg-surface-muted border border-line rounded-pill px-2 py-0.5 text-[11px] font-bold text-ink-muted whitespace-nowrap">
                      {u.roleName}
                    </span>
                  </td>
                  <td className="px-3 py-2.5">
                    {u.salesToday > 0 ? (
                      <span>
                        <span className="font-bold tabular text-ink text-[13px]">{u.salesToday}</span>
                        <span className="block text-[11px] text-ink-muted tabular">{formatMoney(u.salesTodayCents)}</span>
                      </span>
                    ) : (
                      <span className="text-[12px] text-ink-subtle">no sales yet</span>
                    )}
                  </td>
                  <td className="px-3 py-2.5 text-[12px] text-ink-muted tabular whitespace-nowrap">
                    {u.lastOrderAt ? new Date(u.lastOrderAt).toLocaleString() : '—'}
                  </td>
                  <td className="px-3 py-2.5">
                    <span className="inline-flex flex-wrap items-center gap-1">
                      <StatusPill status={u.active ? 'paid' : 'void'} label={u.active ? 'Active' : 'Inactive'} />
                      <StatusPill status={u.pinSet ? 'info' : 'void'} label={u.pinSet ? 'PIN set' : 'No PIN'} />
                      {u.mustRotate && <StatusPill status="pending" label="Rotate password" />}
                    </span>
                  </td>
                  <td className="px-3 py-2.5 text-right whitespace-nowrap">
                    <span className="inline-flex items-center gap-1">
                      <Button size="sm" variant="ghost" onClick={() => setEditing(u)}>Edit</Button>
                      <Button size="sm" variant="ghost" onClick={() => setPwFor(u)}>Password</Button>
                      <Button size="sm" variant="ghost" onClick={() => setPinFor(u)}>PIN</Button>
                      {u.active && u.id !== me?.id && (
                        <Button size="sm" variant="ghost" className="text-danger-text ml-2 border-l-2 border-line pl-3" onClick={() => deactivate(u)}>Deactivate</Button>
                      )}
                    </span>
                  </td>
                </tr>
              ))}
            </Table>
          )
        ) : !roles ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : (
          <Table head={['Role', 'Users', 'Permissions', '']}>
            {roles.map((r) => (
              <Fragment key={r.id}>
                <tr>
                  <td className="px-3 py-2.5">
                    <p className="font-bold text-ink text-[13px]">
                      {r.name} {r.system && <span className="text-[10px] uppercase text-ink-subtle font-bold">system</span>}
                    </p>
                    {r.description && <p className="text-[11px] text-ink-subtle">{r.description}</p>}
                  </td>
                  <td className="px-3 py-2.5 tabular text-ink-muted">{r.userCount ?? 0}</td>
                  <td className="px-3 py-2.5 text-[12px] text-ink-muted">
                    {r.permissions.length} permission{r.permissions.length === 1 ? '' : 's'}
                    <span className="block text-ink-subtle truncate max-w-56">{r.permissions.slice(0, 4).join(', ')}{r.permissions.length > 4 ? '…' : ''}</span>
                  </td>
                  <td className="px-3 py-2.5 text-right whitespace-nowrap">
                    <span className="inline-flex items-center gap-1">
                      <Button size="sm" variant="ghost" onClick={() => setRoleEditor(r)}>Permissions</Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setDashFor(dashFor === r.id ? null : r.id)}
                      >
                        <LayoutDashboard size={14} strokeWidth={2.25} aria-hidden />
                        Dashboard
                        {dashFor === r.id
                          ? <ChevronUp size={13} strokeWidth={2.5} aria-hidden />
                          : <ChevronDown size={13} strokeWidth={2.5} aria-hidden />}
                      </Button>
                      {!r.system && (
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-danger-text"
                          onClick={async () => {
                            if (!confirm(`Delete role ${r.name}?`)) return
                            try {
                              await api.del(`/api/v1/roles/${r.id}`)
                              toast.success('Role deleted')
                              load()
                            } catch (e: any) {
                              toast.error('Cannot delete', e?.message)
                            }
                          }}
                        >
                          Delete
                        </Button>
                      )}
                    </span>
                  </td>
                </tr>
                {dashFor === r.id && (
                  <tr className="bg-surface-muted/60">
                    <td colSpan={4} className="px-3 py-3">
                      <DashboardEditor roleId={r.id} roleName={r.name} />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </Table>
        )}
      </Card>

      {editing && (
        <UserModal
          user={editing === 'new' ? null : editing}
          roles={roles ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load() }}
        />
      )}
      {pwFor && <SecretModal kind="password" user={pwFor} onClose={() => setPwFor(null)} />}
      {pinFor && <SecretModal kind="pin" user={pinFor} onClose={() => setPinFor(null)} />}
      {roleEditor && (
        <RoleModal
          role={roleEditor === 'new' ? null : roleEditor}
          catalog={catalog}
          onClose={() => setRoleEditor(null)}
          onSaved={() => { setRoleEditor(null); load() }}
        />
      )}
    </div>
  )
}

// Per-role landing page + nav visibility. Expand a role row to load its
// dashboard config (GET /roles/:id) and save via PUT /roles/:id/dashboard.
function DashboardEditor({ roleId, roleName }: { roleId: number; roleName: string }) {
  const [role, setRole] = useState<RoleDetail | null>(null)
  const [homePage, setHomePage] = useState('')
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    api
      .get<Role>(`/api/v1/roles/${roleId}`)
      .then((r) => {
        if (!alive) return
        const rr = r as RoleDetail
        const hiddenNav = rr.dashboard?.hiddenNav ?? rr.dashboard_config?.hiddenNav ?? []
        setRole(rr)
        setHomePage(rr.homePage ?? rr.home_page ?? '')
        setHidden(new Set(hiddenNav))
      })
      .catch((e: any) => {
        if (alive) setError(e?.message || 'Could not load role')
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [roleId])

  const toggle = (key: string) => {
    const next = new Set(hidden)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setHidden(next)
  }

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      // The nav-hide list is stored under the Go models.DashboardConfig
      // wire shape (hiddenNav). Any extra widget config that came back from
      // the server is passed through untouched.
      const config: Record<string, unknown> = { hiddenNav: [...hidden] }
      if (role?.dashboard?.hiddenStats) config.hiddenStats = role.dashboard.hiddenStats
      if (role?.dashboard?.widgetsOrder) config.widgetsOrder = role.dashboard.widgetsOrder
      if (role?.dashboard_config?.widgets) config.widgets = role.dashboard_config.widgets
      await api.put(`/api/v1/roles/${roleId}/dashboard`, { homePage, config })
      toast.success('Dashboard saved', `${roleName} — ${HOME_PAGES.find((h) => h.value === homePage)?.label ?? homePage}`)
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  if (loading) {
    return <div className="py-4 flex justify-center"><Spinner /></div>
  }

  return (
    <div className="space-y-3" aria-label={`Dashboard for ${roleName}`}>
      <p className="text-[12px] uppercase font-bold text-ink-muted">Dashboard — what this role lands on</p>
      <Field label="Landing page" hint="Where members of this role land after signing in.">
        <Select value={homePage} onChange={(e) => setHomePage(e.target.value)}>
          {HOME_PAGES.map((h) => (
            <option key={h.value} value={h.value}>{h.label}</option>
          ))}
        </Select>
      </Field>
      <div>
        <p className="text-[13px] font-semibold text-ink-muted mb-1">Main navigation</p>
        <p className="text-xs text-ink-subtle mb-1.5">Checked = visible for this role. Permissions still decide the final say.</p>
        <div className="grid sm:grid-cols-2 gap-1.5">
          {NAV_KEYS.map((n) => (
            <label key={n.key} className="flex items-center gap-2 p-2 rounded-[5px] hover:bg-surface cursor-pointer min-h-11">
              <input
                type="checkbox"
                checked={!hidden.has(n.key)}
                onChange={() => toggle(n.key)}
                className="w-5 h-5 accent-[#10B981]"
              />
              <span>
                <span className="block text-[13px] font-semibold text-ink">{n.label}</span>
                <span className="block text-[11px] text-ink-subtle font-mono">{n.key}</span>
              </span>
            </label>
          ))}
        </div>
      </div>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      <div>
        <Button variant="primary" size="sm" onClick={save} disabled={busy}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Save dashboard'}
        </Button>
      </div>
    </div>
  )
}

function UserModal({ user, roles, onClose, onSaved }: { user: User | null; roles: Role[]; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState({
    username: user?.username ?? '',
    fullName: user?.fullName ?? '',
    roleId: user?.roleId ?? roles[0]?.id ?? 0,
    password: '',
    pin: '',
    active: user?.active ?? true,
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      if (user) {
        await api.put(`/api/v1/users/${user.id}`, {
          username: form.username, fullName: form.fullName, roleId: form.roleId, active: form.active,
        })
        if (form.password) await api.put(`/api/v1/users/${user.id}/password`, { password: form.password })
        if (form.pin) await api.put(`/api/v1/users/${user.id}/pin`, { pin: form.pin })
        toast.success('User updated', form.username)
      } else {
        if (!form.password) throw new Error('Password required for new users')
        if (form.pin && form.pin.length !== 4) throw new Error('PIN must be exactly 4 digits')
        await api.post('/api/v1/users', form)
        toast.success('User created', form.username)
      }
      onSaved()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={user ? `Edit — ${user.username}` : 'New user'} footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !form.username.trim()}>
          {busy ? <Spinner className="border-t-brand-ink" /> : user ? 'Save' : 'Create user'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <Field label="Username">
          <Input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} disabled={!!user} autoFocus />
        </Field>
        <Field label="Full name">
          <Input value={form.fullName} onChange={(e) => setForm({ ...form, fullName: e.target.value })} />
        </Field>
        <Field label="Role">
          <Select value={form.roleId} onChange={(e) => setForm({ ...form, roleId: Number(e.target.value) })}>
            {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
          </Select>
        </Field>
        <Field label={user ? 'New password (blank = keep)' : 'Password'}>
          <Input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} autoComplete="new-password" />
        </Field>
        <Field label={user ? 'New PIN (blank = keep)' : 'Quick-switch PIN (4 digits)'}>
          <Input
            value={form.pin}
            onChange={(e) => setForm({ ...form, pin: e.target.value.replace(/\D/g, '').slice(0, 4) })}
            inputMode="numeric"
            placeholder="1234"
            className="tabular tracking-widest text-center font-bold"
          />
        </Field>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} className="w-5 h-5 accent-[#10B981]" />
          Active (can sign in)
        </label>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function SecretModal({ kind, user, onClose }: { kind: 'password' | 'pin'; user: User; onClose: () => void }) {
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      if (kind === 'password') {
        await api.put(`/api/v1/users/${user.id}/password`, { password: value })
      } else {
        await api.put(`/api/v1/users/${user.id}/pin`, { pin: value })
      }
      toast.success(kind === 'password' ? 'Password updated' : 'PIN updated', `for ${user.username}`)
      onClose()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  const ok = kind === 'pin' ? /^\d{4}$/.test(value) : value.length >= 6

  return (
    <Modal open onClose={onClose} title={`${kind === 'password' ? 'Password' : 'PIN'} — ${user.username}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !ok}>Save</Button>
      </>
    }>
      <Field label={kind === 'password' ? 'New password (min 6 chars)' : 'New 4-digit PIN'}>
        <Input
          type={kind === 'password' ? 'password' : 'text'}
          value={value}
          onChange={(e) => setValue(kind === 'pin' ? e.target.value.replace(/\D/g, '').slice(0, 4) : e.target.value)}
          inputMode={kind === 'pin' ? 'numeric' : undefined}
          className={kind === 'pin' ? 'tabular tracking-widest text-center font-bold' : ''}
          autoFocus
        />
      </Field>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}

function RoleModal({ role, catalog, onClose, onSaved }: { role: Role | null; catalog: PermissionDef[]; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(role?.name ?? '')
  const [description, setDescription] = useState(role?.description ?? '')
  const [perms, setPerms] = useState<Set<string>>(new Set(role?.permissions ?? []))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const groups = catalog.reduce<Record<string, PermissionDef[]>>((acc, p) => {
    ;(acc[p.group] = acc[p.group] || []).push(p)
    return acc
  }, {})

  const toggle = (key: string) => {
    const next = new Set(perms)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setPerms(next)
  }

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      const body = { name, description, permissions: [...perms] }
      if (role) {
        await api.put(`/api/v1/roles/${role.id}`, body)
        toast.success('Role updated', `${name}: ${perms.size} permissions`)
      } else {
        await api.post('/api/v1/roles', body)
        toast.success('Role created', name)
      }
      onSaved()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={role ? `Permissions — ${role.name}` : 'New role'} size="lg" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !name.trim()}>
          {busy ? <Spinner className="border-t-brand-ink" /> : `Save (${perms.size})`}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <Field label="Role name">
          <Input value={name} onChange={(e) => setName(e.target.value)} disabled={!!role?.system} />
        </Field>
        <Field label="Description">
          <Textarea value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <div className="space-y-3">
          {Object.entries(groups).map(([group, items]) => (
            <fieldset key={group} className="border-2 border-line rounded-input p-3">
              <legend className="text-[12px] uppercase font-bold text-ink-muted px-1.5">{group}</legend>
              <div className="grid sm:grid-cols-2 gap-1.5">
                {items.map((p) => (
                  <label key={p.key} className="flex items-start gap-2 p-2 rounded-[5px] hover:bg-surface-muted cursor-pointer min-h-11">
                    <input
                      type="checkbox"
                      checked={perms.has(p.key)}
                      onChange={() => toggle(p.key)}
                      className="w-5 h-5 mt-0.5 accent-[#10B981]"
                    />
                    <span>
                      <span className="block text-[13px] font-semibold text-ink">{p.label}</span>
                      <span className="block text-[11px] text-ink-subtle font-mono">{p.key}</span>
                    </span>
                  </label>
                ))}
              </div>
            </fieldset>
          ))}
        </div>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}
