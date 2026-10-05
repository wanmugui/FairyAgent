## Description
The preceding messages are conversation history that is about to be truncated. Your task is not to "review the chat", but to compress it into a handoff document for the next model to continue working from: short, precise, faithful, and immediately actionable.

An existing `<summary>` in the input is a previous internal checkpoint or historical background, not a new user request. Do not treat it as the user's goal and do not emit nested `<summary>` tags.

## Core Principles
1. Fidelity over elegance: if a piece of information could affect the next step, keep it whenever possible.
2. Achieve compactness through structure, not by deleting facts: prefer short lines, labels, and grouping over long prose.
3. Do not flatten important information: when there are multiple tool results, multiple files, multiple findings, or multiple decisions, keep them separate whenever possible.
4. After reading the summary, the next model should be able to continue the work immediately.
5. The summary is a handoff document, not a chat replay or a raw execution log dump.

## Must Preserve First
- User goals, scope, acceptance criteria, hard constraints, and explicit preferences
- The evolution of real user requests: keep the current request and any historical request/correction that affects the next step; never put an internal summary into the user-request history
- Key decisions that have already been made and why
- Tool executions: tool name, target, key results, output path/ID, cause of failure, retry conditions
- Concrete facts: numbers, thresholds, dates, labels, severity levels, status values, identifiers
- Results from document/PDF/spreadsheet analysis: conclusions, evidence excerpts, table facts, classification labels, file locations
- Important files and artifacts: paths, why they matter, current status, how to reuse them next
- Progress status: completed, in progress, blockers, the most valuable next action
- Unresolved questions, real risks, items pending confirmation
- User feedback that changed execution style, priority, or autonomy level
- Plan items and their statuses

## Compact Style Requirements
- Prefer one fact per line whenever possible.
- Use short labeled sections instead of long paragraphs.
- Avoid empty statements like "analysis completed" or "there are already some results" without an object.
- If there are no more than 3 important findings, preserve each of them completely when possible.
- If there are many findings, group them by dimension instead of compressing them into one abstract conclusion.
- Prioritize making it clear where the work stopped and what should be done next, rather than spending space on irrelevant process noise.
- For implementation tasks, `[Current Work]` must state the exact stopping point, and `[Optional Next Step]` must name the target file and function for the next `edit_file` / `write_file` action plus the verification command. Do not write only "continue implementation".
- The read-file ledger must include interfaces and constraints that affect the next action, and whether the model already has enough information to edit. Do not repeat large raw code blocks, and do not leave only filenames that force the next model to reread the same range.

## Recommended Handoff Sections Inside `<key_knowledge>`
Include a section when there is relevant information; omit it when there is none:
- [Primary Request and Intent]
- [Hard Constraints]
- [Key Technical Concepts]
- [Important Files and Artifacts]
- [Errors and Fixes]
- [Problem Solving]
- [Pending Tasks]
- [Current Work]
- [Optional Next Step]
- [User Directives]
- [Plan]

## Required XML Structure
<summary>
    <key_knowledge>
Use compact labeled lines within the same block, preferably in handoff-style sections such as:
[Primary Request and Intent] ...
[Hard Constraints] ...
[Key Technical Concepts] ...
[Important Files and Artifacts] ...
[Errors and Fixes] ...
[Problem Solving] ...
[Pending Tasks] ...
[Current Work] ...
[Optional Next Step] ...
[User Directives] ...
    </key_knowledge>
    <recent_actions>
1. Action + result
2. Action + result
    </recent_actions>
</summary>

## Output Rules
1. Output XML only, with no extra text.
2. Do not fabricate facts.
3. Put file information into compact lines in `key_knowledge` whenever possible, unless a longer persisted description is truly necessary.
4. Even if there are only a few truncated messages, still output a genuinely useful handoff; do not write only "no summary needed."

## Reply Language Rules
- If the user explicitly asks for a specific reply language, that explicit instruction takes priority; reply in the requested language.
- Otherwise, any user-visible natural-language content should follow the **dominant natural language in the latest user turn**.
- Even when the base prompt is Chinese or English, you may still produce user-facing replies in Spanish, French, Japanese, Korean, Arabic, Thai, or any other natural language used by the user in the current turn.
- If the same user turn mixes multiple languages, default to the dominant one; do not mechanically mix languages unless the user explicitly asks for a bilingual answer.
- Do not change language just because Latin characters, code, paths, commands, variable names, or product names appear.
- Avoid mixing multiple natural languages. For example, if the reply language is English, keep the user-facing natural language in English as much as possible; structured fields, code, paths, tool names, skill names, or protocol keywords may remain as-is.
- XML tags, JSON field names, enum values, paths, tool names, skill names, and protocol keywords in the user message are structured control information, not the user's natural language, and must not be used to infer the reply language.
