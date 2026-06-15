import fs from 'node:fs/promises'
import path from 'node:path'
import { transform as esbuildTransform } from 'esbuild'
import { normalizeImportMapExternal } from './import-map.js'

const modulePrefix = '/@creght/module'
const assetPrefix = '/@creght/asset'
const runtimePrefix = '/@creght/runtime'
const pageExts = ['.tsx', '.ts', '.jsx', '.js']
const sourceExts = new Set([...pageExts, '.css'])

const fallbackImportMap = {
  react: 'https://esm.talizen.com/react@19.2.4?dev',
  'react/': 'https://esm.talizen.com/react@19.2.4&dev/',
  'react-dom': 'https://esm.talizen.com/react-dom@19.2.4?dev',
  'react-dom/': 'https://esm.talizen.com/react-dom@19.2.4&dev/',
  'react-dom/client': 'https://esm.talizen.com/react-dom@19.2.4/client?dev',
  'class-variance-authority': 'https://esm.talizen.com/class-variance-authority@0.7.1',
  clsx: 'https://esm.talizen.com/clsx@2.1.1',
  'tailwind-merge': 'https://esm.talizen.com/tailwind-merge@3.5.0',
  '@radix-ui/react-slot': 'https://esm.talizen.com/@radix-ui/react-slot@1.2.4',
  'lucide-react': 'https://esm.talizen.com/lucide-react@0.577.0?dev&external=react,react-dom',
  motion: 'https://esm.talizen.com/motion@12.38.0?dev',
  'motion/react': 'https://esm.talizen.com/motion@12.38.0/react?dev&external=react,react-dom',
  'framer-motion': 'https://esm.talizen.com/framer-motion@12.38.0?dev&external=react,react-dom',
  three: 'https://esm.talizen.com/three@0.167.1',
  'three/': 'https://esm.talizen.com/three@0.167.1/',
  '@react-three/fiber': 'https://esm.talizen.com/@react-three/fiber@9.3.0?external=react,react-dom,three',
  '@react-three/drei': 'https://esm.talizen.com/@react-three/drei@10.7.4?external=react,react-dom,three,@react-three',
  talizen: 'https://esm.talizen.com/talizen@0.1.4',
  'talizen/': 'https://esm.talizen.com/talizen@0.1.4/',
}

const escapeHtml = (s) => String(s)
  .replaceAll('&', '&amp;')
  .replaceAll('<', '&lt;')
  .replaceAll('>', '&gt;')
  .replaceAll('"', '&quot;')

const jsonScript = (value) => JSON.stringify(value).replaceAll('<', '\\u003c')

const transformWithEsbuild = (source, filename, options) =>
  esbuildTransform(source, { ...options, sourcefile: filename })

const isRelativeSpecifier = (specifier) =>
  specifier.startsWith('./') || specifier.startsWith('../') || specifier.startsWith('/')

const contentTypeForPath = (file) => {
  switch (path.extname(file).toLowerCase()) {
    case '.css':
      return 'text/css; charset=utf-8'
    case '.js':
    case '.mjs':
      return 'text/javascript; charset=utf-8'
    case '.json':
      return 'application/json; charset=utf-8'
    case '.svg':
      return 'image/svg+xml'
    case '.png':
      return 'image/png'
    case '.jpg':
    case '.jpeg':
      return 'image/jpeg'
    case '.gif':
      return 'image/gif'
    case '.webp':
      return 'image/webp'
    case '.woff':
      return 'font/woff'
    case '.woff2':
      return 'font/woff2'
    default:
      return 'application/octet-stream'
  }
}

const normalizeProjectPath = (value) => {
  const out = path.posix.normalize('/' + value.replaceAll(path.sep, '/').replace(/^\/+/, ''))
  if (out.startsWith('/../')) {
    throw new Error(`unsafe Creght path: ${value}`)
  }
  return out
}

