# Copyright (c) The OpenTofu Authors
# SPDX-License-Identifier: MPL-2.0
# Copyright (c) 2023 HashiCorp, Inc.
# SPDX-License-Identifier: MPL-2.0

FROM alpine:3.20

LABEL maintainer="Bogform maintainers <https://github.com/bogware/bogform>"

RUN apk add --no-cache git bash openssh

COPY bogform /usr/local/bin/bogform

ENTRYPOINT ["/usr/local/bin/bogform"]
