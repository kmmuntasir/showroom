import { Button, Heading, Stack, Text } from '@chakra-ui/react'
import ErrorAlert from '../components/ErrorAlert.jsx'

// Login is server-side Google OAuth — the button only starts the flow
// (docs/demos.md §Auth). The app never renders a local account form.
export default function Login({ bootError }) {
  return (
    <Stack minH="100vh" align="center" justify="center" gap={6} px={4} textAlign="center">
      <Stack gap={2} align="center" maxW="sm">
        <Heading size="2xl">democtl</Heading>
        <Text color="fg.muted">
          Self-serve demo hosting on example.com — sign in with your company
          Google account.
        </Text>
      </Stack>
      {bootError ? (
        <ErrorAlert title="Could not check your session" description={bootError} maxW="sm" />
      ) : null}
      <Button
        size="lg"
        colorPalette="teal"
        onClick={() => {
          window.location.href = '/login'
        }}
      >
        Sign in with Google
      </Button>
    </Stack>
  )
}
