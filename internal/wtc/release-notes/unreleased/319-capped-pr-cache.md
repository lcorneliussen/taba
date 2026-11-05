## Fixes

- Refresh expired pull-request details by number when an incomplete open-PR list omits them, so cached drafts can become merged without waiting for the longer audit interval. Fresh cache reuse, shared request limits and coalescing remain in place; unavailable fresh reads stay pending or unknown.
