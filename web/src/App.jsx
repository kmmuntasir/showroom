import { Spinner, Stack, Text } from '@chakra-ui/react'
import { useCallback, useEffect, useState } from 'react'
import { api, setCsrfToken } from './api.js'
import Dashboard from './views/Dashboard.jsx'
import DemoDetail from './views/DemoDetail.jsx'
import Login from './views/Login.jsx'

// Two views switched on App state — no router (docs/demos.md §Control
// dashboard UI). view: null → dashboard; { name, demo } → demo detail, with
// the demo object refreshed from the list once it loads.
export default function App() {
  const [auth, setAuth] = useState({ status: 'loading', email: null })
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
        setAuth({ status: 'ready', email: me.email })
      } catch (bootFailure) {
        if (cancelled) return
        setAuth({ status: 'login', email: null })
        if (bootFailure.status !== 401) setBootError(bootFailure.message)
      }
    }
    boot()
    return () => {
      cancelled = true
    }
  }, [])

  const signOut = async () => {
    try {
      await api.post('/logout')
    } catch {
      // Session already gone — the login view is correct either way.
    }
    setCsrfToken(null)
    setAuth({ status: 'login', email: null })
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
    return <Login bootError={bootError} />
  }

  const viewedDemo = view
    ? (demos ?? []).find((demo) => demo.name === view.name) ?? view.demo
    : null

  if (viewedDemo) {
    return (
      <DemoDetail
        demo={viewedDemo}
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
      demos={demos}
      error={demosError}
      onOpenDemo={(demo) => setView({ name: demo.name, demo })}
      onRefresh={loadDemos}
      onSignOut={signOut}
    />
  )
}
