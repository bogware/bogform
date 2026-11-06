# Bogform documentation

This directory contains the [Bogform documentation site](https://bogware.github.io/bogform/docs/),
built with [Docusaurus](https://docusaurus.io/).

## Preview your changes

With Node.js 20 or later installed, run these commands from this directory:

```shell
npm ci
npm start
```

To check for broken links, run `npm run build`. The build fails if it finds any.

## Add or move a page

The sidebar follows the `docs` directory. Set `sidebar_position` and `sidebar_label` in a page's front
matter to control where it appears and what it's called.

## Publish

To publish with GitHub Pages, set **Settings > Pages > Source** to **GitHub Actions** and add a repository
variable named `PAGES_ENABLED` with the value `true`. Merging to `main` then publishes the site. GitHub
Pages needs a public repository, or a paid plan for a private one.

To use a custom domain, change `url` and `baseUrl` in `docusaurus.config.js`, and update the
`bogware.github.io` links in the Go code.
