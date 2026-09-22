import { Button, CloseButton, Dialog, Portal } from '@chakra-ui/react'

// Destructive/confirm dialog on the v3 Dialog primitive (role=alertdialog) —
// docs/demos.md §Control dashboard UI (AlertDialog for destructive confirms).
export default function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  destructive = true,
  loading = false,
  onConfirm,
  onCancel,
}) {
  return (
    <Dialog.Root
      role="alertdialog"
      open={open}
      onOpenChange={(details) => {
        if (!details.open) onCancel()
      }}
      closeOnEsc={!loading}
      closeOnInteractOutside={!loading}
    >
      <Portal>
        <Dialog.Backdrop />
        <Dialog.Positioner>
          <Dialog.Content>
            <Dialog.Header>
              <Dialog.Title>{title}</Dialog.Title>
            </Dialog.Header>
            <Dialog.Body>{body}</Dialog.Body>
            <Dialog.Footer>
              <Dialog.ActionTrigger asChild>
                <Button variant="outline" disabled={loading} onClick={onCancel}>
                  Cancel
                </Button>
              </Dialog.ActionTrigger>
              <Button
                colorPalette={destructive ? 'red' : 'teal'}
                loading={loading}
                onClick={onConfirm}
              >
                {confirmLabel}
              </Button>
            </Dialog.Footer>
            <Dialog.CloseTrigger asChild>
              <CloseButton size="sm" disabled={loading} />
            </Dialog.CloseTrigger>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  )
}
