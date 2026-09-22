import { Button, CloseButton, Dialog, Field, Input, Portal, Stack } from '@chakra-ui/react'
import { useState } from 'react'
import { api } from '../api.js'
import ErrorAlert from './ErrorAlert.jsx'
import { toaster } from './toaster.jsx'

// ChangePasswordDialog is the self-service password change (password auth
// mode): the current password authorizes the change, and every other
// session of the user is revoked server-side — only this one stays alive.
export default function ChangePasswordDialog({ open, onClose }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [error, setError] = useState(null)
  const [saving, setSaving] = useState(false)

  const submit = async (event) => {
    event.preventDefault()
    setError(null)
    setSaving(true)
    try {
      await api.post('/api/me/password', { current_password: current, new_password: next })
      toaster.success({
        title: 'Password changed',
        description: 'Your other sessions were signed out.',
      })
      setCurrent('')
      setNext('')
      onClose()
    } catch (changeFailure) {
      setError(changeFailure.message)
    } finally {
      setSaving(false)
    }
  }

  const cancel = () => {
    setError(null)
    setCurrent('')
    setNext('')
    onClose()
  }

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(details) => {
        if (!details.open) cancel()
      }}
      closeOnEsc={!saving}
      closeOnInteractOutside={!saving}
    >
      <Portal>
        <Dialog.Backdrop />
        <Dialog.Positioner>
          <Dialog.Content as="form" onSubmit={submit}>
            <Dialog.Header>
              <Dialog.Title>Change your password</Dialog.Title>
            </Dialog.Header>
            <Dialog.Body>
              <Stack gap={4}>
                <Field.Root required>
                  <Field.Label>
                    Current password <Field.RequiredIndicator />
                  </Field.Label>
                  <Input
                    type="password"
                    autoComplete="current-password"
                    value={current}
                    onChange={(event) => setCurrent(event.target.value)}
                  />
                </Field.Root>
                <Field.Root required>
                  <Field.Label>
                    New password <Field.RequiredIndicator />
                  </Field.Label>
                  <Input
                    type="password"
                    autoComplete="new-password"
                    placeholder="At least 8 characters"
                    value={next}
                    onChange={(event) => setNext(event.target.value)}
                  />
                  <Field.HelperText>
                    Signing you out everywhere except this browser.
                  </Field.HelperText>
                </Field.Root>
                {error ? <ErrorAlert title="Could not change password" description={error} /> : null}
              </Stack>
            </Dialog.Body>
            <Dialog.Footer>
              <Dialog.ActionTrigger asChild>
                <Button variant="outline" disabled={saving} onClick={cancel}>
                  Cancel
                </Button>
              </Dialog.ActionTrigger>
              <Button
                type="submit"
                colorPalette="teal"
                loading={saving}
                disabled={!current || !next}
              >
                Change password
              </Button>
            </Dialog.Footer>
            <Dialog.CloseTrigger asChild>
              <CloseButton size="sm" disabled={saving} />
            </Dialog.CloseTrigger>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  )
}
