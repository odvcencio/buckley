import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Buckley',
  description: 'Tool-first AI agent harness for serious repository work',

  head: [
    ['link', { rel: 'icon', type: 'image/png', href: '/favicon.png' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
    ['link', { href: 'https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500&family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap', rel: 'stylesheet' }],
  ],

  themeConfig: {
    logo: '/favicon.png',

    nav: [
      { text: 'Docs', link: '/' },
      { text: 'Blog', link: '/blog/2026-01-04-introducing-buckley' },
      { text: 'GitHub', link: 'https://github.com/odvcencio/buckley' }
    ],

    sidebar: {
      '/': [
        {
          text: 'Getting Started',
          items: [
            { text: 'Introduction', link: '/' },
            { text: 'CLI Reference', link: '/CLI' },
            { text: 'Configuration', link: '/CONFIGURATION' }
          ]
        },
        {
          text: 'Operating Buckley',
          items: [
            { text: 'Running Goals', link: '/goals' },
            { text: 'Code Mode', link: '/code-mode' },
            { text: 'Troubleshooting', link: '/troubleshooting' }
          ]
        },
        {
          text: 'Interfaces',
          items: [
            { text: 'Mission Control (GoSX)', link: '/MISSION_CONTROL' },
            { text: 'Editor Integration', link: '/ACP' }
          ]
        },
        {
          text: 'Blog',
          items: [
            { text: 'Introducing Buckley', link: '/blog/2026-01-04-introducing-buckley' },
          ]
        }
      ]
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/odvcencio/buckley' }
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2024-present'
    },

    search: {
      provider: 'local'
    }
  }
})
