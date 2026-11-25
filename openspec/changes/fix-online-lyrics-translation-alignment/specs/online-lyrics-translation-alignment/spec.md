# Spec Delta

## Purpose

Defines timestamp-based alignment behavior when pairing synced online lyrics (such as NetEase LRC) with source-provided synced translations (such as NetEase `tlyric`), including empty clear-line handling, tolerance and nearest-line ownership boundaries, same-timestamp stability, and unsynced fallback guards.

## ADDED Requirements

### Requirement: Empty timestamped clear-lines SHALL NOT shadow translated lyric lines
When aligning synced source translations to synced original lyrics, blank or whitespace-only entries in the translation LRC SHALL be excluded from alignment candidates, and blank or whitespace-only entries in the original lyrics SHALL remain untranslated (`""`) without consuming any translation entry.

#### Scenario: Clear-line placed 1ms before a translated lyric line
- **WHEN** the original LRC and translation LRC both contain an empty timestamped line `[00:38.523]` immediately followed by `[00:38.524]` containing lyric text and its translation
- **THEN** the aligned output leaves the `[00:38.523]` entry empty and maps the `[00:38.524]` translation onto the `[00:38.524]` original lyric line

#### Scenario: Translation LRC omits clear-lines present in original LRC
- **WHEN** the original LRC contains empty timestamped lines between non-empty verses while the translation LRC contains only non-empty translated lines
- **THEN** every empty original line receives an empty string in the aligned output and does not consume any adjacent translation line

### Requirement: Translation alignment SHALL select the closest unconsumed match without stealing across neighboring lines
Each non-empty original lyric line SHALL be matched monotonically against unused non-empty translation lines within a `±250ms` tolerance window, preferring the candidate with the smallest absolute timestamp delta. A translation candidate SHALL NOT be assigned to an original line if another non-empty original line with a distinct timestamp is strictly closer to that translation candidate. Each translation entry SHALL be consumed at most once.

#### Scenario: Minor timestamp drift within tolerance still aligns
- **WHEN** an isolated non-empty original line is at `[00:10.000]` and its corresponding translation line is at `[00:10.200]` with no closer neighboring original line
- **THEN** the aligned output assigns the `[00:10.200]` translation to the `[00:10.000]` original line

#### Scenario: Untranslated line does not steal a neighboring line's translation
- **WHEN** an untranslated original line is at `[00:01.000]` and the next original line is at `[00:01.150]` with a translation line at `[00:01.150]`
- **THEN** the `[00:01.000]` line receives `""` and the `[00:01.150]` line receives the `[00:01.150]` translation

#### Scenario: Trailing leftover translation does not leak onto a later untranslated line
- **WHEN** an original line at `[00:01.000]` matches a translation at `[00:01.000]`, an extra translation line exists at `[00:01.040]`, and the next original line at `[00:01.200]` has no translation
- **THEN** the `[00:01.200]` line receives `""` because `[00:01.040]` is strictly closer to `[00:01.000]`

#### Scenario: Exact match wins over an earlier in-window candidate
- **WHEN** an original line is at `[00:10.000]` and unconsumed translation lines exist at `[00:09.950]` and `[00:10.000]`
- **THEN** the `[00:10.000]` original line receives the `[00:10.000]` translation rather than `[00:09.950]`

#### Scenario: Rapid consecutive lines within tolerance each receive their own translation
- **WHEN** two non-empty original lines appear at `[00:10.000]` and `[00:10.050]` and matching translations appear at `[00:10.000]` and `[00:10.050]`
- **THEN** the first original line receives the `[00:10.000]` translation and the second receives the `[00:10.050]` translation without reusing the first translation

### Requirement: Duplicate timestamps SHALL preserve source ordering and align one-to-one
Synced LRC parsing SHALL sort timed lines stably by timestamp so lines sharing an identical timestamp retain their original relative order. When multiple non-empty original lines share a timestamp, translation alignment SHALL pair them in order against matching translation lines at that timestamp without duplicating a single translation across unmatched siblings.

#### Scenario: Two original lines and two translations share one timestamp
- **WHEN** both original LRC and translation LRC contain two lines at `[00:10.000]` (`A`, `B` and `译A`, `译B`)
- **THEN** parsing preserves `A` before `B` and `译A` before `译B`, and alignment pairs `A -> 译A` and `B -> 译B`

#### Scenario: Two original lines share one timestamp with only one translation
- **WHEN** original LRC contains two lines at `[00:10.000]` (`A`, `B`) and translation LRC contains only one line at `[00:10.000]` (`译A`)
- **THEN** alignment pairs `A -> 译A` and leaves `B` untranslated (`""`)

### Requirement: Unsynced original lyrics SHALL NOT be aligned against source translation LRC
When resolving track lyrics translation via `POST /api/v1/tracks/{id}/lyrics/translation`, the server SHALL only attempt timestamp-based source translation alignment when the parsed original lyrics are time-synced. If the original lyrics are unsynced, the server SHALL skip source translation alignment and fall through to the normal AI translation or disabled response path.

#### Scenario: Unsynced original lyrics with synced source translation stored
- **WHEN** a track's resolved original lyrics contain no timestamps (`synced=false`) while a synced `tlyric` is present and Simplified Chinese translation is requested
- **THEN** the endpoint does not align `tlyric` timestamps onto `0ms` plain lines and instead falls back to AI translation (or returns `translation_disabled` when AI translation is off)
