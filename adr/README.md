# Architecture Decision Records

Each ADR is a short, numbered, immutable document recording one architectural
decision: context → decision → consequences. Superseded decisions are not
edited — a new ADR replaces them and links back.

File naming: `ADR-001-terminal-ontology.md`, etc.

Suggested template:

```markdown
# ADR-NNN: Title

- Status: proposed | accepted | superseded by ADR-MMM
- Date: YYYY-MM-DD

## Context
## Decision
## Consequences
```

## Planned series (M0.0, engineering plan §23)

- [ ] ADR-001 Terminal ontology
- [ ] ADR-002 Identity and device keys
- [ ] ADR-003 Deterministic serialization
- [ ] ADR-004 Event identity
- [ ] ADR-005 Group encryption
- [ ] ADR-006 Local storage
- [ ] ADR-007 Transport boundary
- [ ] ADR-008 Claims and honesty model
- [ ] ADR-009 Schema evolution
- [ ] ADR-010 Deletion semantics

Acceptance for M0.0: no core entity has two conflicting definitions.
