import { Alert } from '@chakra-ui/react'

// Inline view-level error surface — always rendered from the message carried
// by a thrown api.js error (docs/demos.md error envelope).
export default function ErrorAlert({ title, description, maxW, mt }) {
  return (
    <Alert.Root status="error" maxW={maxW} mt={mt} alignItems="flex-start">
      <Alert.Indicator />
      <Alert.Content>
        {title ? <Alert.Title>{title}</Alert.Title> : null}
        <Alert.Description>{description}</Alert.Description>
      </Alert.Content>
    </Alert.Root>
  )
}
