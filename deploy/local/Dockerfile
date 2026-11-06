# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0

FROM node:22-alpine AS ui
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /bogform-server ./cmd/bogform-server

FROM alpine:3.20
RUN adduser -D -H bogform
COPY --from=server /bogform-server /usr/local/bin/bogform-server
COPY --from=ui /src/dist /srv/ui
USER bogform
ENTRYPOINT ["/usr/local/bin/bogform-server"]
CMD ["serve"]
