// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
    site: 'https://open-outbox.dev',
    base: '/',
    integrations: [
        starlight({
            title: 'Open Outbox',
            customCss: ['./src/styles/custom.css'],
            logo: {
                alt: 'Open Outbox Logo',
                src: './src/assets/logo.svg',
            },
            social: [
                { icon: 'github', label: 'GitHub', href: 'https://github.com/open-outbox/relay' },
                { icon: 'discord', label: 'Discord', href: 'https://discord.gg/Tk3jwfm7' }
            ],
            editLink: {
                baseUrl: 'https://github.com/open-outbox/relay/edit/main/docs/',
            },
            sidebar: [
                {
                    label: 'Getting Started',
                    items: [
                        { label: 'Introduction', link: '/tutorial/introduction' },
                        { label: 'Quick Start', link: '/tutorial/quick-start' },
                    ],
                },
                {
                    label: 'Guides',
                    autogenerate: { directory: 'guides' },
                },
                {
                    label: 'Reference',
                    collapsed: true,
                    items: [
                        { label: 'Configuration', link: '/reference/configuration' },
                        { label: 'Diagnostics & Metrics', link: '/reference/diagnostics' },
                        {
                            label: 'API Reference',
                            collapsed: true,
                            autogenerate: { directory: 'reference/api',},
                        },
                    ],
                },
                {
                    label: 'Architecture & Spec',
                    collapsed: true,
                    autogenerate: { directory: 'spec' },
                },
                {
                    label: 'Project',
                    items: [
                    { label: 'Roadmap', link: 'project/roadmap' },
                    { label: 'How to Contribute', link: 'project/contributing' },
                    ]
                },
            ],
        }),
    ],
});
