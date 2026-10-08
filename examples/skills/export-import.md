---

name: agent-knowledge-package
description: Export and import an agent's accumulated knowledge, skills, experience, preferences, and supporting evidence as a portable package that can be transferred between different agent harnesses. Use when migrating an agent, backing up its knowledge, handing over work to another agent, or reconstructing an agent's accumulated experience in a different environment.
-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

# Export/import agent knowledge package

This skill defines a semantic procedure for transferring an agent's accumulated knowledge between different agent harnesses.

The source and target harnesses may have completely different memory, skill, context, and storage mechanisms. Do not assume that another harness understands the source harness's internal formats.

The goal is **semantic portability**, not preservation of the source harness's internal implementation.

## Export

When asked to export agent knowledge, inspect all knowledge sources that are actually available to you.

Look for:

* durable memory;
* project or repository instructions;
* installed skills;
* learned procedures;
* reusable problem-solving patterns;
* confirmed facts and domain knowledge;
* known failures and rejected approaches;
* persistent preferences and constraints relevant to the agent's work;
* important decisions and their rationale;
* provenance or evidence associated with knowledge;
* temporal information such as creation, verification, update, or invalidation;
* previous task outcomes when they contain reusable knowledge.

Do not assume that a source exists merely because this skill mentions it. Export only information you can actually access.

### Do not export

Do not blindly dump:

* raw conversation history;
* complete task trajectories;
* transient task context;
* duplicate knowledge;
* credentials, API keys, tokens, cookies, private keys, or other secrets;
* personal data that is not necessary for the knowledge to remain useful;
* temporary observations that have no reusable value;
* obsolete or explicitly invalidated knowledge unless it is useful as historical evidence;
* internal implementation details of the source harness that have no semantic value.

## Classify knowledge

Every exported item must have a clear semantic type.

Use these types where applicable:

* `fact` — a relatively stable statement about the domain or environment;
* `procedure` — a reusable way of accomplishing something;
* `pattern` — a recurring problem/solution pattern;
* `constraint` — a rule or limitation that should influence future work;
* `preference` — a persistent preference relevant to future decisions;
* `decision` — an important decision together with its rationale;
* `failure` — a known unsuccessful approach and what was learned from it;
* `skill` — a reusable capability that can be represented as an executable or instructional skill;
* `context` — background information required to correctly interpret other knowledge.

Prefer a small number of high-value items over a large collection of weak memories.

## Knowledge representation

Represent each exported item using the following conceptual structure:

```yaml
id: unique-stable-identifier
type: fact | procedure | pattern | constraint | preference | decision | failure | skill | context

title: Short human-readable name

statement: >
  The actual knowledge in a concise, self-contained form.

status: confirmed | probable | provisional | obsolete | invalidated

confidence: 0.0-1.0

scope:
  projects: []
  repositories: []
  technologies: []
  environments: []

provenance:
  - source: task | conversation | document | observation | human | agent
    reference: optional-reference
    description: optional-description

temporal:
  created_at: optional-timestamp
  last_verified_at: optional-timestamp
  invalidated_at: optional-timestamp

evidence:
  - optional-supporting-evidence

related:
  - ids-of-related-knowledge

notes: optional-additional-context
```

Do not invent provenance, timestamps, confidence, or evidence. If they are unavailable, omit them.

## Procedures and skills

When exporting a reusable procedure, separate:

1. the goal;
2. prerequisites;
3. the procedure;
4. important constraints;
5. known failure modes;
6. verification criteria.

If the procedure is sufficiently self-contained to be represented as a skill, export it as a conventional skill directory with a `SKILL.md` and any required supporting files.

Do not convert every memory into a skill.

A skill describes **how to perform something**.

A knowledge item describes **what is known**.

## Portable package

When possible, produce a package with this structure:

