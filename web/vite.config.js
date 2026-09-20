import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf-8')
)

// https://vite.dev/config/
// backendOrigin resolves where cmd/server is listening. FASTLLM_ADDR is the same
// variable the server itself reads, in Go's listen-address form (":8080",
// "127.0.0.1:8080", or a bare port), so changing the port in one place does not
// leave dev-mode /api requests proxying into a closed socket.
function backendOrigin() {
  const addr = (process.env.FASTLLM_ADDR || ':8080').trim()
  const [host, port] = addr.includes(':')
    ? [addr.slice(0, addr.lastIndexOf(':')), addr.slice(addr.lastIndexOf(':') + 1)]
    : ['', addr]
  // An empty or wildcard host means "all interfaces"; dial loopback for that.
  const target = !host || host === '0.0.0.0' || host === '[::]' ? 'localhost' : host
  return `http://${target}:${port || '8080'}`
}

export default defineConfig(() => ({
  plugins: [react()],
  server: {
    proxy: { '/api': backendOrigin() },
  },
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'react-vendor',
              test: /node_modules[\\/](?:react|react-dom)[\\/]/,
              priority: 30,
            },
            {
              name: 'katex',
              test: /node_modules[\\/]katex[\\/]/,
              priority: 25,
            },
            {
              name: 'highlight',
              test: /node_modules[\\/]highlight\.js[\\/]/,
              priority: 25,
            },
            {
              name: 'markdown',
              test: /node_modules[\\/](?:react-markdown|remark-|rehype-|unified|micromark|mdast|hast|unist|vfile|property-information)/,
              priority: 20,
              maxSize: 300_000,
            },
            {
              name: 'vendor',
              test: /node_modules/,
              priority: 10,
              maxSize: 300_000,
            },
          ],
        },
      },
    },
  },
  define: {
    // Baked in at build time so the running app can show exactly which
    // build it is — critical for a locally-built/self-hosted app where
    // "is this the latest code?" isn't otherwise answerable at a glance.
    __APP_VERSION__: JSON.stringify(pkg.version),
    __BUILD_TIME__: JSON.stringify(new Date().toISOString()),
  },
}))
