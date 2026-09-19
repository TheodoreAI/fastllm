import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'

const pkg = JSON.parse(
  readFileSync(fileURLToPath(new URL('./package.json', import.meta.url)), 'utf-8')
)

// Wails' external-asset-handler (see cmd/desktop/main.go's Assets/Handler
// comment) only falls back from the Vite dev server to the app's real Go
// backend on a genuine 404/405 response. Without this, Vite's own SPA
// fallback (connect-history-api-fallback) serves index.html — 200 OK,
// HTML — for any unmatched /api/* GET request, and the frontend's
// res.json() calls fail with a cryptic SyntaxError instead of ever
// reaching the real backend. Short-circuiting /api/* to a bare 404 here,
// before Vite's own middleware chain gets to it, is what makes that
// fallback actually trigger.
function desktopApiFallthrough() {
  return {
    name: 'desktop-api-fallthrough',
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (req.url && req.url.startsWith('/api/')) {
          res.statusCode = 404
          res.end()
          return
        }
        next()
      })
    },
  }
}

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

export default defineConfig(({ mode }) => ({
  plugins: [react(), ...(mode === 'desktop' ? [desktopApiFallthrough()] : [])],
  server: {
    // In desktop dev mode (see cmd/desktop/wails.json's frontend:dev:watcher
    // passing --mode desktop), the app is served through Wails' own dev
    // bridge, not cmd/server's :8080 — nothing listens there, so proxying
    // /api to it here would 502 every request before Wails' own
    // AssetServer fallback (Assets 404 -> Handler, see cmd/desktop/main.go)
    // ever got a chance to serve it from the real in-process backend. Vite
    // returning its own 404 for /api/* instead lets that fallback work.
    proxy: mode === 'desktop' ? {} : { '/api': backendOrigin() },
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
