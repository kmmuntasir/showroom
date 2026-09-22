import { Button, HStack, Input, Stack, Text } from '@chakra-ui/react'
import { useState } from 'react'
import { toaster } from './toaster.jsx'

// AccessKeyBox shows a freshly generated access key exactly once — the
// server stores only a hash and cannot show it again. Copy-to-clipboard
// plus an explicit warning; the caller renders it only while the key from
// the enabling/rotating response is still relevant.
export default function AccessKeyBox({ accessKey }) {
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(accessKey)
      setCopied(true)
    } catch {
      toaster.error({ title: 'Could not copy', description: 'Select the key and copy it manually.' })
    }
  }

  return (
    <Stack gap={2} borderWidth="1px" borderRadius="l3" p={4}>
      <Text fontWeight="medium">Access key — copy it now</Text>
      <Text fontSize="sm" color="fg.muted">
        This key is shown only once. The server stores a hash and cannot recover it — share it with
        anyone who needs to view the demo.
      </Text>
      <HStack gap={3}>
        <Input readOnly value={accessKey} fontFamily="mono" />
        <Button variant="outline" onClick={copy} flexShrink={0}>
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </HStack>
    </Stack>
  )
}
