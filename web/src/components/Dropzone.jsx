import { Box, Text } from '@chakra-ui/react'
import { useRef, useState } from 'react'

// Zip drop target: click-to-browse + drag-drop, keyboard activatable
// (role=button, docs/demos.md §Control dashboard UI).
export default function Dropzone({ onFile, disabled = false }) {
  const inputRef = useRef(null)
  const [dragOver, setDragOver] = useState(false)

  const activate = () => {
    if (!disabled) inputRef.current?.click()
  }

  const onKeyDown = (event) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      activate()
    }
  }

  const onDrop = (event) => {
    event.preventDefault()
    setDragOver(false)
    if (disabled) return
    const file = event.dataTransfer.files?.[0]
    if (file) onFile(file)
  }

  const onInputChange = (event) => {
    const file = event.target.files?.[0]
    if (file) onFile(file)
    event.target.value = ''
  }

  return (
    <Box
      role="button"
      tabIndex={disabled ? -1 : 0}
      aria-label="Upload a .zip of your built dist folder"
      aria-disabled={disabled}
      onClick={activate}
      onKeyDown={onKeyDown}
      onDragOver={(event) => {
        event.preventDefault()
        if (!disabled) setDragOver(true)
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={onDrop}
      borderWidth="2px"
      borderStyle="dashed"
      borderRadius="l2"
      p={{ base: 6, md: 10 }}
      textAlign="center"
      bg={dragOver ? 'teal.subtle' : 'transparent'}
      cursor={disabled ? 'not-allowed' : 'pointer'}
      _focusVisible={{ outline: '2px solid', outlineColor: 'teal.solid' }}
    >
      <input ref={inputRef} type="file" accept=".zip,application/zip,application/x-zip-compressed" hidden onChange={onInputChange} />
      <Text fontWeight="medium">Drop a .zip of your built dist/ folder here</Text>
      <Text fontSize="sm" color="fg.muted" mt={1}>
        or click to browse — index.html must be at the zip root
      </Text>
    </Box>
  )
}
