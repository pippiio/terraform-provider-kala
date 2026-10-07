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
| `/api/GetJobDetailsAdvanced/` | GET | `caseNr` (query) | the per-case read. **64 fields**; see below |

**`GetJobDetailsAdvanced` embeds the checklist items, with their assigned workers.**
Observed 2026-09-25. Alongside `caseId`, `restricted` and `checklistItemsTotal`, it carries
`checklistItems[]`, and each item carries `workersAssigned[]`:

```
workersAssigned[0]: initials, isValidated, name, phone, title, workerImage, workerNr
```

So the case's **assignments** are readable in one call, without touching
`/Case/GetChecklistItemsPaged/` and without pagination — the items are embedded, not paged.
`checklistItemsTotal` against `len(checklistItems)` is the completeness check, and it is
self-describing: the payload states how many items exist.

**Assignment is not access.** Kala keeps a separate access list, found 2026-09-29 by capturing
the UI's access panel:

| Endpoint | Method | Keys on | Notes |
|---|---|---|---|
| `/api/GrantedWorkers/` | GET | `caseNr` (query) | `{"grantedWorkers":[<workerNr>…],"rolesEnabled":<bool>}` — plain numbers, no personal data. Unknown case → HTTP 500 |
| `/api/GrantAccess/` | POST | `caseNr` (body) | `{"workerNumber":N,"caseNr":"…","access":<bool>,"role":[]}` — grant or revoke. A **write**, used by `kala_case_access` (ARCH1.3 #27). Keys on `workerNumber`, not `workerNr`. Response not characterised: every write is verified by reading `GrantedWorkers` back |

The access list governs only a **restricted** case (`restricted` in the case detail); unrestricted
cases observed have an empty list and everyone has access. On a restricted case an employee can be
assigned to a task without being granted access — then they cannot see it. That state was found
live on KA-2 after an access revocation left the assignment in place.

`rolesEnabled` and `role:[]` suggest a per-case role model; `projectRoles` in the case detail was
empty throughout, including on a restricted case. Unexplored.

`workersAssigned` carries personal data — name, phone, title, image. Only `workerNr` may cross
the client boundary (SEC1.5).

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

## Numbers are not integers

**Observed 2026-09-25; fixed 2026-09-27.** `registeredHoursTotal` came back as **`0.25`** — a
quarter hour. The client declared it, and every sibling hour and money field, as Go `int`, and a
decimal in any of them was a **decode failure, not a rounding error** — the read died:

```
json: cannot unmarshal number 0.25 into Go struct field
wireTasksPage.items.0.registeredHoursTotal of type int
```

One fractional value anywhere on a case took down the **entire** read for that case — `kala_tasks`,
`kala_case`, `kala_cases` detail, and the `kala_case` resource's `Read`. It went unnoticed because no
test fixture had ever carried a decimal.

**The rule:** treat every Kala numeric as potentially fractional. Hours are quarter-hours and money
has cents. Only **identifiers and counts** are safe as integers.

| Field group | Declared now |
|---|---|
| `registeredHoursTotal`, `billedHours` (case and task) | `float64` |
| `cost`, `sales`, `result`, `invoiced`, `uninvoiced`, `realised` | `float64` |
| `priceFixed` (read and write) | `*float64` |
| `caseTotalNormTime`, `caseTotalRegisteredHours` (task envelope) | **not declared** — never used, and a decode liability |

**It was not a breaking change.** Earlier notes here and in the track that found the bug said
moving `registered_hours_total` from `Int64` to `Float64` would break the schema. That was wrong:
both are Terraform's single `number` type (`tftypes.Number`), so no configuration can observe the
difference, the generated docs render both as `(Number)`, and state written under the `Int64`
schema reads unchanged under `Float64`. An integral `float64` also marshals without a decimal
point, so the write payload for an existing integer price is byte-identical. Each of those is
pinned by a test.

**Truncating would have been worse than failing:** it reports 0.25 hours as `0`, silently.

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
| Employee | `workerNr` (employee number) on Set\*, ChangeBoss, SetValidated, SetEmailNew, GrantAccess (as `workerNumber`); **`workerID`/`workerId` = Kala's INTERNAL id** on ChangeWorkerDepartment, ChangeLeaderNote, ChangeWorkerName, ChangeDateOfEmployment | `workerNr` and `workerId` — **different values** (employee 23 has id 5). Sending the employee number where the internal id is expected fails with *Sequence contains no elements*, or writes to another worker |

## Observation dates

Most of this was observed between **2026-09-07 and 2026-09-09**; the
`GetJobDetailsAdvanced` shape and the decimal-numeric finding on **2026-09-25**,
both against a single-company tenant. Multi-company behaviour is untested: the `kacompany`
header is sent, but no account with several companies was available.
