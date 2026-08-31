# Design System

## Intent

A calm operational terminal interface for reviewing and applying redirect changes. Familiar terminal conventions should disappear into the task.

## Theme

Use the terminal's own background and foreground as the primary surfaces. Apply a restrained color strategy: deep rose is reserved for focus and product identity, while semantic colors communicate additions, changes, deletion, and errors.

## Color

Lip Gloss adaptive colors are the implementation tokens:

- Primary: deep rose, approximately `oklch(0.36 0.147 340)` in capable terminals.
- Ink/background: inherit terminal defaults.
- Muted: adaptive neutral gray.
- Add/success: restrained green.
- Update/warning: amber.
- Delete/error: red.

Never rely on elaborate gradients, background fills, or decorative color. Text markers (`+`, `~`, `-`) accompany semantic colors.

## Typography

Use the terminal's monospace font. Hierarchy comes from weight, spacing, concise labels, and alignment—not oversized text or ASCII-art headings.

## Layout

A compact header identifies the configured list and current mode. The main list prioritizes source and target. Contextual help stays at the bottom. Narrow terminals switch to stacked rows rather than clipping URLs.

## Components

- Redirect list: keyboard-first selection with a quiet highlighted row.
- Detail view: shows full URLs and redirect options for the selected item.
- Forms: conventional labeled source and target inputs with inline validation.
- Plan review: deterministic `+`, `~`, and `-` diff with counts.
- Confirmation: explicit apply/cancel choices; destructive operations use direct language.
- Status: restrained spinner only during active network operations; success and failure copy remains factual.

## Motion

No decorative animation. State transitions are immediate. A spinner is permitted only while waiting on Cloudflare.
