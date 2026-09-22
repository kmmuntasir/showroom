import {
  Badge,
  Box,
  Button,
  Container,
  Field,
  Heading,
  HStack,
  Input,
  Link,
  Progress,
  Spinner,
  Stack,
  Text,
} from '@chakra-ui/react'
import { useState } from 'react'
import { api, uploadZip } from '../api.js'
import AccessKeyBox from '../components/AccessKeyBox.jsx'
import ConfirmDialog from '../components/ConfirmDialog.jsx'
import Dropzone from '../components/Dropzone.jsx'
import ErrorAlert from '../components/ErrorAlert.jsx'
import { toaster } from '../components/toaster.jsx'
import { humanSize, timeAgoOrDate } from '../format.js'
import { isValidDemoName, NAME_HINT } from '../names.js'

// liveDemoUrl points at the real deployment: the base domain rides the
// boot probe (/api/me) so demo links never hardcode a domain. The
// example.com default only shows before the probe resolves.
const liveDemoUrl = (baseDomain, name) => `https://${name}.${baseDomain || 'example.com'}`

// keep-2 on the server; the list payload exposes only the last release, so
// roll back is enabled once two releases are known — via an optional
// release_count if the API provides one, else a present last_release.
const releaseCountOf = (demo) => demo.release_count ?? (demo.last_release ? 1 : 0)

