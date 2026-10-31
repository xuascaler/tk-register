import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  base: '/tk-register/',
  plugins: [react()],
  server: {
    proxy: { '/tk-register/api': 'http://127.0.0.1:3002' },
  },
  build: { outDir: 'dist', emptyOutDir: true },
});
