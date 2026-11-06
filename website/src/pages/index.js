// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import React from 'react';
import { Redirect } from '@docusaurus/router';
import useBaseUrl from '@docusaurus/useBaseUrl';

export default function Home() {
  return <Redirect to={useBaseUrl('/docs/')} />;
}
