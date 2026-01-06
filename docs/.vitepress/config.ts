import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'Buckley',
  description: 'The AI development assistant that doesn\'t waste your time',

  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/logo.svg' }],
  ],

  themeConfig: {
    logo: '/logo.svg',

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
            { text: 'Configuration', link: '/CONFIGURATION' },
            { text: 'CLI Reference', link: '/CLI' },
          ]
        },
        {
          text: 'Core Concepts',
          items: [
            { text: 'State Management', link: '/STATE' },
            { text: 'Error Handling', link: '/ERRORS' },
          ]
        },
        {
          text: 'Agent Coordination',
          items: [
            { text: 'ACP Protocol', link: '/ACP' },
            { text: 'ACP API', link: '/ACP_API' },
            { text: 'Deployment', link: '/ACP_DEPLOYMENT' },
            { text: 'Security', link: '/ACP_SECURITY_AUDIT' },
            { text: 'Tool Policies', link: '/ACP_TOOL_POLICY' },
          ]
        },
        {
          text: 'Experimentation',
          items: [
            { text: 'Experiments', link: '/EXPERIMENTS' },
            { text: 'Framework Guide', link: '/EXPERIMENTATION' },
          ]
        },
        {
          text: 'Operations',
          items: [
            { text: 'Mission Control', link: '/MISSION_CONTROL' },
            { text: 'Releases', link: '/RELEASE' },
          ]
        },
      ],
      '/blog/': [
        {
          text: 'Blog',
          items: [
            { text: 'All Posts', link: '/blog/' },
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
