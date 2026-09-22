import { Spinner, Stack, Text } from '@chakra-ui/react'
import { useCallback, useEffect, useState } from 'react'
import { api, setCsrfToken } from './api.js'
import Dashboard from './views/Dashboard.jsx'
import DemoDetail from './views/DemoDetail.jsx'
import Login from './views/Login.jsx'

// Two views switched on App state — no router (docs/demos.md §Control
// dashboard UI). view: null → dashboard; { name, demo } → demo detail, with
// the demo object refreshed from the list once it loads.
//
// The login mechanism is deployment-wide (DEMOCTL_AUTH_MODE): the boot
// probe learns it from GET /api/me when authenticated, or from the public
// GET /api/auth-info when logged out, and the Login view renders the
// matching form.
export default function App() {
  const [auth, setAuth] = useState({ status: 'loading', email: null, role: null, isSuperadmin: false, authMode: 'google' })
  const [bootError, setBootError] = useState(null)
  const [demos, setDemos] = useState(null)
  const [demosError, setDemosError] = useState(null)
  const [view, setView] = useState(null)

  const loadDemos = useCallback(async () => {
    try {
      const data = await api.get('/api/demos')
      setDemos(data?.demos ?? [])
      setDemosError(null)
    } catch (listFailure) {
      setDemosError(listFailure.message)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    const boot = async () => {
      try {
        const me = await api.get('/api/me')
        if (cancelled) return
        setCsrfToken(me.csrf_token)
        setAuth({
          status: 'ready',
          email: me.email,
          role: me.role ?? null,
          isSuperadmin: me.is_superadmin === true,
          authMode: me.auth_mode ?? 'google',
        })
      } catch (bootFailure) {
        if (cancelled) return
        // Logged out: learn the active auth mode so the login view renders
        // the right form. A failed probe keeps the google default.
        let authMode = 'google'
        try {
          const info = await api.get('/api/auth-info')
          if (info?.auth_mode === 'password') authMode = 'password'
        } catch {
          // /api/auth-info unreachable — the login attempt will surface it.
        }
        if (cancelled) return
        setAuth({ status: 'login', email: null, role: null, isSuperadmin: false, authMode })
        if (bootFailure.status !== 401) setBootError(bootFailure.message)
      }
    }
    boot()
    return () => {
      cancelled = true
    }
  }, [])

  // Password-mode sign-in: the login endpoint returns the session identity
  // plus the CSRF token (the session cookie arrives as Set-Cookie).
  const signInWithPassword = ({ email, role, csrfToken }) => {
    setCsrfToken(csrfToken)
    setAuth({
      status: 'ready',
      email,
      role: role ?? null,
      isSuperadmin: role === 'superadmin',
      authMode: 'password',
    })
    setBootError(null)
  }

  const signOut = async () => {
    try {
      await api.post('/logout')
    } catch {
      // Session already gone — the login view is correct either way.
    }
    setCsrfToken(null)
    setAuth((prev) => ({ status: 'login', email: null, role: null, isSuperadmin: false, authMode: prev.authMode }))
    setDemos(null)
    setView(null)
  }

  if (auth.status === 'loading') {
    return (
      <Stack minH="100vh" align="center" justify="center" gap={4} px={4}>
        <Spinner size="lg" colorPalette="teal" />
        <Text color="fg.muted">Checking your session…</Text>
      </Stack>
    )
  }

  if (auth.status === 'login') {
    return <Login bootError={bootError} authMode={auth.authMode} onPasswordLogin={signInWithPassword} />
  }

  const viewedDemo = view
    ? (demos ?? []).find((demo) => demo.name === view.name) ?? view.demo
    : null

  if (viewedDemo) {
    return (
      <DemoDetail
        demo={viewedDemo}
        email={auth.email}
        isSuperadmin={auth.isSuperadmin}
        refreshing={demos === null}
        onBack={() => setView(null)}
        onRenamed={(renamedTo) => setView({ name: renamedTo, demo: { ...viewedDemo, name: renamedTo } })}
        refresh={loadDemos}
      />
    )
  }

  return (
    <Dashboard
      email={auth.email}
      isSuperadmin={auth.isSuperadmin}
      authMode={auth.authMode}
      demos={demos}
      error={demosError}
      onOpenDemo={(demo) => setView({ name: demo.name, demo })}
      onRefresh={loadDemos}
      onSignOut={signOut}
    />
  )
}