```text
agent-knowledge-package/
├── manifest.yaml
├── knowledge/
│   ├── facts.yaml
│   ├── procedures.yaml
│   ├── patterns.yaml
│   ├── constraints.yaml
│   ├── decisions.yaml
│   └── failures.yaml
├── skills/
│   └── <skill-name>/
│       └── SKILL.md
├── evidence/
│   └── ...
└── README.md
```

The exact filesystem format is optional. Semantic content is more important than directory layout.

The `manifest.yaml` should describe:

```yaml
format: agent-knowledge-package
version: 1
created_at: <timestamp-if-known>

source:
  harness: <name-if-known>
  agent: <name-if-known>

contents:
  knowledge: <count>
  skills: <count>
  evidence: <count>
```

Do not invent a harness name or agent identity.

## Export quality checks

Before completing an export:

1. Remove duplicates.
2. Remove secrets and credentials.
3. Remove transient context.
4. Identify obsolete or invalidated knowledge.
5. Ensure every item is understandable without the source harness.
6. Preserve provenance when available.
7. Preserve uncertainty instead of turning uncertain information into facts.
8. Prefer concise generalizations over raw task history.
9. Check that procedures contain enough information to be useful in a different environment.
10. Identify knowledge that depends on source-harness-specific tools or capabilities.

If important information cannot be exported because the source harness does not expose it, state that explicitly.

---

# Import

When asked to import an agent knowledge package, first inspect the package completely enough to understand its structure and semantics.

Do not blindly copy every item into active memory.

## Import classification

For each item determine:

* whether it is still applicable;
* whether it conflicts with existing knowledge;
* whether it is sufficiently supported;
* whether it is scoped to the current project/environment;
* whether it should become durable knowledge;
* whether it should become a skill;
* whether it should remain historical evidence;
* whether it should be rejected.

Use the following general policy:

| Imported item           | Default treatment                         |
| ----------------------- | ----------------------------------------- |
| confirmed reusable fact | durable knowledge                         |
| confirmed procedure     | knowledge or skill                        |
| reusable pattern        | durable knowledge                         |
| project constraint      | project knowledge                         |
| preference              | preserve only if relevant                 |
| decision                | preserve with rationale                   |
| failure                 | preserve if it prevents repeated mistakes |
| provisional claim       | do not promote to confirmed fact          |
| obsolete item           | preserve only as historical context       |
| invalidated item        | do not activate                           |

## Conflict resolution

When imported knowledge conflicts with existing knowledge:

1. do not silently overwrite existing knowledge;
2. compare provenance and verification state;
3. prefer more recent verified evidence when appropriate;
4. consider scope — a project-specific rule may not conflict with a general rule;
5. preserve the conflict if it cannot be resolved;
6. ask the user when the conflict materially affects future work.

Never resolve a factual conflict merely because the imported item is newer.

## Harness adaptation

The target harness may not support the same concepts as the source harness.

Adapt semantics rather than implementation.

For example:

```text
source:
  durable_memory → target durable memory

source:
  SKILL.md → target skill mechanism

source:
  project constraint → target project instructions

source:
  evidence/provenance → target metadata or supporting documentation
```

If the target harness has no equivalent mechanism, preserve the information in the closest durable representation available.

Do not claim that an item has been imported into a mechanism that the target harness does not actually provide.

## Import report

After import, produce a concise report containing:

```text
Imported:
- N knowledge items
- N skills
- N constraints
- N decisions

Adapted:
- ...

Rejected:
- ...

Conflicts requiring attention:
- ...

Information that could not be represented:
- ...
```

The report must distinguish between:

* successfully imported;
* adapted into another representation;
* preserved but inactive;
* rejected;
* unavailable due to target-harness limitations.

## Important principle

The package is a **semantic transfer format**, not a memory dump.

The objective is:

```text
source agent experience
        ↓
semantic extraction
        ↓
portable knowledge package
        ↓
semantic reconstruction
        ↓
target agent experience
```

not:

```text
source memory database
        ↓
copy database
        ↓
target memory database
```

The target agent must be able to understand the imported knowledge even when its memory architecture, model, tools, and harness are completely different from those of the source agent.
