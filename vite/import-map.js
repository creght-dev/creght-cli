function mergeExternalDeps(externalValue, requiredDeps) {
  try {
    externalValue = decodeURIComponent(externalValue)
  } catch {
    // Keep the original value and continue merging invalid encoded external lists.
  }

  const seen = new Set()
  const deps = externalValue
    .split(',')
    .map((dep) => dep.trim())
    .filter(Boolean)
    .filter((dep) => {
      if (seen.has(dep)) return false
      seen.add(dep)
      return true
    })

  for (const dep of requiredDeps) {
    if (seen.has(dep)) continue
    seen.add(dep)
    deps.push(dep)
  }

  return deps.join(',')
}

export function normalizeImportMapExternal(specifier, url) {
  if (!/^https?:\/\//i.test(url)) return url
  if (
    specifier === 'react' ||
    specifier.startsWith('react/') ||
    specifier === 'react-dom' ||
    specifier.startsWith('react-dom/')
  ) {
    return url
  }

  const requiredDeps = ['react', 'react-dom']
  const hashIndex = url.indexOf('#')
  const beforeHash = hashIndex >= 0 ? url.slice(0, hashIndex) : url
  const hash = hashIndex >= 0 ? url.slice(hashIndex) : ''
  const hasPrefixSlash = specifier.endsWith('/') && beforeHash.endsWith('/')
  const externalTarget = hasPrefixSlash ? beforeHash.slice(0, -1) : beforeHash
  const externalSuffix = hasPrefixSlash ? '/' : ''
  const externalMatch = externalTarget.match(/([?&])external=([^&#]*)/)

  if (externalMatch && externalMatch.index !== undefined) {
    const start = externalMatch.index + externalMatch[1].length + 'external='.length
    const end = start + externalMatch[2].length
    return `${externalTarget.slice(0, start)}${mergeExternalDeps(externalMatch[2], requiredDeps)}${externalTarget.slice(end)}${externalSuffix}${hash}`
  }

  return `${externalTarget}${hasPrefixSlash || externalTarget.includes('?') ? '&' : '?'}external=${requiredDeps.join(',')}${externalSuffix}${hash}`
}
