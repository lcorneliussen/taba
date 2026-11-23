-------------------------- MODULE ForgeCacheOrder --------------------------
(***************************************************************************)
(* Write ordering of one forge cache entry in the status collector         *)
(* (internal/wtc). How to run and read it:                                 *)
(* docs/formal/verify-forge-cache-order/SKILL.md.                          *)
(*                                                                         *)
(* The entry is a "pr" or a "build" cache entry. Every production writer   *)
(* publishes it through statusWriteForgeEntrySinceObservedContext          *)
(* (status_cache.go): check the "since" entry (the receipt), retain a      *)
(* confirmed merge in the "merge" entry, rename the data file into place,  *)
(* then rename the receipt into place. Callers hold the entry lock         *)
(* (statusLockCacheContext, an flock on <entry path>.lock) around that     *)
(* call.                                                                   *)
(*                                                                         *)
(* A build entry follows the same protocol without the merge rules, which  *)
(* status_cache.go applies only to kind "pr". Every build-entry behavior   *)
(* is a behavior of this model in which no response confirms a merge.      *)
(*                                                                         *)
(* Time is a counter. A request's start stamp is the time.Now() taken just *)
(* before its fetch. Stamps increase in real-time order.                   *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
    ListWriters,     \* publish a response fetched before taking the entry lock
    LockedWriters,   \* fetch while holding the entry lock
    MaxRequests,     \* bound: forge requests started in one behavior
    SinceGuard,      \* the receipt check and receipt write (9274035 and later)
    MergeRules,      \* the retained-merge rules (46b48d3 and later)
    LockFallthrough, \* a writer whose lock attempt fails still publishes (before 4764f64)
    ReceiptMayFail,  \* the receipt after a publication can be lost
    None             \* a model value: no response, no lock holder

ASSUME ListWriters \cap LockedWriters = {}
ASSUME MaxRequests \in Nat
ASSUME {SinceGuard, MergeRules, LockFallthrough, ReceiptMayFail} \subseteq BOOLEAN

Writers == ListWriters \cup LockedWriters
States == {"open", "merged"}
Response == [start : 0..MaxRequests, state : States]

VARIABLES
    clock,    \* start stamp of the next forge request
    pc,       \* where each writer is on its write path
    resp,     \* the response each writer holds
    holder,   \* the writer holding the entry lock
    content,  \* the response in the entry's data file
    since,    \* the receipt: start stamp written after the last publication
    retained, \* the "merge" entry holds a confirmed merge
    replaced  \* history: the response that the last publication replaced

vars == <<clock, pc, resp, holder, content, since, retained, replaced>>

TypeOK ==
    /\ clock \in 1..(MaxRequests + 1)
    /\ pc \in [Writers -> {"idle", "fetched", "locked", "ready", "merging",
                           "passed", "published", "done"}]
    /\ resp \in [Writers -> Response \cup {None}]
    /\ holder \in Writers \cup {None}
    /\ content \in Response
    /\ since \in 0..MaxRequests
    /\ retained \in BOOLEAN
    /\ replaced \in Response \cup {None}

\* The entry already holds an open response from request 0, and its receipt.
Init ==
    /\ clock = 1
    /\ pc = [w \in Writers |-> "idle"]
    /\ resp = [w \in Writers |-> None]
    /\ holder = None
    /\ content = [start |-> 0, state |-> "open"]
    /\ since = 0
    /\ retained = FALSE
    /\ replaced = None

Fetch(w) ==
    /\ clock <= MaxRequests
    /\ \E s \in States : resp' = [resp EXCEPT ![w] = [start |-> clock, state |-> s]]
    /\ clock' = clock + 1

(***************************************************************************)
(* A list writer's request runs before it takes the entry lock. For a pr   *)
(* entry: the open list (statusFetchGHOpenListContext,                     *)
(* statusFetchBBOpenListContext in status_open_list.go, item.Started) that *)
(* statusEnrichListedContext (status_enlisted.go) later publishes through  *)
(* statusDetailFromOpenItemContext. For a build entry: the recent          *)
(* pipelines page of statusBBBuildContext (status_build_forge.go) that     *)
(* statusSaveLivePipelinePageContext publishes for each branch. The list   *)
(* can come from the cache, so its start can be arbitrarily old.           *)
(***************************************************************************)
ListFetch(w) ==
    /\ w \in ListWriters
    /\ pc[w] = "idle"
    /\ Fetch(w)
    /\ pc' = [pc EXCEPT ![w] = "fetched"]
    /\ UNCHANGED <<holder, content, since, retained, replaced>>

LockTarget(w) == IF w \in ListWriters THEN "ready" ELSE "locked"
CanLock(w) ==
    \/ w \in ListWriters /\ pc[w] = "fetched"
    \/ w \in LockedWriters /\ pc[w] = "idle" /\ clock <= MaxRequests

(***************************************************************************)
(* statusLockCacheContext (status_forge_budget.go) on the entry path, in   *)
(* statusEnrichListedContext, statusSaveLivePipelinePageContext and every  *)
(* locked writer. Since 4764f64 a failed lock ends the write path.         *)
(***************************************************************************)
Lock(w) ==
    /\ CanLock(w)
    /\ holder = None
    /\ holder' = w
    /\ pc' = [pc EXCEPT ![w] = LockTarget(w)]
    /\ UNCHANGED <<clock, resp, content, since, retained, replaced>>

(***************************************************************************)
(* Before 4764f64: unlock, lockErr := statusLockCache(...); if lockErr ==  *)
(* nil { defer unlock() }. A writer whose lock failed went on without it.  *)
(***************************************************************************)
LockFails(w) ==
    /\ LockFallthrough
    /\ CanLock(w)
    /\ pc' = [pc EXCEPT ![w] = LockTarget(w)]
    /\ UNCHANGED <<clock, resp, holder, content, since, retained, replaced>>

(***************************************************************************)
(* A locked writer starts its request after it takes the entry lock:       *)
(* statusFetchSharedAdmissionContext (status_forge_budget.go: pr entries   *)
(* from statusViewPRWithLiveContext, build entries from                    *)
(* statusSingleFlightContext), statusViewPRWithLiveContext with            *)
(* bypassCache (status_enlisted.go), statusRefreshRecord,                  *)
(* statusConfirmFullPRLockedContext (the full refresh) and                 *)
(* statusConfirmFullBuildContext (confirm-full-build) in                   *)
(* status_full_refresh.go, and prIdentityOrdinary                          *)
(* (pr_identity_schedule.go).                                              *)
(***************************************************************************)
LockedFetch(w) ==
    /\ w \in LockedWriters
    /\ pc[w] = "locked"
    /\ Fetch(w)
    /\ pc' = [pc EXCEPT ![w] = "ready"]
    /\ UNCHANGED <<holder, content, since, retained, replaced>>

(***************************************************************************)
(* The same locked writers when admission refuses the request              *)
(* (statusWithForgeAdmissionContext, statusTakeForegroundToken) or the     *)
(* fetch fails: the write path ends without a write. Enabled only once the *)
(* request bound is spent. Before that, it reaches no entry state that a   *)
(* writer which never took the lock does not reach.                        *)
(***************************************************************************)
Refused(w) ==
    /\ w \in LockedWriters
    /\ pc[w] = "locked"
    /\ clock > MaxRequests
    /\ pc' = [pc EXCEPT ![w] = "done"]
    /\ UNCHANGED <<clock, resp, holder, content, since, retained, replaced>>

\* statusRetainedMergeContext (status_freshness.go): a confirmed merge is
\* known from the "merge" entry or from a MERGED pr entry.
Known == retained \/ content.state = "merged"

(***************************************************************************)
(* statusWriteForgeEntrySinceObservedContext, status_cache.go:202-223.     *)
(* With MergeRules, a known merge rejects a non-merged response, and the   *)
(* first confirmed merge (promotesMerge) skips the receipt check. With     *)
(* SinceGuard, a receipt from a later request rejects the response         *)
(* (saved.After(started)). Before 9274035, writers called                  *)
(* statusWriteForgeCache or statusWriteForgeEntry with no check.           *)
(***************************************************************************)
Check(w) ==
    /\ pc[w] = "ready"
    /\ LET r == resp[w]
           isMerge == MergeRules /\ r.state = "merged"
           promotesMerge == isMerge /\ ~Known
           mergeReject == MergeRules /\ Known /\ r.state # "merged"
           sinceReject == SinceGuard /\ since > r.start /\ ~promotesMerge
       IN pc' = [pc EXCEPT ![w] =
                    IF mergeReject \/ sinceReject THEN "done"
                    ELSE IF isMerge THEN "merging"
                    ELSE "passed"]
    /\ UNCHANGED <<clock, resp, holder, content, since, retained, replaced>>

\* statusRetainMergeObservationContext (status_cache.go:226-235) writes the
\* "merge" entry under that entry's own lock, before the pr entry.
RetainMerge(w) ==
    /\ pc[w] = "merging"
    /\ retained' = TRUE
    /\ pc' = [pc EXCEPT ![w] = "passed"]
    /\ UNCHANGED <<clock, resp, holder, content, since, replaced>>

\* statusWriteForgeEntryObservedContext renames the data file into place
\* (status_cache.go:239).
Publish(w) ==
    /\ pc[w] = "passed"
    /\ content' = resp[w]
    /\ replaced' = content
    /\ pc' = [pc EXCEPT ![w] = IF SinceGuard THEN "published" ELSE "done"]
    /\ UNCHANGED <<clock, resp, holder, since, retained>>

(***************************************************************************)
(* The receipt: statusWriteForgeEntryObservedContext renames the request's *)
(* start stamp into the "since" entry (status_cache.go:245). It runs after *)
(* cancellation (cc0226a) and after a changed credential selection (the    *)
(* kind != "since" exemption in statusWriteForgeEntryObservedContext).     *)
(***************************************************************************)
Receipt(w) ==
    /\ pc[w] = "published"
    /\ since' = resp[w].start
    /\ pc' = [pc EXCEPT ![w] = "done"]
    /\ UNCHANGED <<clock, resp, holder, content, retained, replaced>>

(***************************************************************************)
(* The receipt is lost: the process dies between the two renames, and the  *)
(* kernel releases its flock, or the receipt write fails and               *)
(* status_cache.go:245 ignores the result.                                 *)
(***************************************************************************)
LoseReceipt(w) ==
    /\ ReceiptMayFail
    /\ pc[w] = "published"
    /\ pc' = [pc EXCEPT ![w] = "done"]
    /\ UNCHANGED <<clock, resp, holder, content, since, retained, replaced>>

\* The deferred unlock() that statusLockCacheContext returned.
Unlock(w) ==
    /\ pc[w] = "done"
    /\ holder' = IF holder = w THEN None ELSE holder
    /\ pc' = [pc EXCEPT ![w] = "idle"]
    /\ resp' = [resp EXCEPT ![w] = None]
    /\ UNCHANGED <<clock, content, since, retained, replaced>>

\* Not code: the end state once every request is made and every writer rests.
Finished ==
    /\ clock > MaxRequests
    /\ \A w \in Writers : pc[w] = "idle"
    /\ UNCHANGED vars

Next ==
    \/ \E w \in Writers :
        \/ ListFetch(w)
        \/ Lock(w)
        \/ LockFails(w)
        \/ LockedFetch(w)
        \/ Refused(w)
        \/ Check(w)
        \/ RetainMerge(w)
        \/ Publish(w)
        \/ Receipt(w)
        \/ LoseReceipt(w)
        \/ Unlock(w)
    \/ Finished

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* The property. A publication never replaces the response of a request    *)
(* that started later, except that a confirmed merge replaces a response   *)
(* that is not a merge (the comment on statusWriteForgeEntrySince,         *)
(* status_cache.go:160-164).                                               *)
(***************************************************************************)
NoEarlierOverwrite ==
    \/ replaced = None
    \/ content.start >= replaced.start
    \/ content.state = "merged" /\ replaced.state # "merged"

=============================================================================