async function exists(file) {
  try {
    await fs.access(file)
    return true
  } catch {
    return false
  }
}

async function resolveFile(projectRoot, projectPath) {
  const clean = normalizeProjectPath(projectPath)
  const abs = path.join(projectRoot, clean.slice(1))
  if (await exists(abs)) return { projectPath: clean, abs }

  for (const ext of pageExts) {
    if (await exists(abs + ext)) {
      return { projectPath: clean + ext, abs: abs + ext }
    }
  }

  for (const ext of pageExts) {
    const indexPath = path.join(abs, 'Index' + ext)
    if (await exists(indexPath)) {
      return { projectPath: normalizeProjectPath(path.posix.join(clean, 'Index' + ext)), abs: indexPath }
    }
  }

  throw new Error(`Creght module not found: ${projectPath}`)
}

async function readSiteConfig(projectRoot) {
  for (const name of ['creght.config.ts', 'creght.config.js', 'talizen.config.ts', 'talizen.config.js', 'folia.config.ts', 'folia.config.js']) {
    const abs = path.join(projectRoot, name)
    if (!(await exists(abs))) continue

    try {
      const source = await fs.readFile(abs, 'utf8')
      const transformed = await transformWithEsbuild(source, abs, {
        loader: name.endsWith('.ts') ? 'ts' : 'js',
        format: 'cjs',
        target: 'es2020',
      })
      const module = { exports: {} }
      const require = (specifier) => {
        if (specifier === 'creght' || specifier === 'creght/config' || specifier === 'talizen' || specifier === 'talizen/config') {
          return { defineConfig: (config) => config }
        }
        if (specifier.startsWith('creght/')) return {}
        if (specifier.startsWith('talizen/')) return {}
        throw new Error(`[creght.config] unsupported import: ${specifier}`)
      }
      const fn = new Function('module', 'exports', 'require', 'defineConfig', transformed.code)
      fn(module, module.exports, require, (config) => config)
      const value = module.exports?.default ?? module.exports
      return value && typeof value === 'object' ? value : {}
    } catch (err) {
      console.warn(`[creght vite] failed to evaluate ${name}:`, err)
      return {}
    }
  }

  return {}
}

async function readIndexCss(projectRoot, config) {
  if (typeof config.tailwindCss === 'string') return config.tailwindCss

  const cssPath = path.join(projectRoot, 'index.css')
  if (!(await exists(cssPath))) return ''
  return fs.readFile(cssPath, 'utf8')
}

async function buildImportMap(projectRoot, options) {
  const config = await readSiteConfig(projectRoot)
  const userImports = config.importMap?.imports || {}
  const imports = Object.keys(options.importMap || {}).length > 0
    ? { ...options.importMap }
    : { ...fallbackImportMap }

  for (const [specifier, url] of Object.entries(userImports)) {
    imports[specifier] = normalizeImportMapExternal(specifier, String(url))
  }
  for (const [specifier, url] of Object.entries(options.importMap || {})) {
    imports[specifier] = normalizeImportMapExternal(specifier, String(url))
  }

  return { config, imports }
}

async function listPages(projectRoot) {
  const pageRoot = path.join(projectRoot, 'page')
  if (!(await exists(pageRoot))) return []

  const out = []
  async function walk(dir) {
    const entries = await fs.readdir(dir, { withFileTypes: true })
    for (const entry of entries) {
      const abs = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        await walk(abs)
        continue
      }
      const ext = path.extname(entry.name)
      if (!pageExts.includes(ext)) continue
      if (entry.name.includes('.canvas.')) continue
      const rel = normalizeProjectPath(path.relative(projectRoot, abs))
      out.push({ file: rel, route: routeFromPagePath(rel) })
    }
  }
  await walk(pageRoot)
  return out.sort((a, b) => a.route.localeCompare(b.route))
}

