# Spec Delta: composite-credit-split

## Purpose

Defines end-to-end composite artist credit splitting across performing, backstage creator, and album artist roles, durable persistence across library rescans, AI tiered autonomy with one-click rollback, single-artist false-positive exemption, and batch/inline review workbench interactions.

## ADDED Requirements

### Requirement: Composite credit split updates all track roles, credit sources, and album artists atomically
When a composite artist credit is split into target individual artists (manually or via AI), the system SHALL replace every reference to that composite artist across primary track artists, non-primary track credit roles (including composer, lyricist, arranger, producer, and instrument/involved-people roles), structured track credit source records, and album artists, deduplicating canonical target artist identities while preserving credit ordering.

#### Scenario: Splitting a backstage-only composite credit
- **WHEN** an artist such as `"ACE, 亀岡夏海"` is referenced only in non-primary track roles (e.g., `composer` on 15 tracks) and the split `["ACE", "亀岡夏海"]` is applied
- **THEN** all 15 tracks replace the composite artist with `"ACE"` and `"亀岡夏海"` in their original `composer` role order, the composite artist has zero remaining track or album references, its pending review state is cleared, and the response reports the actual affected track count

#### Scenario: Splitting a mixed-role and album-artist composite credit
- **WHEN** a composite artist appears simultaneously as a track `primary` performer, a track `lyricist`, and an `album_artist`
- **THEN** applying the split updates primary track credits through the primary-artist correction mechanism, replaces the non-primary track credit and album-artist associations, clears the composite artist's pending candidates and composite-skip state, and removes the artist from the composite review tab

#### Scenario: Preventing resurrected credits from remote or manual credit sources
- **WHEN** a composite artist is present in both track credits and non-file-tag credit source records
- **THEN** the split updates the credit source records before removing the composite artist's track credit rows so database triggers do not reinsert the composite artist

#### Scenario: Canonical deduplication and invalid split rejection
- **WHEN** target artist names include merged aliases resolving to an already-attached canonical artist on a track or album, or the split resolves to zero valid targets or includes the composite artist itself
- **THEN** valid splits deduplicate canonical target artist IDs without failing on duplicate constraints, and invalid splits (empty targets or self-referential targets) are rejected with an explicit error message

#### Scenario: Zero-reference composite artist feedback
- **WHEN** a user triggers a split for a composite artist that has no remaining track or album references
- **THEN** the system does not report a misleading success count of zero updated tracks and instead informs the user clearly while cleaning stale review state if applicable

### Requirement: Applied composite splits persist across library rescans and orphan cleanup
The system SHALL persist active composite-artist split rules so subsequent incremental or full library rescans automatically expand the composite artist into its canonical target artists across track primary credits, non-primary track credits, and album artists, and orphan artist cleanup SHALL NOT delete artists referenced by active split rules or rollback snapshots.

#### Scenario: Full library rescan preserves backstage and album-artist splits
- **WHEN** a composite artist has been split across `composer` and `album_artists` and a full library rescan re-imports the unmodified audio files
- **THEN** the re-imported tracks and albums automatically retain the split target artists instead of reverting to the unsplit composite artist

#### Scenario: Orphan cleanup protects active split rules and rollback targets
- **WHEN** a composite artist has zero remaining track and album references after splitting and orphan cleanup executes
- **THEN** the composite artist record and target artist records referenced by active split rules remain intact so rescan replay and one-click rollback continue to work

### Requirement: AI composite credit review supports tiered autonomy with one-click rollback
The system SHALL allow administrators to configure composite credit splitting in AI settings with `off`, `recommend`, and `auto` modes. In `auto` mode, when AI classifies an artist as composite with confidence at least 92 and at least two distinct canonical target artists excluding the composite artist itself, the system SHALL automatically apply the split (creating new individual artist records when needed), record a rollback snapshot, and expose one-click revert in the review workbench.

#### Scenario: High-confidence composite credit auto-splits including new individual artists
- **WHEN** AI composite credit mode is set to `auto` and AI evaluates `"Alice Duport-Percier, Lorien Testard"` as composite with confidence 100 and parts `["Alice Duport-Percier", "Lorien Testard"]`
- **THEN** the system automatically creates any missing individual artist records, splits all associated track and album credits, marks the AI decision as auto-applied with a rollback snapshot, and lists the action in recent AI auto-applied records

#### Scenario: Low-confidence or single-part AI decision stays in manual review
- **WHEN** AI composite credit mode is set to `auto` and AI returns confidence below 92, `isComposite=false`, or fewer than two distinct valid parts
- **THEN** the system does not auto-apply the split and retains the artist in the composite review queue with the AI recommendation displayed

#### Scenario: Cached high-confidence recommendation upgrades without re-calling the model
- **WHEN** an artist already has an unapplied, non-reverted AI credit split recommendation with confidence at least 92 matching the current artist fingerprint, and an AI review run executes while composite credit mode is `auto`
- **THEN** the system applies the existing high-confidence split directly without making a redundant LLM request

#### Scenario: Inline AI auto-split during artist scan is not overwritten by composite skip state
- **WHEN** automatic artist matching encounters a composite artist and inline AI auto-splits it successfully
- **THEN** the scan records the item as resolved/applied and does not write a `skipped_composite` state that would re-surface the artist in the composite tab

#### Scenario: Reverting an AI auto-applied composite split restores prior state
- **WHEN** an administrator clicks revert on an AI auto-applied composite credit split decision
- **THEN** the split rule is marked reverted, all affected primary track corrections, non-primary track credits, credit source records, and album-artist associations are restored from the snapshot, and the composite artist returns to the composite review queue

### Requirement: False-positive composite artists can be exempted as single artists
The system SHALL allow administrators to confirm that a flagged artist whose name contains separator characters (such as `"Simon & Garfunkel"` or `"Every Little Thing"`) is a legitimate single artist or unit, exempting it from composite-credit blocking across review queues, artist matching scans, identity binding, and merge conflict checks.

#### Scenario: Exempting an AI-flagged non-composite unit from the composite tab
- **WHEN** an administrator clicks the confirm-single-artist action on a composite tab card
- **THEN** the artist is recorded in composite exemptions, its composite-skip state is cleared, it disappears from the composite tab, and subsequent incremental artist scans or manual matches allow MusicBrainz and Last.fm matching and identity confirmation

### Requirement: Composite review workbench supports batch high-confidence apply and inline part editing
The composite review tab (`/admin/matches?tab=credits`) SHALL display each artist's role breakdown alongside album and track counts, allow inline editing of the split artist list before applying a single card, provide a single-artist exemption action, and provide a batch action to apply all pending high-confidence (≥92%) AI split recommendations without re-running AI inference.

#### Scenario: Editing AI-suggested parts before applying
- **WHEN** an administrator modifies the suggested split names inline on a composite credit card and submits
- **THEN** the system applies the administrator-edited artist list rather than the unmodified AI suggestion and displays the updated track and album counts

#### Scenario: Batch applying existing high-confidence recommendations
- **WHEN** multiple pending composite artists already have unapplied AI split recommendations with confidence at least 92 and the administrator triggers batch high-confidence apply
- **THEN** the system applies all eligible splits in one action, skips any invalid or conflicting items safely without aborting the rest, and reports a summary of applied artists, updated tracks/albums, and any skipped count
