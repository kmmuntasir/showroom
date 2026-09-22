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
  Table,
  Text,
} from '@chakra-ui/react'
import { useEffect, useState } from 'react'
import { api } from '../api.js'
import ErrorAlert from '../components/ErrorAlert.jsx'
import { toaster } from '../components/toaster.jsx'
import { humanSize, timeAgoOrDate } from '../format.js'
import { isValidDemoName, NAME_HINT } from '../names.js'
import Users from './Users.jsx'

const liveUrl = (name) => `https://${name}.example.com`

// Live demo list + create form (docs/demos.md §Control dashboard UI). The
// list is API state only — nothing is derived or cached client-side.
// Superadmins (password mode) additionally get the user management panel.
export default function Dashboard({ email, isSuperadmin, demos, error, onOpenDemo, onRefresh, onSignOut }) {
  const [name, setName] = useState('')
  const [validationError, setValidationError] = useState(null)
  const [createError, setCreateError] = useState(null)
  const [creating, setCreating] = useState(false)

  useEffect(() => {
    onRefresh()
  }, [onRefresh])

  const submitCreate = async (event) => {
    event.preventDefault()
    setCreateError(null)

    const trimmed = name.trim()
    if (!isValidDemoName(trimmed)) {
      setValidationError(`"${trimmed || ' '}" is not a valid subdomain name. ${NAME_HINT}`)
      return
    }
    setValidationError(null)
    setCreating(true)
    try {
      await api.post('/api/demos', { name: trimmed })
      toaster.success({
        title: 'Demo created',
        description: `${liveUrl(trimmed)} is ready for its first deploy.`,
      })
      setName('')
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
                      <Button
                        variant="plain"
                        size="sm"
                        colorPalette="teal"
                        px={0}
                        onClick={() => onOpenDemo(demo)}
                      >
                        {demo.name}
                      </Button>
                    </Table.Cell>
                    <Table.Cell>
                      <Link
                        href={liveUrl(demo.name)}
                        target="_blank"
                        rel="noreferrer"
                        colorPalette="teal"
                      >
                        {liveUrl(demo.name)}
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
            <Button type="submit" colorPalette="teal" loading={creating} alignSelf="flex-start">
              Create
            </Button>
            {createError ? (
              <ErrorAlert title={`Could not create "${name.trim()}"`} description={createError} />
            ) : null}
          </Stack>
        </Box>

        {isSuperadmin ? <Users /> : null}
      </Stack>
    </Container>
  )
}
