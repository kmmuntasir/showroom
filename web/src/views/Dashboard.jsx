import {
  Badge,
  Box,
  Button,
  Container,
  EmptyState,
  Field,
  Flex,
  Heading,
  HStack,
  Input,
  Link,
  Spinner,
  Stack,
  Switch,
  Table,
  Text,
} from '@chakra-ui/react'
import { useEffect, useState } from 'react'
import { api } from '../api.js'
import AccessKeyBox from '../components/AccessKeyBox.jsx'
import ChangePasswordDialog from '../components/ChangePasswordDialog.jsx'
import ErrorAlert from '../components/ErrorAlert.jsx'
import { toaster } from '../components/toaster.jsx'
import { humanSize, timeAgoOrDate } from '../format.js'
import { isValidDemoName, NAME_HINT } from '../names.js'
import Users from './Users.jsx'

// liveDemoUrl points at the real deployment: the base domain rides the
// boot probe (/api/me, /api/auth-info) so the dashboard never hardcodes a
// domain. The example.com default only shows before the probe resolves.
const liveDemoUrl = (baseDomain, name) => `https://${name}.${baseDomain || 'example.com'}`

// Live demo list + create form (docs/demos.md §Control dashboard UI). The
// list is API state only — nothing is derived or cached client-side.
// Superadmins (password mode) additionally get the user management panel.
export default function Dashboard({ email, isSuperadmin, authMode, baseDomain, demos, error, onOpenDemo, onRefresh, onSignOut }) {
  const [name, setName] = useState('')
  const [validationError, setValidationError] = useState(null)
  const [createError, setCreateError] = useState(null)
  const [creating, setCreating] = useState(false)
  const [isPrivate, setIsPrivate] = useState(false)
  const [issuedKey, setIssuedKey] = useState(null)
  const [pwOpen, setPwOpen] = useState(false)

  useEffect(() => {
    onRefresh()
  }, [onRefresh])

  const submitCreate = async (event) => {
    event.preventDefault()
    setCreateError(null)
    setIssuedKey(null)

    const trimmed = name.trim()
    if (!isValidDemoName(trimmed)) {
      setValidationError(`"${trimmed || ' '}" is not a valid subdomain name. ${NAME_HINT}`)
      return
    }
    setValidationError(null)
    setCreating(true)
    try {
      const data = await api.post('/api/demos', { name: trimmed, ...(isPrivate ? { private: true } : {}) })
      toaster.success({
        title: 'Demo created',
        description: `${liveDemoUrl(baseDomain, trimmed)} is ready for its first deploy.`,
      })
      // A private demo's access key rides this response exactly once.
      if (data?.access_key) setIssuedKey(data.access_key)
      setName('')
      setIsPrivate(false)
      onRefresh()
    } catch (createFailure) {
      // 409 name taken / reserved — surface inline (docs/demos.md HTTP surface).
      setCreateError(createFailure.message)
    } finally {
      setCreating(false)
    }
  }

  return (
    <Container maxW="6xl" py={{ base: 6, md: 10 }} px={4}>
      <Stack gap={8}>
        <Flex justify="space-between" align="center" gap={4} wrap="wrap">
          <HStack gap={3}>
            <Heading size="xl">Demos</Heading>
            {demos === null ? <Spinner size="sm" color="fg.muted" /> : null}
          </HStack>
          <HStack gap={3}>
            <Text fontSize="sm" color="fg.muted">
              Signed in as {email}
            </Text>
            {authMode === 'password' ? (
              <Button variant="outline" size="sm" onClick={() => setPwOpen(true)}>
                Change password
              </Button>
            ) : null}
            <Button variant="outline" size="sm" onClick={onSignOut}>
              Sign out
            </Button>
          </HStack>
        </Flex>

        {error ? (
          <Stack gap={3}>
            <ErrorAlert title="Could not load demos" description={error} />
            <Button variant="outline" size="sm" alignSelf="flex-start" onClick={onRefresh}>
              Retry
            </Button>
          </Stack>
        ) : null}

        {!error && demos !== null && demos.length === 0 ? (
          <EmptyState.Root borderWidth="1px" borderRadius="l3" py={16}>
            <EmptyState.Content>
              <EmptyState.Description>No demos yet — create your first one below</EmptyState.Description>
            </EmptyState.Content>
          </EmptyState.Root>
        ) : null}

        {demos !== null && demos.length > 0 ? (
          <Box borderWidth="1px" borderRadius="l3" overflowX="auto">
            <Table.Root size="md" width="100%">
              <Table.Header>
                <Table.Row bg="bg.subtle">
                  <Table.ColumnHeader>Name</Table.ColumnHeader>
                  <Table.ColumnHeader>Created by</Table.ColumnHeader>
                  <Table.ColumnHeader>Live URL</Table.ColumnHeader>
                  <Table.ColumnHeader>Last deploy</Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">Size</Table.ColumnHeader>
                  <Table.ColumnHeader textAlign="end">Files</Table.ColumnHeader>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {demos.map((demo) => (
                  <Table.Row key={demo.name}>
                    <Table.Cell>
                      <HStack gap={2}>
                        <Button
                          variant="plain"
                          size="sm"
                          colorPalette="teal"
                          px={0}
                          onClick={() => onOpenDemo(demo)}
                        >
                          {demo.name}
                        </Button>
                        {demo.private ? (
                          <Badge colorPalette="orange" variant="subtle">
                            Private
                          </Badge>
                        ) : null}
                      </HStack>
                    </Table.Cell>
                    <Table.Cell>{demo.created_by}</Table.Cell>
                    <Table.Cell>
                      <Link
                        href={liveDemoUrl(baseDomain, demo.name)}
                        target="_blank"
                        rel="noreferrer"
                        colorPalette="teal"
                      >
                        {liveDemoUrl(baseDomain, demo.name)}
                      </Link>
                    </Table.Cell>
                    <Table.Cell>
                      {demo.last_release ? (
                        <Stack gap={0}>
                          <Text>{timeAgoOrDate(demo.last_release.uploaded_at)}</Text>
                          <Text fontSize="xs" color="fg.muted">
                            by {demo.last_release.uploaded_by}
                          </Text>
                        </Stack>
                      ) : (
                        <Badge colorPalette="gray" variant="subtle">
                          Never deployed
                        </Badge>
                      )}
                    </Table.Cell>
                    <Table.Cell textAlign="end">
                      {demo.last_release ? humanSize(demo.last_release.size_bytes) : '—'}
                    </Table.Cell>
                    <Table.Cell textAlign="end">
                      {demo.last_release ? demo.last_release.file_count : '—'}
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table.Root>
          </Box>
        ) : null}

        <Box as="form" onSubmit={submitCreate} borderWidth="1px" borderRadius="l3" p={{ base: 4, md: 6 }}>
          <Stack gap={4}>
            <Stack gap={1}>
              <Heading size="md">Add a demo</Heading>
              <Text color="fg.muted">
                Pick a subdomain label — the zip upload happens on the demo's own page.
              </Text>
            </Stack>
            <Field.Root invalid={Boolean(validationError)} maxW="lg">
              <Field.Label>Subdomain name</Field.Label>
              <Input
                placeholder="acme"
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
              <Field.HelperText>{NAME_HINT}</Field.HelperText>
              {validationError ? <Field.ErrorText>{validationError}</Field.ErrorText> : null}
            </Field.Root>
            <Field.Root maxW="lg">
              <Switch.Root
                checked={isPrivate}
                onCheckedChange={(details) => setIsPrivate(details.checked)}
              >
                <Switch.HiddenInput />
                <Switch.Control>
                  <Switch.Thumb />
                </Switch.Control>
                <Switch.Label>Private — visitors need an access key</Switch.Label>
              </Switch.Root>
              <Field.HelperText>
                A private demo shows an access page instead of the site. The key is generated when
                you create the demo and shown exactly once.
              </Field.HelperText>
            </Field.Root>
            <Button type="submit" colorPalette="teal" loading={creating} alignSelf="flex-start">
              Create
            </Button>
            {issuedKey ? <AccessKeyBox accessKey={issuedKey} /> : null}
            {createError ? (
              <ErrorAlert title={`Could not create "${name.trim()}"`} description={createError} />
            ) : null}
          </Stack>
        </Box>

        {isSuperadmin && authMode === 'password' ? <Users /> : null}
      </Stack>

      {authMode === 'password' ? <ChangePasswordDialog open={pwOpen} onClose={() => setPwOpen(false)} /> : null}
    </Container>
  )
}
