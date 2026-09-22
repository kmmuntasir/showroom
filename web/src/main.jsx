import { ChakraProvider, defaultSystem } from '@chakra-ui/react'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.jsx'
import { Toaster } from './components/toaster.jsx'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <ChakraProvider value={defaultSystem}>
      <App />
      <Toaster />
    </ChakraProvider>
  </StrictMode>,
)