// Demo detail: deploy (zip dropzone), rollback, rename, privacy, delete —
// deploy and settings render only for the demo's owner or a superadmin
// (docs/demos.md §Control dashboard UI + §Upload pipeline).
export default function DemoDetail({ demo, email, isSuperadmin, baseDomain, refreshing, onBack, onRenamed, refresh }) {
  const name = demo.name
  const canManage = isSuperadmin || email === demo.created_by
  const liveUrl = (n) => liveDemoUrl(baseDomain, n)

  // Deploy
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState(0)
  const [processing, setProcessing] = useState(false)
  const [uploadError, setUploadError] = useState(null)

  const deploy = async (file) => {
    setUploadError(null)
    if (!file.name.toLowerCase().endsWith('.zip')) {
      setUploadError('Please choose a .zip file of the built dist/ folder.')
      return
    }
    setUploading(true)
    setProgress(0)
    setProcessing(false)
    try {
      const data = await uploadZip(name, file, (pct) => {
        setProgress(pct)
        if (pct >= 100) setProcessing(true)
      })
      toaster.success({
        title: 'Deploy is live',
        description: `${data.release.file_count} files · ${humanSize(data.release.size_bytes)} → ${liveUrl(name)}`,
      })
      await refresh()
    } catch (deployFailure) {
      setUploadError(deployFailure.message)
    } finally {
      setUploading(false)
      setProcessing(false)
      setProgress(0)
    }
  }

  // Rollback
  const [rollingBack, setRollingBack] = useState(false)
  const canRollback = releaseCountOf(demo) >= 2

  const rollback = async () => {
    setRollingBack(true)
    try {
      await api.post(`/api/demos/${encodeURIComponent(name)}/rollback`)
      toaster.success({
        title: 'Rolled back',
        description: `The previous release is live again on ${liveUrl(name)}.`,
      })
      await refresh()
    } catch (rollbackFailure) {
      toaster.error({ title: 'Rollback failed', description: rollbackFailure.message })
    } finally {
      setRollingBack(false)
    }
  }

  // Rename
  const [newName, setNewName] = useState('')
  const [renameError, setRenameError] = useState(null)
  const [renaming, setRenaming] = useState(false)
  const [renameOpen, setRenameOpen] = useState(false)

  const submitRenameIntent = (event) => {
    event.preventDefault()
    setRenameError(null)

    const target = newName.trim()
    if (target === name) {
      setRenameError('That is already this demo\'s name.')
      return
    }
    if (!isValidDemoName(target)) {
      setRenameError(`"${target || ' '}" is not a valid subdomain name. ${NAME_HINT}`)
      return
    }
    setRenameOpen(true)
  }

  const confirmRename = async () => {
    const target = newName.trim()
    setRenaming(true)
    try {
      const data = await api.patch(`/api/demos/${encodeURIComponent(name)}`, { name: target })
      toaster.success({
        title: 'Demo renamed',
        description: `Now live on ${liveUrl(data.demo.name)} — ${liveUrl(name)} stops working.`,
      })
      setRenameOpen(false)
      setNewName('')
      onRenamed(data.demo.name)
      await refresh()
    } catch (renameFailure) {
      setRenameError(renameFailure.message)
    } finally {
      setRenaming(false)
    }
  }

  // Privacy — enabling or rotating mints a server-generated key that is
  // shown exactly once (AccessKeyBox); disabling forgets the key.
  const [issuedKey, setIssuedKey] = useState(null)
  const [privacyError, setPrivacyError] = useState(null)
  const [privacyBusy, setPrivacyBusy] = useState(false)
  const [rotateOpen, setRotateOpen] = useState(false)
  const [disableOpen, setDisableOpen] = useState(false)

  const applyPrivacy = async (body, successTitle) => {
    setPrivacyError(null)
    setPrivacyBusy(true)
    try {
      const data = await api.patch(`/api/demos/${encodeURIComponent(name)}`, body)
      setIssuedKey(data?.access_key ?? null)
      toaster.success({ title: successTitle })
      await refresh()
    } catch (privacyFailure) {
      setPrivacyError(privacyFailure.message)
    } finally {
      setPrivacyBusy(false)
    }
  }

  // Delete
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const confirmDelete = async () => {
    setDeleting(true)
    try {
      await api.del(`/api/demos/${encodeURIComponent(name)}`)
      toaster.success({ title: `Deleted "${name}"`, description: 'The site and all releases are gone.' })
      setDeleteOpen(false)
      onBack()
    } catch (deleteFailure) {
      toaster.error({ title: 'Delete failed', description: deleteFailure.message })
      setDeleting(false)
    }
  }

  return (
    <Container maxW="4xl" py={{ base: 6, md: 10 }} px={4}>
      <Stack gap={6}>
        <Button variant="plain" size="sm" alignSelf="flex-start" px={0} onClick={onBack}>
          ← All demos
        </Button>

        <Stack gap={1}>
          <HStack gap={3}>
            <Heading size="xl">{name}</Heading>
            {demo.private ? (
              <Badge colorPalette="orange" variant="subtle">
                Private
              </Badge>
            ) : (
              <Badge colorPalette="gray" variant="subtle">
                Public
              </Badge>
            )}
            {refreshing ? <Spinner size="xs" color="fg.muted" /> : null}
          </HStack>
          <Link href={liveUrl(name)} target="_blank" rel="noreferrer" colorPalette="teal">
            {liveUrl(name)}
          </Link>
          <Text color="fg.muted">
            Created by {demo.created_by}
            {demo.last_release
              ? ` · Last deploy ${timeAgoOrDate(demo.last_release.uploaded_at)} by ` +
                `${demo.last_release.uploaded_by} · ${humanSize(demo.last_release.size_bytes)} · ` +
                `${demo.last_release.file_count} files`
              : ' · Never deployed'}
          </Text>
        </Stack>

        {!canManage ? (
          <Text color="fg.muted" borderWidth="1px" borderRadius="l3" p={4}>
            Only {demo.created_by} or a superadmin can manage this demo — deploying, renaming,
            privacy, and deletion are limited to them.
          </Text>
        ) : (
          <>
            <Stack gap={3} borderWidth="1px" borderRadius="l3" p={{ base: 4, md: 6 }}>
              <Stack gap={1}>
                <Heading size="md">Deploy</Heading>
                <Text color="fg.muted">
                  A new release goes live instantly; the previous one is kept for rollback.
                </Text>
              </Stack>
              <Dropzone onFile={deploy} disabled={uploading} />
              {uploading ? (
                <Stack gap={1}>
                  <Progress.Root value={processing ? null : progress} size="sm" colorPalette="teal">
                    <Progress.Track>
                      <Progress.Range />
                    </Progress.Track>
                  </Progress.Root>
                  <Text fontSize="sm" color="fg.muted">
                    {processing ? 'Upload complete — processing on the server…' : `Uploading… ${progress}%`}
                  </Text>
                </Stack>
              ) : null}
              <HStack gap={3}>
                <Button
                  variant="outline"
                  onClick={rollback}
                  loading={rollingBack}
                  disabled={!canRollback || uploading}
                >
                  Rollback
                </Button>
                <Text fontSize="sm" color="fg.muted">
                  {canRollback
                    ? 'Go back to the previous release.'
                    : 'Rollback is available once there are at least two releases.'}
                </Text>
              </HStack>
              {uploadError ? <ErrorAlert title="Deploy failed" description={uploadError} /> : null}
            </Stack>

            <Stack gap={4} borderWidth="1px" borderRadius="l3" p={{ base: 4, md: 6 }}>
              <Stack gap={1}>
                <Heading size="md">Settings</Heading>
                <Text color="fg.muted">Rename, privacy, and deletion.</Text>
              </Stack>

              <Stack gap={3} borderWidth="1px" borderRadius="l3" p={4}>
                <Stack gap={1}>
                  <Heading size="sm">Privacy</Heading>
                  <Text color="fg.muted" fontSize="sm">
                    {demo.private
                      ? 'Visitors must enter the access key before the site loads.'
                      : 'Anyone with the link can view this demo.'}
                  </Text>
                </Stack>
                <HStack gap={3} wrap="wrap">
                  {!demo.private ? (
                    <Button
                      size="sm"
                      colorPalette="teal"
                      loading={privacyBusy}
                      onClick={() => applyPrivacy({ private: true }, 'Demo is now private')}
                    >
                      Require access key
                    </Button>
                  ) : (
                    <>
                      <Button size="sm" variant="outline" loading={privacyBusy} onClick={() => setRotateOpen(true)}>
                        Rotate key
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        loading={privacyBusy}
                        onClick={() => setDisableOpen(true)}
                      >
                        Make public
                      </Button>
                    </>
                  )}
                </HStack>
                {issuedKey ? <AccessKeyBox accessKey={issuedKey} /> : null}
                {privacyError ? (
                  <ErrorAlert title="Could not change privacy" description={privacyError} />
                ) : null}
              </Stack>

              <Box as="form" onSubmit={submitRenameIntent}>
                <Field.Root invalid={Boolean(renameError)} maxW="lg">
                  <Field.Label>Subdomain name</Field.Label>
                  <HStack gap={3} align="flex-start" flexDir={{ base: 'column', md: 'row' }} width="full">
                    <Input
                      flex="1"
                      placeholder={name}
                      value={newName}
                      onChange={(event) => setNewName(event.target.value)}
                    />
                    <Button type="submit" variant="outline" loading={renaming} disabled={!newName.trim()}>
                      Rename
                    </Button>
                  </HStack>
                  <Field.HelperText>
                    {NAME_HINT} Renaming moves the site to a new URL — the old subdomain stops working.
                  </Field.HelperText>
                  {renameError ? <Field.ErrorText>{renameError}</Field.ErrorText> : null}
                </Field.Root>
              </Box>

              <Stack gap={1} borderTopWidth="1px" pt={4}>
                <Heading size="sm">Delete this demo</Heading>
                <HStack gap={3}>
                  <Button colorPalette="red" variant="outline" onClick={() => setDeleteOpen(true)}>
                    Delete demo
                  </Button>
                  <Text fontSize="sm" color="fg.muted">
                    Removes the site and every release for everyone.
                  </Text>
                </HStack>
              </Stack>
            </Stack>
          </>
        )}
      </Stack>

      <ConfirmDialog
        open={renameOpen}
        title={`Rename demo "${name}"?`}
        body={`The site moves to ${liveUrl(newName.trim())} and the old subdomain ${liveUrl(name)} stops working.`}
        confirmLabel="Rename"
        destructive={false}
        loading={renaming}
        onConfirm={confirmRename}
        onCancel={() => setRenameOpen(false)}
      />
      <ConfirmDialog
        open={rotateOpen}
        title={`Rotate the access key for "${name}"?`}
        body="Everyone currently unlocked stops seeing the demo until you share the new key with them. The new key is shown exactly once."
        confirmLabel="Rotate key"
        destructive={false}
        loading={privacyBusy}
        onConfirm={() => {
          setRotateOpen(false)
          applyPrivacy({ rotate_key: true }, 'Access key rotated')
        }}
        onCancel={() => setRotateOpen(false)}
      />
      <ConfirmDialog
        open={disableOpen}
        title={`Make "${name}" public?`}
        body="Anyone with the link will be able to view this demo — no key needed. The access key is forgotten."
        confirmLabel="Make public"
        destructive={false}
        loading={privacyBusy}
        onConfirm={() => {
          setDisableOpen(false)
          applyPrivacy({ private: false }, 'Demo is now public')
        }}
        onCancel={() => setDisableOpen(false)}
      />
      <ConfirmDialog
        open={deleteOpen}
        title={`Delete demo "${name}"?`}
        body="The site and all releases are removed for everyone. This cannot be undone."
        confirmLabel="Delete"
        loading={deleting}
        onConfirm={confirmDelete}
        onCancel={() => {
          setDeleteOpen(false)
          setDeleting(false)
        }}
      />
    </Container>
  )
}
