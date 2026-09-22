import {
  Badge,
  Box,
  Button,
  Field,
  Heading,
  HStack,
  Input,
  NativeSelect,
  Spinner,
  Stack,
  Table,
  Text,
} from '@chakra-ui/react'
import { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import ErrorAlert from '../components/ErrorAlert.jsx'
import ConfirmDialog from '../components/ConfirmDialog.jsx'
import { toaster } from '../components/toaster.jsx'

// Users is the superadmin-only user management panel (password auth mode):
// list local accounts, create users/superadmins, reset passwords, delete
// accounts. It renders nothing unless the caller verified isSuperadmin.
// currentEmail is the signed-in superadmin: their own row cannot be deleted
// (the API also rejects self-delete with 409 — this just hides the trap).
export default function Users({ currentEmail }) {
  const selfEmail = (currentEmail || '').toLowerCase()
  const [users, setUsers] = useState(null)
  const [error, setError] = useState(null)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('user')
  const [createError, setCreateError] = useState(null)
  const [creating, setCreating] = useState(false)
  const [pendingDelete, setPendingDelete] = useState(null)
  const [resetFor, setResetFor] = useState(null)
  const [resetPassword, setResetPassword] = useState('')
  const [resetError, setResetError] = useState(null)
  const [working, setWorking] = useState(false)

  const load = useCallback(async () => {
    try {
      const data = await api.get('/api/users')
      setUsers(data?.users ?? [])
      setError(null)
    } catch (loadFailure) {
      setError(loadFailure.message)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const submitCreate = async (event) => {
    event.preventDefault()
    setCreateError(null)
    setCreating(true)
    try {
      await api.post('/api/users', { email: email.trim(), password, role })
      toaster.success({ title: 'User created', description: email.trim() })
      setEmail('')
      setPassword('')
      setRole('user')
      load()
    } catch (createFailure) {
      setCreateError(createFailure.message)
    } finally {
      setCreating(false)
    }
  }

  const confirmDelete = async () => {
    if (!pendingDelete) return
    // Defense in depth: the button is disabled for your own row, and the
    // API rejects self-delete — never send the request for yourself.
    if (pendingDelete.email.toLowerCase() === selfEmail) return
    setWorking(true)
    try {
      await api.del(`/api/users/${pendingDelete.id}`)
      toaster.success({ title: 'User deleted', description: pendingDelete.email })
      setPendingDelete(null)
      load()
    } catch (deleteFailure) {
      toaster.error({ title: `Could not delete "${pendingDelete.email}"`, description: deleteFailure.message })
    } finally {
      setWorking(false)
    }
  }

  const submitReset = async (event) => {
    event.preventDefault()
    if (!resetFor) return
    setResetError(null)
    setWorking(true)
    try {
      await api.post(`/api/users/${resetFor.id}/password`, { password: resetPassword })
      toaster.success({ title: 'Password updated', description: resetFor.email })
      setResetFor(null)
      setResetPassword('')
    } catch (resetFailure) {
      setResetError(resetFailure.message)
    } finally {
      setWorking(false)
    }
  }

  return (
    <Box borderWidth="1px" borderRadius="l3" p={{ base: 4, md: 6 }}>
      <Stack gap={5}>
        <Stack gap={1}>
          <Heading size="md">Users</Heading>
          <Text color="fg.muted">Local accounts for this deployment — superadmins only.</Text>
        </Stack>

        {error ? <ErrorAlert title="Could not load users" description={error} /> : null}

        {users === null && !error ? (
          <HStack gap={2} color="fg.muted">
            <Spinner size="sm" />
            <Text fontSize="sm">Loading users…</Text>
          </HStack>
        ) : null}

        {users !== null && users.length > 0 ? (
          <Box borderWidth="1px" borderRadius="l3" overflowX="auto">
            <Table.Root size="md" width="100%">
              <Table.Header>
                <Table.Row bg="bg.subtle">
                  <Table.ColumnHeader>Email</Table.ColumnHeader>
                  <Table.ColumnHeader>Role</Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">Actions</Table.ColumnHeader>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {users.map((user) => (
                  <Table.Row key={user.id}>
                    <Table.Cell>
                      {user.email}
                      {user.email.toLowerCase() === selfEmail ? (
                        <Badge colorPalette="teal" variant="subtle" ml={2}>
                          you
                        </Badge>
                      ) : null}
                    </Table.Cell>
                    <Table.Cell>
                      <Badge colorPalette={user.role === 'superadmin' ? 'purple' : 'gray'} variant="subtle">
                        {user.role}
                      </Badge>
                    </Table.Cell>
                    <Table.Cell textAlign="end">
                      <HStack justify="end" gap={2}>
                        <Button
                          variant="outline"
                          size="xs"
                          onClick={() => {
                            setResetFor(user)
                            setResetPassword('')
                            setResetError(null)
                          }}
                        >
                          Reset password
                        </Button>
                        <Button
                          variant="outline"
                          size="xs"
                          colorPalette="red"
                          disabled={user.email.toLowerCase() === selfEmail}
                          title={
                            user.email.toLowerCase() === selfEmail
                              ? 'You cannot delete your own account'
                              : undefined
                          }
                          onClick={() => setPendingDelete(user)}
                        >
                          Delete
                        </Button>
                      </HStack>
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        ) : null}

        {resetFor ? (
          <Box as="form" onSubmit={submitReset} borderWidth="1px" borderRadius="l3" p={4}>
            <Stack gap={3}>
              <Text fontWeight="medium">Reset password for {resetFor.email}</Text>
              <Field.Root maxW="lg">
                <Field.Label>New password</Field.Label>
                <Input
                  type="password"
                  autoComplete="new-password"
                  placeholder="At least 8 characters"
                  value={resetPassword}
                  onChange={(event) => setResetPassword(event.target.value)}
                />
                <Field.HelperText>At least 8 characters.</Field.HelperText>
              </Field.Root>
              <HStack gap={2}>
                <Button type="submit" size="sm" colorPalette="teal" loading={working}>
                  Update password
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setResetFor(null)
                    setResetPassword('')
                    setResetError(null)
                  }}
                >
                  Cancel
                </Button>
              </HStack>
              {resetError ? <ErrorAlert title="Could not update password" description={resetError} /> : null}
            </Stack>
          </Box>
        ) : null}

        <Box as="form" onSubmit={submitCreate}>
          <Stack gap={4}>
            <Stack gap={1}>
              <Heading size="sm">Add a user</Heading>
              <Text color="fg.muted" fontSize="sm">
                New users sign in with email and password. Only superadmins can manage users.
              </Text>
            </Stack>
            <Field.Root maxW="lg">
              <Field.Label>Email</Field.Label>
              <Input
                type="email"
                placeholder="teammate@example.com"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
            </Field.Root>
            <Field.Root maxW="lg">
              <Field.Label>Password</Field.Label>
              <Input
                type="password"
                autoComplete="new-password"
                placeholder="At least 8 characters"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
              <Field.HelperText>At least 8 characters.</Field.HelperText>
            </Field.Root>
            <Field.Root maxW="lg">
              <Field.Label>Role</Field.Label>
              <NativeSelect.Root>
                <NativeSelect.Field value={role} onChange={(event) => setRole(event.target.value)}>
                  <option value="user">user</option>
                  <option value="superadmin">superadmin</option>
                </NativeSelect.Field>
                <NativeSelect.Indicator />
              </NativeSelect.Root>
            </Field.Root>
            <Button type="submit" colorPalette="teal" loading={creating} alignSelf="flex-start">
              Create user
            </Button>
            {createError ? (
              <ErrorAlert title={`Could not create "${email.trim()}"`} description={createError} />
            ) : null}
          </Stack>
        </Box>
      </Stack>

      <ConfirmDialog
        open={pendingDelete !== null}
        title={`Delete "${pendingDelete?.email ?? ''}"?`}
        body="Their sessions stop working immediately. This cannot be undone."
        confirmLabel="Delete"
        loading={working}
        onCancel={() => setPendingDelete(null)}
        onConfirm={confirmDelete}
      />
    </Box>
  )
}
