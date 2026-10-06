# Product design v1

Direction: restrained workspace controls, clear typography, and a browsable collection of private game-night spaces. Codenames leads; Mutation Lab is secondary. Keep real membership counts and browser-bound guest access explicit.

## Code and design contract

- `web/src/product.css` owns the revised semantic color tokens and product layout.
- Nunito Regular/Medium/Bold is the product type family. Headings are bold, compact, and left aligned. Buttons share a 7 px radius, cards a 12 px radius, and content uses a 24 px grid gap.
- `web/src/components/HouseCard.tsx` is the reusable collection tile. Its cover is decorative geometry, not a gameplay screenshot or claimed live match.
- Home has header, concise introduction, searchable House collection, and guest-access guidance. First use has create/invite/play steps.
- Room keeps Codenames first, optional Mutation Lab second, people/voice separate, and invitations visible in the header.
- Forms, dialogs, loading, errors, invitation approval, and room recovery retain their existing functional behavior.

## Figma handoff

Existing file: https://www.figma.com/design/ueqg10kXmMBfTNeFjMn2XB
Existing references: `design/figma.json`. That manifest records the earlier design, not this revision.

On October 6, 2026, the connector read the existing Home instances and variables successfully, but the next call returned the Starter-plan MCP call limit. No revised canvas nodes or Code Connect mappings were saved in this pass. `design/product-v1.json` records the intended source/component mappings and pending screens so the sync can resume without guessing IDs.

Resume by updating semantic color values to the CSS light/dark pairs, setting Nunito heading styles, and rebuilding Home desktop/mobile, populated House collection, room game chooser, and invite dialog with editable layers and local component instances. Preserve earlier screens as history. Add a HouseCard component and map it to the source; use verified node IDs returned by Figma. Render and compare both 1440 px and 390 px views before marking the sync complete.
