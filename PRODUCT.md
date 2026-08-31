# Product

## Register

product

## Platform

web

## Users

Developers inside the company who need to maintain a shared Cloudflare Bulk Redirect List but do not have permission to create Cloudflare API tokens themselves. They work interactively in a terminal and also automate repeatable changes in scripts or CI.

## Product Purpose

Provide a safe, focused way to list, search, add, edit, import, and explicitly delete redirects in one preconfigured account-level Cloudflare Bulk Redirect List. Success means developers can make routine redirect changes without using the Cloudflare dashboard or receiving broader account access.

## Positioning

A deliberate redirect editor, not a general Cloudflare administration client: every mutation is validated, previewed, confirmed, and constrained to one configured list.

## Brand Personality

Calm, precise, and trustworthy. Copy is direct and operational. The tool avoids jokes, celebration, and visual noise around infrastructure changes.

## Anti-references

Do not resemble a playful animated terminal toy, an overloaded infrastructure dashboard exposing every Cloudflare concept, or a thin wrapper around raw HTTP endpoints.

## Design Principles

- Make the pending change understandable before asking for commitment.
- Keep interactive and scripted behavior consistent by sharing one planning and execution core.
- Default to preserving data: CSV import adds or updates and never deletes omitted entries.
- Expose enough Cloudflare state to diagnose failures without exposing credentials or irrelevant administration features.
- Optimize the first release for one account and one list while keeping list identity explicit in the domain model.

## Accessibility & Inclusion

No formal accessibility target is required for the first release. Preserve basic terminal conventions and keyboard operation without making accessibility work a release gate.
