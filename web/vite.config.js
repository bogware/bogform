// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
    },
  },
  build: {
    // The content security policy forbids inline scripts and styles, so keep assets in files.
    assetsInlineLimit: 0,
  },
});
