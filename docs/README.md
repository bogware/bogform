# Bogform Core Codebase Documentation

This directory contains some documentation about the Bogform Core codebase,
aimed at readers who are interested in making code contributions.

If you're looking for information on _using_ Bogform, please instead refer
to [the main Bogform CLI documentation](https://bogware.github.io/bogform/docs/cli/index.html).

## Bogform Core Architecture Documents

* [Bogform Core Architecture Summary](./architecture.md): an overview of the
  main components of Bogform Core and how they interact. This is the best
  starting point if you are diving in to this codebase for the first time.

* [Resource Instance Change Lifecycle](./resource-instance-change-lifecycle.md):
  a description of the steps in validating, planning, and applying a change
  to a resource instance, from the perspective of the provider plugin RPC
  operations. This may be useful for understanding the various expectations
  Bogform enforces about provider behavior, either if you intend to make
  changes to those behaviors or if you are implementing a new Bogform plugin
  SDK and so wish to conform to them.

  (If you are planning to write a new provider using the _official_ SDK then
  please refer to [the Extend documentation](https://github.com/hashicorp/terraform-docs-common)
  instead; it presents similar information from the perspective of the SDK
  API, rather than the plugin wire protocol.)

* [Diagnostics](./diagnostics.md): how we report errors and warnings to end-users
  in Bogform.

* [Plugin Protocol](./plugin-protocol/): gRPC/protobuf definitions for the
  plugin wire protocol and information about its versioning strategy.

  This documentation is for SDK developers, and is not necessary reading for
  those implementing a provider using the official SDK.

* [How Bogform Uses Unicode](./unicode.md): an overview of the various
  features of Bogform that rely on Unicode and how to change those features
  to adopt new versions of Unicode.

## Contribution Guides

* [Contributing to Bogform](../CONTRIBUTING.md): a complete guideline for those who want to contribute to this project.
