/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.tsx'
import './index.css'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root element')

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
