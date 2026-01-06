import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Buckley',
  description: 'The AI development assistant that doesn\'t waste your time',
  ignoreDeadLinks: true,

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
      { text: 'Blog', link: '/blog/' },
      { text: 'GitHub', link: 'https://github.com/odvcencio/buckley' }
    ],

    sidebar: {
      '/': [
        {
          text: 'Getting Started',
          items: [
            { text: 'Introduction', link: '/' },
            { text: 'CLI Reference', link: '/CLI' },
            { text: 'Configuration', link: '/CONFIGURATION' },
          ]
        },
        {
          text: 'Core Concepts',
          items: [
            { text: 'Skills', link: '/SKILLS' },
            { text: 'Tools', link: '/TOOLS' },
            { text: 'Orchestration', link: '/ORCHESTRATION' },
            { text: 'Experiments', link: '/EXPERIMENTS' },
          ]
        },
        {
          text: 'Interfaces',
          items: [
            { text: 'Mission Control (Web)', link: '/MISSION_CONTROL' },
            { text: 'Editor Integration', link: '/ACP' },
          ]
        },
        {
          text: 'Reference',
          items: [
            { text: 'Error Handling', link: '/ERRORS' },
            { text: 'Architecture Decisions', link: '/architecture/decisions/' },
          ]
        },
      ],
      '/blog/': [
        {
          text: 'Blog',
          items: [
            { text: 'All Posts', link: '/blog/' },
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
