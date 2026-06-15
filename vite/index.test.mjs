import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { normalizeImportMapExternal } from './import-map.js'

describe('normalizeImportMapExternal', () => {
  it('adds react and react-dom externals to regular import map entries', () => {
    assert.equal(
      normalizeImportMapExternal('framer-motion', 'https://esm.talizen.com/framer-motion'),
      'https://esm.talizen.com/framer-motion?external=react,react-dom',
    )
  })

  it('merges missing externals without dropping existing entries', () => {
    assert.equal(
      normalizeImportMapExternal(
        '@react-three/drei',
        'https://esm.talizen.com/@react-three/drei?external=three,react,@react-three/fiber',
      ),
      'https://esm.talizen.com/@react-three/drei?external=three,react,@react-three/fiber,react-dom',
    )
  })

  it('inserts externals before the trailing slash for prefix import map entries', () => {
    assert.equal(
      normalizeImportMapExternal('talizen/', 'https://esm.talizen.com/talizen@0.1.4/'),
      'https://esm.talizen.com/talizen@0.1.4&external=react,react-dom/',
    )
  })

  it('merges externals before the trailing slash for prefix entries', () => {
    assert.equal(
      normalizeImportMapExternal(
        '@kobalte/core/',
        'https://esm.talizen.com/@kobalte/core&external=react/',
      ),
      'https://esm.talizen.com/@kobalte/core&external=react,react-dom/',
    )
  })

  it('does not modify host React runtime specifiers', () => {
    assert.equal(
      normalizeImportMapExternal('react-dom/client', 'https://esm.talizen.com/react-dom@19/client?dev'),
      'https://esm.talizen.com/react-dom@19/client?dev',
    )
  })
})
