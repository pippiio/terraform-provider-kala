---
page_title: "Internal API reference"
subcategory: ""
description: |-
  The undocumented Kala endpoints this provider writes through, and the
  behaviour observed of each.
---

# Internal API reference

Every write in this provider goes through Kala's **internal app API** — the
surface the web client uses. It is undocumented and unversioned, so this page is
the only record of what was observed, when, and how.

This is not a specification. It is a set of dated observations against one
tenant, and Kala can change any of it without notice.

## Why not the documented API

`webapiv2` was measured before it was rejected. It has **create without update**:

| Endpoint | Result |
|---|---|
| `CreateCustomer`, `CreateCase`, `AddClItems`, `MarkCliDone` | exist |
| `CustomersList` | exists (GET only) |
| `UpdateCustomer`, `UpdateCase`, `ChangeCaseStatus`, `UpdateClItems` | **absent on both GET and POST** |

Absence was checked on both verbs, because ASP.NET MVC also answers 404 for a
verb mismatch — `CustomersList` is the control that proves it, being 404 on POST
and 200 on GET.

A resource whose attributes cannot change in place must declare them all
`RequiresReplace`, turning every edit into destroy-then-create. Kala cannot
delete, so every edit would strand the old record and duplicate it. That is why
the internal API carries the write path.

## Two write models

Confusing them destroys data.

| Entity | Model | Consequence |
|---|---|---|
| Customer, Task | **full-record replace** | an omitted field is **blanked**; callers must read-modify-write |
| Case | **per-field** | omission is safe; each write carries a compare-and-swap value |

## Endpoints

Paths are exact, including trailing slashes and capitalisation — both vary and
both fail silently when wrong.

### Customers

| Endpoint | Method | Keys on | Notes |
|---|---|---|---|
| `/api/AddCustomer/` | POST | — | returns `{"status":"Success","customerId":N}` |
| `/api/EditCustomer/` | POST | `customerId` (int) | **full-record replace** |
| `/api/SetWorktypePricingForCustomer/` | POST | `customerNumber` (**string**) | `worktypePricing` is JSON embedded in a string |

Same entity, two identifier spellings across two endpoints.

### Cases

| Endpoint | Method | Keys on | Notes |
|---|---|---|---|
| `/Api/CreateCase/` | POST | `customerNr` (string) | **capital `Api`**. Returns `{"caseId","caseNumber"}` |
| `/api/RenameCase/` | POST | `caseNr` (string) | carries `previousCaseName` |
| `/api/ChangeCaseAddress/` | POST | `caseNr` | carries `previousAddress` |
| `/api/ChangeCaseZip/` | POST | `caseNr` | carries `previousZip` |
| `/api/RenameCaseCustomerPhoneNumber/` | POST | `caseNr` | carries `oldPhoneNumber` — `old`, not `previous` |
| `/api/ChangeCaseCustomer/` | POST | `caseNr` | `newCustomerId` when assigning, `oldCustomerId` when converting to internal |
| `/api/ArchiveCase/` | **GET** | `caseNr` (query) | a **write performed by GET** |

**`CreateCase` must always send `newCustomer:false`.** Its body carries a full
customer record, and `true` creates a customer as a side effect of creating a
case — a record no configuration declared and that cannot be deleted.

**`ChangeCaseCustomer` must always send `updateCustomerAddress:false`**, which
would otherwise overwrite the case address as a side effect.

**The `previous*` value is validated.** A mismatch is refused with
`Previous case name doesn't match` — deliberate optimistic concurrency, which
makes case writes drift-safe. It arrives as a **5xx** and must not be retried:
it is deterministic. The provider reads before writing so the value is current.

### Tasks (checklist items)

| Endpoint | Method | Keys on | Notes |
|---|---|---|---|
| `/api/AddChecklistItem/` | POST | `caseNr` (string) | returns `{"Id":N,…}` — capital `I` |
| `/api/UpdateChecklistItem/` | POST | `cliId` (int) **and** `caseNr` (string) | **full-record replace** |
| `/Case/GetChecklistItemsPaged/` | POST | `caseId` (**int**) | prefix `/Case/`. The read path |

`description` and `normTime` are accepted on **update only**, so a task
configured with a description takes two calls.

**`MarkCliDone` is deliberately not wrapped.** Completion is work performed by a
person, not configuration.

### Assignment

| Endpoint | Method | Keys on | Notes |
|---|---|---|---|
| `/api/NewJobLinkNoTimeCaseId/` | POST | `workerNr`, `caseId` | returns the link; its `id` is the `jobLinkId` below |
| `/api/UpdateJobLinkChecklist/` | POST | `jobLinkId` | replaces the **whole** `checklistIds` array |
| `/api/RemoveJoblinkChecklistItem/` | **GET** | `caseNr`, `checklistItemId`, `workerNr` | a write by GET; needs no `jobLinkId`. Note `Joblink`, not `JobLink` |

There is **no way to read a job link**. Assignment is read from the task list's
`workersAssigned` collection instead. There is **no way to remove a link**;
destroy detaches every item it covers.

**Unverified:** whether `NewJobLinkNoTimeCaseId` returns an existing link for a
(worker, case) pair or creates a duplicate. The provider verifies the outcome by
read-back rather than trusting either answer.

## Response shapes

A successful write answers in **four** shapes:

- `{"status":"Success"}`
- a bare data object with no status field
- a JSON array
- `{"success":true,…}` — the case field-setters

A **refused** write answers `HTTP 200` with `{"status":"Error","message":"…"}`
or `{"success":false}`. The status line is never evidence that a write landed.
Messages are in Danish and are surfaced verbatim, being the most specific
explanation available.

Server errors arrive as ASP.NET HTML pages whose only useful sentence is the
`<title>`; the client extracts it.

## Things that do not round-trip

- **Task deadlines.** The create response echoes the millisecond value it was
  given, but the list read returns it ~2 ms later. `kala_task` therefore uses
  second precision, on both write and read.
- **`name` on write, `text` on read** in the checklist-item *write responses*.
  The **list** spells it `name`, which is why verification reads the list.

## Identifier table

There is no rule here, only a table.

| Entity | Written as | Read as |
|---|---|---|
| Customer | `customerId` (int) / `customerNumber` (string) | `id`, `number` |
| Case | `caseNr` (**string**) | `caseId` (**int**) |
| Checklist item | `cliId` (int) | `Id` (int) |
| Job link | `jobLinkId` (int) | `id` (int) |
| Employee | `workerNr`, `workerID`, `workerId` | `workerNr` |

## Observation dates

Everything here was observed between **2026-09-07 and 2026-09-09** against a
single-company tenant. Multi-company behaviour is untested: the `kacompany`
header is sent, but no account with several companies was available.