function routeFromPagePath(file) {
  const withoutExt = file.replace(/^\/page\//, '').replace(/\.[^.]+$/, '')
  if (withoutExt === 'Index') return '/'
  return '/' + withoutExt
    .replace(/\/Index$/, '')
    .split('/')
    .map((segment) => segment.startsWith('[') && segment.endsWith(']') ? `:${segment.slice(1, -1)}` : segment.toLowerCase())
    .join('/')
}

function matchRoute(routes, pathname) {
  const normalized = pathname !== '/' ? pathname.replace(/\/+$/, '') : '/'
  const exact = routes.find((r) => r.route === normalized)
  if (exact) return { ...exact, params: {} }

  for (const route of routes) {
    const routeParts = route.route.split('/').filter(Boolean)
    const pathParts = normalized.split('/').filter(Boolean)
    if (routeParts.length !== pathParts.length) continue
    const params = {}
    let ok = true
    for (let i = 0; i < routeParts.length; i++) {
      const part = routeParts[i]
      if (part.startsWith(':')) {
        params[part.slice(1)] = decodeURIComponent(pathParts[i])
        continue
      }
      if (part !== pathParts[i].toLowerCase()) {
        ok = false
        break
      }
    }
    if (ok) return { ...route, params }
  }

  return { ...(routes.find((r) => r.route === '/') || routes[0]), params: {} }
}

function rewriteModuleSpecifiers(code, importerPath) {
  const rewrite = (specifier) => {
    if (!isRelativeSpecifier(specifier)) return specifier
    const [rawPath, rawQuery = ''] = specifier.split('?')
    const importerDir = path.posix.dirname(importerPath)
    const resolved = normalizeProjectPath(path.posix.join(importerDir, rawPath))
    const target = rawQuery ? `${resolved}?${rawQuery}` : resolved
    return `${modulePrefix}?path=${encodeURIComponent(target)}`
  }

  return code
    .replace(/(from\s*["'])([^"']+)(["'])/g, (_, before, specifier, after) => `${before}${rewrite(specifier)}${after}`)
    .replace(/(import\s*["'])([^"']+)(["'])/g, (_, before, specifier, after) => `${before}${rewrite(specifier)}${after}`)
    .replace(/(import\s*\(\s*["'])([^"']+)(["']\s*\))/g, (_, before, specifier, after) => `${before}${rewrite(specifier)}${after}`)
}

async function transformProjectModule(projectRoot, projectPath) {
  const [pathWithoutQuery, query = ''] = projectPath.split('?')
  const { projectPath: resolvedProjectPath, abs } = await resolveFile(projectRoot, pathWithoutQuery)
  const ext = path.extname(abs).toLowerCase()

  if (query === 'raw') {
    const source = await fs.readFile(abs, 'utf8')
    return `export default ${JSON.stringify(source)};\n`
  }
  if (query === 'url') {
    return `export default ${JSON.stringify(`${assetPrefix}?path=${encodeURIComponent(resolvedProjectPath)}`)};\n`
  }
  if (!sourceExts.has(ext)) {
    return `export default ${JSON.stringify(`${assetPrefix}?path=${encodeURIComponent(resolvedProjectPath)}`)};\n`
  }

  const source = await fs.readFile(abs, 'utf8')
  if (abs.endsWith('.css')) {
    return `
const css = ${JSON.stringify(source)};
let style = document.querySelector('style[data-talizen-css-module="${resolvedProjectPath}"]');
if (!style) {
  style = document.createElement('style');
  style.dataset.talizenCssModule = ${JSON.stringify(resolvedProjectPath)};
  document.head.appendChild(style);
}
style.textContent = css;
export default css;
`
  }

  const loader = abs.endsWith('.tsx') ? 'tsx' : abs.endsWith('.ts') ? 'ts' : abs.endsWith('.jsx') ? 'jsx' : 'js'
  const result = await transformWithEsbuild(source, abs, {
    loader,
    jsx: 'automatic',
    jsxDev: true,
    sourcemap: 'inline',
    target: 'es2020',
    define: {
      'process.env.NODE_ENV': '"development"',
      'process.env.RENDER_ENV': '"design"',
      'process.env.RENDER_MODE': '"design"',
    },
  })

  return rewriteModuleSpecifiers(result.code, resolvedProjectPath)
}

function renderRuntimeScript(entryFile, params, options) {
  const projectId = options.projectId || ''
  const authHeaders = options.token ? { Authorization: `Bearer ${options.token}` } : {}

  return `
import React from 'react';
import { createRoot } from 'react-dom/client';
import { createHotContext } from '/@vite/client';

const creghtRuntimeHeaders = ${jsonScript(authHeaders)};
const creghtRuntimeFetch = window.fetch.bind(window);

window.TalizenConfig = {
  baseUrl: window.location.origin + '/api/u/v2/project/' + ${JSON.stringify(projectId)},
  headers: creghtRuntimeHeaders,
  fetch(input, init = {}) {
    const headers = new Headers(init.headers || {});
    for (const [key, value] of Object.entries(creghtRuntimeHeaders)) {
      if (!headers.has(key)) headers.set(key, value);
    }
    return creghtRuntimeFetch(input, { ...init, headers });
  },
};

const rootEl = document.getElementById('root');
const root = createRoot(rootEl);
let renderVersion = 0;

async function loadPageModule() {
  return import(${JSON.stringify(`${modulePrefix}?path=${encodeURIComponent(entryFile)}`)} + '&t=' + Date.now());
}

async function renderPage() {
  const version = ++renderVersion;
  const pageModule = await loadPageModule();
  if (version !== renderVersion) return;

  const Page = pageModule.default || pageModule.App;
  let props = {};

  if (!Page) {
    throw new Error('Creght page has no default export: ${entryFile}');
  }

  if (typeof pageModule.getServerSideProps === 'function') {
    const result = await pageModule.getServerSideProps({
      query: Object.fromEntries(new URLSearchParams(window.location.search)),
      params: ${jsonScript(params)},
    });
    props = result && result.props ? result.props : {};
  }

  root.render(React.createElement(Page, props));
}

const hot = createHotContext(${JSON.stringify(`${runtimePrefix}?entry=${encodeURIComponent(entryFile)}`)});

await renderPage();

hot.on('creght:update', () => {
  renderPage().catch((err) => {
    console.error('[creght vite] hot update failed', err);
  });
});
`
}

async function renderHtml(projectRoot, pathname, options) {
  const routes = await listPages(projectRoot)
  if (routes.length === 0) {
    return `<!doctype html><div style="font:14px system-ui;padding:24px">No Creght pages found in <code>/page</code>.</div>`
  }

  const matched = matchRoute(routes, pathname)
  const { config, imports } = await buildImportMap(projectRoot, options)
  const indexCss = await readIndexCss(projectRoot, config)
  const customCode = config.customCode || {}

  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>${escapeHtml(matched.route === '/' ? 'Creght' : matched.route)}</title>
  <script type="importmap">${jsonScript({ imports })}</script>
  <script type="module" src="/@vite/client"></script>
  <style type="text/tailwindcss">${indexCss}</style>
  <script type="module" src="https://esm.talizen.com/@tailwindcss/browser@4"></script>
  ${customCode.head || ''}
</head>
<body>
  ${customCode.bodyStart || ''}
  <div id="root"></div>
  <script type="module" src="${runtimePrefix}?entry=${encodeURIComponent(matched.file)}&params=${encodeURIComponent(JSON.stringify(matched.params))}&t=${Date.now()}"></script>
  ${customCode.bodyEnd || customCode.body || ''}
</body>
</html>`
}

export function creght(options = {}) {
  let projectRoot = ''
  const apiHost = (options.apiHost || 'https://creght.cn').replace(/\/+$/, '')

  return {
    name: 'creght-local-preview',
    enforce: 'pre',
    configResolved(config) {
      projectRoot = path.resolve(options.root || config.root)
    },
    configureServer(server) {
      const hmrTimers = new Map()
      const sendCreghtUpdate = (file) => {
        if (!file.startsWith(projectRoot)) return

        const projectPath = normalizeProjectPath(path.relative(projectRoot, file))
        const existing = hmrTimers.get(projectPath)
        if (existing) clearTimeout(existing)

        hmrTimers.set(projectPath, setTimeout(() => {
          hmrTimers.delete(projectPath)
          server.ws.send({
            type: 'custom',
            event: 'creght:update',
            data: {
              file: projectPath,
              time: Date.now(),
            },
          })
        }, 80))
      }

      server.watcher.add(path.join(projectRoot, '**/*'))
      server.watcher.on('change', sendCreghtUpdate)
      server.watcher.on('add', sendCreghtUpdate)
      server.watcher.on('unlink', sendCreghtUpdate)

      server.middlewares.use(async (req, res, next) => {
        try {
          const url = new URL(req.url || '/', 'http://localhost')

          if (url.pathname.startsWith('/api/')) {
            const headers = new Headers()
            for (const [key, value] of Object.entries(req.headers)) {
              if (Array.isArray(value)) {
                headers.set(key, value.join(', '))
              } else if (value != null) {
                headers.set(key, value)
              }
            }
            headers.set('host', new URL(apiHost).host)
            if (options.token) {
              headers.delete('cookie')
              headers.set('authorization', `Bearer ${options.token}`)
            }

            const method = req.method || 'GET'
            const body = method === 'GET' || method === 'HEAD' ? undefined : req
            const upstream = await fetch(apiHost + url.pathname + url.search, {
              method,
              headers,
              body,
              // Required by Node fetch when the request body is a stream.
              duplex: body ? 'half' : undefined,
            })

            res.statusCode = upstream.status
            upstream.headers.forEach((value, key) => {
              const lowerKey = key.toLowerCase()
              if (lowerKey === 'content-encoding') return
              if (lowerKey === 'content-length') return
              if (lowerKey === 'transfer-encoding') return
              if (lowerKey === 'connection') return
              res.setHeader(key, value)
            })
            const data = Buffer.from(await upstream.arrayBuffer())
            res.end(data)
            return
          }

          if (url.pathname === modulePrefix) {
            const projectPath = url.searchParams.get('path')
            if (!projectPath) {
              res.statusCode = 400
              res.end('missing path')
              return
            }
            const code = await transformProjectModule(projectRoot, projectPath)
            res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
            res.end(code)
            return
          }

          if (url.pathname === runtimePrefix) {
            const entry = url.searchParams.get('entry')
            const rawParams = url.searchParams.get('params') || '{}'
            if (!entry) {
              res.statusCode = 400
              res.end('missing entry')
              return
            }
            let params = {}
            try {
              params = JSON.parse(rawParams)
            } catch {
              params = {}
            }
            res.setHeader('Content-Type', 'text/javascript; charset=utf-8')
            res.end(renderRuntimeScript(entry, params, options))
            return
          }

          if (url.pathname === assetPrefix) {
            const projectPath = url.searchParams.get('path')
            if (!projectPath) {
              res.statusCode = 400
              res.end('missing path')
              return
            }
            const { abs } = await resolveFile(projectRoot, projectPath)
            res.setHeader('Content-Type', contentTypeForPath(abs))
            res.end(await fs.readFile(abs))
            return
          }

          if (req.method !== 'GET') {
            next()
            return
          }

          const accept = req.headers.accept || ''
          if (!accept.includes('text/html')) {
            next()
            return
          }

          const html = await renderHtml(projectRoot, url.pathname, options)
          res.setHeader('Content-Type', 'text/html; charset=utf-8')
          res.end(html)
        } catch (err) {
          next(err)
        }
      })
    },
  }
}

export default creght
