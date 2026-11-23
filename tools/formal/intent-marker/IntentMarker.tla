---------------------------- MODULE IntentMarker ----------------------------
(***************************************************************************)
(* One side effect that must take effect at most once, and the runs that   *)
(* try to achieve it. How to run and read it:                              *)
(* docs/formal/verify-intent-marker/SKILL.md.                              *)
(*                                                                         *)
(* A run is one invocation of a command: review post creating the review   *)
(* comment (internal/wtc/review_post.go), or open starting the collection  *)
(* agent (cmd/taba/open_collection.go). Runs overlap, crash at any step,   *)
(* and run again later. Each one goes through retry.Intent.Do              *)
(* (internal/retry/retry.go): create the marker file exclusively, ask Done *)
(* whether the intent is already achieved, attempt the effect, then settle *)
(* by its outcome.                                                         *)
(*                                                                         *)
(* The receiver (GitHub, Bitbucket, herdr) applies every request it        *)
(* processes, or refuses it as busy. A reply can be lost: the run then     *)
(* sees an unknown outcome while its request may still be processed.       *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
    Runs,             \* invocations of one command for one intent
    MaxAttempts,      \* attempts one run makes after busy replies
    UseMarker,        \* the exclusive marker file (22001b7 and later)
    RecheckUnderMarker, \* Intent.Done asked while holding the marker (#498)
    RecordBeforeRelease, \* the result is saved before the marker goes
    RetryUnknown,     \* an unknown outcome is attempted again (before 128bbe8)
    ResultAtReceiver  \* the receiver shows the result (a pane's agent), not a local file

ASSUME MaxAttempts \in Nat \ {0}
ASSUME {UseMarker, RecheckUnderMarker, RecordBeforeRelease, RetryUnknown,
        ResultAtReceiver} \subseteq BOOLEAN

VARIABLES
    pc,       \* where each run is
    reply,    \* what each run learned from its last attempt
    attempts, \* attempts each run has made
    inflight, \* requests (run, attempt) the receiver has not processed yet
    applied,  \* times the receiver applied the effect
    marker,   \* the marker file exists
    record    \* the result is saved locally (comment.id)

vars == <<pc, reply, attempts, inflight, applied, marker, record>>

PCs == {"check", "claim", "recheck", "send", "wait", "record", "release",
        "done", "stopped", "crashed"}

TypeOK ==
    /\ pc \in [Runs -> PCs]
    /\ reply \in [Runs -> {"none", "applied", "busy", "unknown"}]
    /\ attempts \in [Runs -> 0..MaxAttempts]
    /\ inflight \subseteq Runs \X (1..MaxAttempts)
    /\ applied \in Nat
    /\ marker \in BOOLEAN
    /\ record \in BOOLEAN

\* What a run reads to learn that the intent is already achieved: comment.id,
\* or the pane's foreground process.
Achieved == IF ResultAtReceiver THEN applied > 0 ELSE record

Init ==
    /\ pc = [r \in Runs |-> "check"]
    /\ reply = [r \in Runs |-> "none"]
    /\ attempts = [r \in Runs |-> 0]
    /\ inflight = {}
    /\ applied = 0
    /\ marker = FALSE
    /\ record = FALSE

Goto(r, where) == pc' = [pc EXCEPT ![r] = where]

\* Before any marker: review post reads comment.id; open reads the pane and
\* leaves a live one alone.
Check(r) ==
    /\ pc[r] = "check"
    /\ Goto(r, IF Achieved THEN "done" ELSE "claim")
    /\ UNCHANGED <<reply, attempts, inflight, applied, marker, record>>

\* os.OpenFile(Marker, O_CREATE|O_EXCL): one atomic step. An existing marker
\* ends the run with ErrUnresolved.
Claim(r) ==
    /\ pc[r] = "claim"
    /\ IF ~UseMarker THEN Goto(r, "send") /\ UNCHANGED marker
       ELSE IF marker THEN Goto(r, "stopped") /\ UNCHANGED marker
       ELSE marker' = TRUE /\ Goto(r, "recheck")
    /\ UNCHANGED <<reply, attempts, inflight, applied, record>>

\* Intent.Done. ErrDone removes the marker and sends nothing.
Recheck(r) ==
    /\ pc[r] = "recheck"
    /\ IF RecheckUnderMarker /\ Achieved
       THEN Goto(r, "done") /\ marker' = FALSE
       ELSE Goto(r, "send") /\ UNCHANGED marker
    /\ UNCHANGED <<reply, attempts, inflight, applied, record>>

\* The effect: herdr agent start, or the gh/Bitbucket comment POST.
Send(r) ==
    /\ pc[r] = "send"
    /\ attempts[r] < MaxAttempts
    /\ attempts' = [attempts EXCEPT ![r] = @ + 1]
    /\ inflight' = inflight \cup {<<r, attempts[r] + 1>>}
    /\ reply' = [reply EXCEPT ![r] = "none"]
    /\ Goto(r, "wait")
    /\ UNCHANGED <<applied, marker, record>>

\* The receiver processes a request, whether or not its run still waits for
\* it. Only the run's current attempt can receive the reply.
Process(r) ==
    \E a \in 1..MaxAttempts :
        /\ <<r, a>> \in inflight
        /\ inflight' = inflight \ {<<r, a>>}
        /\ \E result \in {"applied", "busy"} :
            /\ applied' = IF result = "applied" THEN applied + 1 ELSE applied
            /\ reply' = IF pc[r] = "wait" /\ reply[r] = "none" /\ a = attempts[r]
                        THEN [reply EXCEPT ![r] = result] ELSE reply
        /\ UNCHANGED <<pc, attempts, marker, record>>

\* A lost reply, a timeout, a transport error, agent_not_ready that the
\* reconciliation could not verify: the run cannot tell what happened.
LoseReply(r) ==
    /\ pc[r] = "wait"
    /\ reply[r] = "none"
    /\ reply' = [reply EXCEPT ![r] = "unknown"]
    /\ UNCHANGED <<pc, attempts, inflight, applied, marker, record>>

\* Do's switch on the outcome. Busy is attempted again while the policy
\* allows, still holding the marker; then the marker goes. Unknown keeps it.
Settle(r) ==
    /\ pc[r] = "wait"
    /\ reply[r] # "none"
    /\ CASE reply[r] = "applied" ->
                Goto(r, IF RecordBeforeRelease THEN "record" ELSE "release")
                /\ UNCHANGED marker
         [] reply[r] = "busy" /\ attempts[r] < MaxAttempts ->
                Goto(r, "send") /\ UNCHANGED marker
         [] reply[r] = "busy" ->
                Goto(r, "done") /\ marker' = FALSE
         [] reply[r] = "unknown" /\ RetryUnknown /\ attempts[r] < MaxAttempts ->
                Goto(r, "send") /\ UNCHANGED marker
         [] OTHER ->
                Goto(r, "stopped") /\ UNCHANGED marker
    /\ UNCHANGED <<reply, attempts, inflight, applied, record>>

\* Intent.Record: write comment.id. Open records nothing: the pane shows it.
Record(r) ==
    /\ pc[r] = "record"
    /\ record' = TRUE
    /\ Goto(r, IF RecordBeforeRelease THEN "release" ELSE "done")
    /\ UNCHANGED <<reply, attempts, inflight, applied, marker>>

Release(r) ==
    /\ pc[r] = "release"
    /\ marker' = FALSE
    /\ Goto(r, IF RecordBeforeRelease THEN "done" ELSE "record")
    /\ UNCHANGED <<reply, attempts, inflight, applied, record>>

\* The process dies between any two steps after it began. Files and the
\* request it sent survive it.
Crash(r) ==
    /\ pc[r] \in {"recheck", "send", "wait", "record", "release"}
    /\ Goto(r, "crashed")
    /\ UNCHANGED <<reply, attempts, inflight, applied, marker, record>>

Next ==
    \E r \in Runs :
        Check(r) \/ Claim(r) \/ Recheck(r) \/ Send(r) \/ Process(r)
        \/ LoseReply(r) \/ Settle(r) \/ Record(r) \/ Release(r) \/ Crash(r)

\* Every run has ended and every request was processed.
Quiet ==
    /\ \A r \in Runs : pc[r] \in {"done", "stopped", "crashed"}
    /\ inflight = {}
    /\ UNCHANGED vars

Spec == Init /\ [][Next \/ Quiet]_vars

\* The property: the receiver applies the effect at most once, however the
\* runs overlap, crash, lose replies and run again.
AtMostOnce == applied <= 1
=============================================================================
