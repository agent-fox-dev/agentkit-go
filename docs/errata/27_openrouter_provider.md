# Erratum: spec 27 `openrouter_provider`

The places where the delivered code and its tests differ from the text of
`.specs/27_openrouter_provider`, and why. PRD 14 and the spec are not edited. In
each case the test checks what the code delivers.

## `provider/repair.go` is back, and TS-09-1 no longer pins its absence (27-REQ-4.1 to 4.3, task 1)

Spec 27's task 1 creates `provider/repair.go` and `provider/repair_test.go` for
the shared tool-pairing repair. Spec 09's `TestDeletedPackagesAbsent_TS09_1`
asserted that both files do not exist: they were the multi-wire repair PRD 9
deleted. The spec does not name that pin, and `make check` failed on it
(`provider/repair.go still exists`) as soon as the repair landed.

The pin is restated, not bypassed: the two names left the absence list in
`repository_cut_test.go` (`TestDeletedPackagesAbsent_TS09_1`), with a comment
that points here, and every other entry stays. The new file is not the old one.
It imports only `core`, repairs tool pairing alone and never touches thinking
(`provider/repair.go`, `RepairToolPairing`), where the deleted repair handled
every vendor's thinking, signatures and ids.

## Rule 2 of the repair is applied by position (27-REQ-4.1, 27-REQ-4.3)

27-REQ-4.1 drops "every tool result whose call id is not in a kept assistant
message". 27-REQ-4.3 requires that, for any transcript, every call of a kept
assistant message is followed by exactly one result with its id before the next
non-result message. Read literally, the two disagree on a result that answers a
call of an earlier kept turn but sits after some other message, and on a second
result for one call: rule 2 keeps both, and the output then has a tool message
the wire cannot pair.

`RepairToolPairing` keeps a result only in the run of results that directly
follows the assistant message that made the call, and only the first result for
each call. A result anywhere else, or a second one, is dropped and counted in
`DroppedResults` (`provider/repair.go:92` for a result that follows no kept
assistant message, `provider/repair.go:113` and `:124` inside a run). For a
transcript the loop builds, where results directly follow their turn, this is
the same as the literal rule. `TestRepairToolPairingProperty_TS27_19`
(`provider/repair_test.go:218`) generates repeated, shuffled, missing and made-up
results and checks the 27-REQ-4.3 property and the fixed point over 500
transcripts.
