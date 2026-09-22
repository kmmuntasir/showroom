import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// Dev proxy points at democtl (LAN :5000)
// service). The auth routes are backend-served too, so the OAuth flow works
// from the dev server.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:5000',
      '/login': 'http://127.0.0.1:5000',
      '/logout': 'http://127.0.0.1:5000',
      '/oauth2': 'http://127.0.0.1:5000',
    },
  },
})
