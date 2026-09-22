import { createToaster, Portal, Stack, Toast, Toaster as ChakraToaster } from '@chakra-ui/react'

// Hand-written minimal v3 toaster snippet (D8: Chakra owns the component
// layer). Usage: toaster.success({ title, description }) / toaster.error(...).
export const toaster = createToaster({
  placement: 'bottom-end',
  pauseOnPageNav: true,
  max: 5,
})

export const Toaster = () => (
  <Portal>
    <ChakraToaster toaster={toaster} insetInline={{ mdDown: '4' }}>
      {(toast) => (
        <Toast.Root width={{ md: 'sm' }}>
          <Toast.Indicator />
          <Stack gap="1" flex="1" maxWidth="100%">
            {toast.title ? <Toast.Title>{toast.title}</Toast.Title> : null}
            {toast.description ? <Toast.Description>{toast.description}</Toast.Description> : null}
          </Stack>
          <Toast.CloseTrigger />
        </Toast.Root>
      )}
    </ChakraToaster>
  </Portal>
)
