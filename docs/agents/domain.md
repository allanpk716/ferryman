# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

This repo is **single-context**. Its glossary is **`CONTEXT.md`** at the repo root (it plays the role other repos give to `GLOSSARY.md`; do not create a separate `GLOSSARY.md`).

## Before exploring, read these

- **`CONTEXT.md`** at the repo root: the domain glossary (terms, `_Avoid_` synonyms).
- **`docs/adr/`**: read ADRs that touch the area you're about to work in.

If any of these files don't exist, **proceed silently**. The `/domain-modeling` skill (reached via `/grill-with-docs` and `/improve-codebase-architecture`) updates them when terms or decisions actually get resolved.

## File structure

```
/
├── CONTEXT.md        ← glossary
├── docs/adr/
│   ├── 0001-summarize-threshold-before-block.md
│   └── ...
└── internal/
```

ADR numbers collide in a few places (0016, 0018 and 0026 each have two files). When citing one of those, use its alias or full file name; see `docs/adr/README.md`.

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal: either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders), but worth reopening because…_
