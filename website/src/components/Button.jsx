// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

import React from 'react';
import Link from '@docusaurus/Link';

export default function Button({ href, children }) {
  return (
    <Link className="button button--primary margin-right--sm" to={href}>
      {children}
    </Link>
  );
}
