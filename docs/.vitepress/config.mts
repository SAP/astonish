import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { fileURLToPath, URL } from 'node:url'
import path from 'path'

export default withMermaid(defineConfig({
  base: '/astonish/',
  srcDir: 'website',
  cleanUrls: true,
  lastUpdated: true,
  ignoreDeadLinks: true,
  title: "Astonish",
  description: "AI Agent Platform That Makes Your Whole Team Smarter",
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/astonish/astonish-logo.svg' }],
    ['link', { rel: 'manifest', href: '/astonish/site.webmanifest' }],
    ['meta', { name: 'theme-color', content: '#7c3aed' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:site_name', content: 'Astonish' }],
  ],
  themeConfig: {
    logo: {
      light: '/astonish-logo.svg',
      dark: '/astonish-logo.svg',
    },
    nav: [
      {
        text: 'Documentation',
        link: '/docs/',
        activeMatch: 'docs',
      },
      {
        text: 'Community',
        link: 'https://github.com/SAP/astonish/discussions',
      },
    ],
    sidebar: {
      '/docs/': [
        {
          text: 'Getting Started',
          items: [
            { text: 'Introduction', link: '/docs/' },
            { text: 'Installation', link: '/docs/getting-started/installation' },
            { text: 'Quick Start: Local', link: '/docs/getting-started/quick-start-local' },
            { text: 'Quick Start: Cloud', link: '/docs/getting-started/quick-start-cloud' },
            { text: 'Choose Your Interface', link: '/docs/getting-started/choose-your-interface' },
            { text: 'Architecture', link: '/docs/getting-started/architecture' },
          ]
        },
        {
          text: 'Platform',
          items: [
            { text: 'Overview', link: '/docs/platform/' },
            { text: 'Organizations & Teams', link: '/docs/platform/organizations-and-teams' },
            { text: 'Three-Tier Memory', link: '/docs/platform/three-tier-memory' },
            { text: 'Cascading Defaults', link: '/docs/platform/cascading-defaults' },
            { text: 'Publish & Fork', link: '/docs/platform/publish-and-fork' },
            { text: 'Remote CLI', link: '/docs/platform/remote-cli' },
            { text: 'Administration', link: '/docs/platform/administration' },
          ]
        },
        {
          text: 'Agent',
          items: [
            { text: 'Chat', link: '/docs/agent/chat' },
            { text: 'Memory', link: '/docs/agent/memory' },
            { text: 'Sessions', link: '/docs/agent/sessions' },
            { text: 'Skills', link: '/docs/agent/skills' },
            { text: 'Sub-agents', link: '/docs/agent/sub-agents' },
            { text: 'Tools Overview', link: '/docs/agent/tools/' },
            { text: 'Shell & Process', link: '/docs/agent/tools/shell-process' },
            { text: 'File & Search', link: '/docs/agent/tools/file-search' },
            { text: 'Web & HTTP', link: '/docs/agent/tools/web-http' },
            { text: 'Browser', link: '/docs/agent/tools/browser' },
            { text: 'Email', link: '/docs/agent/tools/email' },
            { text: 'Credentials', link: '/docs/agent/tools/credentials' },
            { text: 'Scheduler & Agent', link: '/docs/agent/tools/scheduler-agent' },
          ]
        },
        {
          text: 'Flows',
          items: [
            { text: 'Overview', link: '/docs/flows/' },
            { text: 'Distillation', link: '/docs/flows/distillation' },
            { text: 'YAML Reference', link: '/docs/flows/yaml-reference' },
            { text: 'Nodes, Edges & State', link: '/docs/flows/nodes-edges-state' },
            { text: 'Taps & Flow Store', link: '/docs/flows/taps' },
          ]
        },
        {
          text: 'Generative UI',
          items: [
            { text: 'Overview', link: '/docs/generative-ui/' },
            { text: 'Building Apps', link: '/docs/generative-ui/building-apps' },
            { text: 'Data Hooks', link: '/docs/generative-ui/data-hooks' },
            { text: 'Sharing & Persistence', link: '/docs/generative-ui/sharing' },
          ]
        },
        {
          text: 'Channels',
          items: [
            { text: 'Overview', link: '/docs/channels/' },
            { text: 'Telegram', link: '/docs/channels/telegram' },
            { text: 'Email', link: '/docs/channels/email' },
            { text: 'Slack', link: '/docs/channels/slack' },
            { text: 'A2A (Agent-to-Agent)', link: '/docs/channels/a2a' },
          ]
        },
        {
          text: 'Fleet',
          items: [
            { text: 'Overview', link: '/docs/fleet/' },
            { text: 'Templates', link: '/docs/fleet/templates' },
            { text: 'Plans', link: '/docs/fleet/plans' },
            { text: 'Sessions & Threads', link: '/docs/fleet/sessions-threads' },
            { text: 'Fleet in Studio', link: '/docs/fleet/studio' },
          ]
        },
        {
          text: 'Security & Compliance',
          items: [
            { text: 'Authentication', link: '/docs/security/authentication' },
            { text: 'Envelope Encryption', link: '/docs/security/envelope-encryption' },
            { text: 'Sandboxes', link: '/docs/security/sandboxes' },
            { text: 'Network Policy', link: '/docs/security/network-policy' },
            { text: 'Audit Logging', link: '/docs/security/audit-logging' },
            { text: 'Credential Security', link: '/docs/security/credential-security' },
          ]
        },
        {
          text: 'Deployment',
          items: [
            { text: 'Overview', link: '/docs/deployment/' },
            { text: 'Kubernetes', link: '/docs/deployment/kubernetes' },
            { text: 'OpenShell', link: '/docs/deployment/openshell' },
            { text: 'Running as a Service', link: '/docs/deployment/running-as-service' },
          ]
        },
        {
          text: 'Studio',
          items: [
            { text: 'Overview', link: '/docs/studio/' },
            { text: 'Chat Interface', link: '/docs/studio/chat' },
            { text: 'Flow Editor', link: '/docs/studio/flow-editor' },
            { text: 'Settings', link: '/docs/studio/settings' },
            { text: 'Running & Debugging', link: '/docs/studio/running-debugging' },
            { text: 'Keyboard Shortcuts', link: '/docs/studio/keyboard-shortcuts' },
          ]
        },
        {
          text: 'Configuration',
          items: [
            { text: 'Config Reference', link: '/docs/configuration/config-reference' },
            { text: 'AI Providers', link: '/docs/configuration/providers' },
            { text: 'MCP Servers', link: '/docs/configuration/mcp-servers' },
            { text: 'Taps', link: '/docs/configuration/taps' },
          ]
        },
        {
          text: 'CLI Reference',
          items: [
            { text: 'Code Mode', link: '/docs/cli/code' },
            { text: 'Chat Commands', link: '/docs/cli/chat' },
            { text: 'Flow Commands', link: '/docs/cli/flows' },
            { text: 'Platform Commands', link: '/docs/cli/platform' },
            { text: 'Daemon & Scheduler', link: '/docs/cli/daemon-scheduler' },
            { text: 'Utility Commands', link: '/docs/cli/utility' },
          ]
        },
        {
          text: 'Reference',
          items: [
            { text: 'Glossary', link: '/docs/reference/glossary' },
            { text: 'Troubleshooting', link: '/docs/reference/troubleshooting' },
          ]
        },
      ]
    },
    socialLinks: [
      { icon: 'github', link: 'https://github.com/SAP/astonish' },
    ],
    search: {
      provider: 'local',
    },
    outline: {
      label: 'Page Content'
    },
  },
  vite: {
    resolve: {
      alias: [
        {
          find: '@components',
          replacement: path.resolve(path.dirname(fileURLToPath(import.meta.url)), './theme/components')
        },
        {
          find: /^.*\/VPFeature\.vue$/,
          replacement: fileURLToPath(
            new URL('./theme/components/VPFeature.vue', import.meta.url)
          )
        },
        {
          find: /^.*\/VPFeatures\.vue$/,
          replacement: fileURLToPath(
            new URL('./theme/components/VPFeatures.vue', import.meta.url)
          )
        },
        {
          find: /^.*\/VPTeamMembersItem\.vue$/,
          replacement: fileURLToPath(
            new URL('./theme/components/VPTeamMembersItem.vue', import.meta.url)
          )
        },
      ]
    }
  },
  markdown: {
    config(md: any) {
      const defaultCodeInline = md.renderer.rules.code_inline!
      md.renderer.rules.code_inline = (tokens: any, idx: any, options: any, env: any, self: any) => {
        tokens[idx].attrSet('v-pre', '')
        return defaultCodeInline(tokens, idx, options, env, self)
      }
    },
  },
  mermaid: {
    securityLevel: 'strict',
  },
}))
