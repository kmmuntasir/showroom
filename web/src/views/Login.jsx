import { Button, Field, Heading, Input, Stack, Text } from '@chakra-ui/react'
import { useState } from 'react'
import { api } from '../api.js'
import ErrorAlert from '../components/ErrorAlert.jsx'

// Login renders the deployment's active mechanism (DEMOCTL_AUTH_MODE):
// - google:   server-side Google OAuth — the button only starts the flow.
// - password: local email/password form posting to POST /api/login; success
//   hands the session identity (email, role, CSRF token) to the app.
export default function Login({ bootError, authMode = 'google', baseDomain, onPasswordLogin }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loginError, setLoginError] = useState(null)
  const [signingIn, setSigningIn] = useState(false)

  const submitPasswordLogin = async (event) => {
    event.preventDefault()
    setLoginError(null)
    setSigningIn(true)
    try {
      const data = await api.post('/api/login', {
        email: email.trim(),
        password,
      })
      onPasswordLogin?.({
        email: data.email,
        role: data.role,
        csrfToken: data.csrf_token,
      })
    } catch (loginFailure) {
      setLoginError(loginFailure.message)
    } finally {
      setSigningIn(false)
    }
  }

  return (
    <Stack minH="100vh" align="center" justify="center" gap={6} px={4} textAlign="center">
      <Stack gap={2} align="center" maxW="sm">
        <Heading size="2xl">democtl</Heading>
        {authMode === 'password' ? (
          <Text color="fg.muted">Self-serve demo hosting on {baseDomain || 'example.com'} — sign in with your account.</Text>
        ) : (
          <Text color="fg.muted">
            Self-serve demo hosting on {baseDomain || 'example.com'} — sign in with your company Google account.
          </Text>
        )}
      </Stack>
      {bootError ? (
        <ErrorAlert title="Could not check your session" description={bootError} maxW="sm" />
      ) : null}
      {authMode === 'password' ? (
        <Stack as="form" onSubmit={submitPasswordLogin} gap={4} w="100%" maxW="sm" textAlign="left">
          <Field.Root>
            <Field.Label>Email</Field.Label>
            <Input
              type="email"
              autoComplete="username"
              placeholder="admin@example.com"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
            />
          </Field.Root>
          <Field.Root>
            <Field.Label>Password</Field.Label>
            <Input
              type="password"
              autoComplete="current-password"
              placeholder="••••••••••••"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </Field.Root>
          <Button type="submit" size="lg" colorPalette="teal" loading={signingIn}>
            Sign in
          </Button>
          {loginError ? <ErrorAlert title="Sign in failed" description={loginError} /> : null}
        </Stack>
      ) : (
        <Button
          size="lg"
          colorPalette="teal"
          onClick={() => {
            window.location.href = '/login'
          }}
        >
          Sign in with Google
        </Button>
      )}
    </Stack>
  )
}
