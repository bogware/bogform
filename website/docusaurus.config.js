// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// To publish on a custom domain, change only `url` and `baseUrl` here. The links printed by the
// CLI are defined separately; search the Go code for bogware.github.io to update them as well.
const url = 'https://bogware.github.io';
const baseUrl = '/bogform/';
const repository = 'https://github.com/bogware/bogform';

module.exports = {
  title: 'Bogform',
  tagline: 'Cheap, secure, friendly infrastructure as code',
  url,
  baseUrl,
  organizationName: 'bogware',
  projectName: 'bogform',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  markdown: {
    format: 'mdx',
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  presets: [
    [
      'classic',
      {
        docs: {
          path: 'docs',
          routeBasePath: 'docs',
          sidebarPath: require.resolve('./sidebars.js'),
          editUrl: `${repository}/edit/main/website/`,
        },
        blog: false,
        theme: {
          customCss: require.resolve('./src/css/custom.css'),
        },
      },
    ],
  ],
  themeConfig: {
    navbar: {
      title: 'Bogform',
      items: [
        { to: '/docs/', label: 'Docs', position: 'left' },
        { href: repository, label: 'GitHub', position: 'right' },
      ],
    },
    footer: {
      style: 'dark',
      copyright:
        'Bogform is licensed under the MPL 2.0 and is not affiliated with the OpenTofu project or HashiCorp.',
    },
    prism: {
      additionalLanguages: ['bash', 'hcl', 'json', 'powershell'],
    },
  },
};
