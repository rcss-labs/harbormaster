import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/geist-mono/400.css'
import '@fontsource/geist-mono/500.css'
import './styles/tokens.css'
import './styles/base.css'

const queryClient = new QueryClient()

// Placeholder root: the router and auth provider arrive with the app shell.
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <h1>Harbormaster</h1>
    </QueryClientProvider>
  </StrictMode>,
)
