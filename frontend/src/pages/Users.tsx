// People — users + dynamic roles. Roles are permission sets the admin can
// edit freely (the seeded Admin/Cashier/Designer are editable system roles;
// custom roles can be created and deleted).

import { useEffect, useState } from 'react'
import { api, PermissionDef, Role, User } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, Select, Spinner, StatusPill, Table, Tabs, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Users as UsersIcon } from 'lucide-react'

export function Users() {
  const { user: me } = useAuth()
  const [tab, setTab] = useState<'users' | 'roles'>('users')
  const [users, setUsers] = useState<User[] | null>(null)
  const [roles, setRoles] = useState<Role[] | null>(null)
  const [catalog, setCatalog] = useState<PermissionDef[]>([])
  const [editing, setEditing] = useState<User | 'new' | null>(null)
  const [pwFor, setPwFor] = useState<User | null>(null)
  const [pinFor, setPinFor] = useState<User | null>(null)
  const [roleEditor, setRoleEditor] = useState<Role | 'new' | null>(null)

  const load = async () => {
    try {
      const [u, r, p] = await Promise.all([
        api.get<User[]>('/api/v1/users'),
        api.get<Role[]>('/api/v1/roles'),
        api.get<{ catalog: PermissionDef[] }>('/api/v1/permissions'),
      ])
      setUsers(u)
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
        sub="Staff accounts and what they're allowed to do"
        actions={
          tab === 'users' ? (
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
              { key: 'users' as const, label: `Users${users ? ` (${users.length})` : ''}` },
              { key: 'roles' as const, label: `Roles${roles ? ` (${roles.length})` : ''}` },
            ]}
            value={tab}
            onChange={setTab}
          />
        </div>

        {tab === 'users' ? (
          !users ? (
            <div className="py-12 flex justify-center"><Spinner /></div>
          ) : users.length === 0 ? (
            <EmptyState icon={<UsersIcon size={24} strokeWidth={2.25} />} title="No users" />
          ) : (
            <Table head={['Name', 'Role', 'PIN', 'Status', '']}>
              {users.map((u) => (
                <tr key={u.id} className={u.active ? '' : 'opacity-50'}>
                  <td className="px-3 py-2.5">
                    <p className="font-bold text-ink text-[13px]">{u.fullName || u.username}</p>
                    <p className="text-[11px] text-ink-subtle">@{u.username}</p>
                  </td>
                  <td className="px-3 py-2.5 text-[13px] font-semibold text-ink-muted">{u.roleName}</td>
                  <td className="px-3 py-2.5">
                    <StatusPill status={u.pinSet ? 'info' : 'void'} label={u.pinSet ? 'Set' : 'None'} />
                  </td>
                  <td className="px-3 py-2.5">
                    <StatusPill status={u.active ? 'paid' : 'void'} label={u.active ? 'Active' : 'Inactive'} />
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
              <tr key={r.id}>
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
                <td className="px-3 py-2.5 text-right">
                  <Button size="sm" variant="ghost" onClick={() => setRoleEditor(r)}>Permissions</Button>
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
                </td>
              </tr>
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
