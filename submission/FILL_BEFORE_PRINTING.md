# AMASE — What to fill in before 19 September

Every document is complete except the details only you have. Search for the
angle-bracket placeholders in Word (Ctrl+H) and replace them everywhere.

| Placeholder | Appears in | What to put |
|---|---|---|
| `<Roll No>` | Synopsis title page, PPT slide 1 table, report card, paper | Each member's roll number |
| `<Team Member Name>` | Synopsis title page, PPT slide 1 table, report card, paper | Your three team-mates |
| `<Supervisor Name>` | Synopsis title page, PPT slide 1, report card, paper | Your guide's name |
| `<Sec>` | Report card only | Your section |
| Project ID / Group No. | Report card | From the allocation sheet |
| Designation | Synopsis + PPT (currently "Assistant Professor") | Correct it if your guide's designation differs |

## Checklist for the presentation

- [ ] Fill all placeholders in all four files
- [ ] Print the synopsis (spiral bound) and get the guide's signature on the Index page
- [ ] Confirm the roles in the PPT slide-1 table match what your team actually agreed
- [ ] Reporting time 11:20 AM, presentation 11:30 AM – 1:30 PM, formal dress
- [ ] Carry the PPT on a pen drive **and** keep the PDF as a fallback

## Notes on the templates

- The synopsis keeps the department's page setup, styles, title-page layout,
  NIET logo and index table exactly as supplied. Headings are Times New Roman
  18 bold centred, body 16 justified, subheadings 16 bold left, references 12
  left — as the template's own format page specifies.
- Body pages are numbered 1–27 (title page and index are unnumbered front
  matter), so the page numbers in the Index are correct as printed. If you edit
  the text, re-check them.
- The PPT keeps the template's Title, Index, Thank You and Feedback slides and
  their layout. The "Content Format" instruction slide was removed and its spec
  followed instead (Arial 32 centred title, Arial 18 bold subheading, Arial 16
  paragraph, Times New Roman 12 references). If your guide wants that slide kept,
  re-insert it from the original template — nothing else needs changing.
- Ten content slides sit between Index and Thank You, exactly as the notice
  requires, covering Introduction, Problem Definition, Objectives, Literature
  Survey, Research Gap, Methodology (×2), Feasibility, Facilities, and Outcomes.

## Likely panel questions

- **"How is this different from OpenHands or SWE-agent?"** — They build agents;
  this measures what makes agents work. Their results compare whole systems, so
  you cannot tell which design choice earned the gain. AMASE isolates one
  mechanism at a time and attaches a cost to each.
- **"Why not just use SWE-bench?"** — OpenAI retired SWE-bench Verified in 2026.
  Their audit of 138 problems found 59.4% materially defective and documented
  frontier models reproducing reference patches verbatim. That is why the project
  authors its own held-out task set.
- **"Is this too ambitious for one semester?"** — The measurement infrastructure
  is finished in Phase 1, before any experiment. Even if the expensive arms are
  cut, the project still delivers an instrumented, regression-gated harness with
  a published baseline.
- **"Where is the novelty?"** — Not in the agent, which is deliberately minimal.
  In the controlled attribution of effect and cost per harness mechanism, which
  nobody has published, on tasks that are clean by construction.
