# `/payments/*` — reference

APPA's payments service (`appa-payments`, Go module `appa_payments`, "la
pasarela"). Consumed directly from the browser by the APPA checkout front
(`pay.appasalud.com`, `pay-appasalud.web.app`, `localhost:5173` in dev), no
proxy — and, for the recurring domiciliación charge, by Shopify itself through
`POST /webhook/order/created`.

Shopify store: `appacare.myshopify.com` (Admin GraphQL). All bolívar amounts are
derived as `usd * BCVTasa`; the rate is cached per day in memory (`pkg/bcv`).
Server timezone is pinned to `America/Caracas`.

> The cart-keyed counterpart (`?cartId=` entry path, no order yet) lives in
> [`docs/cart_payments.md`](cart_payments.md). Everything below assumes a
> Shopify **Order** or **DraftOrder** already exists.

## `typeOrder`

Every endpoint that charges accepts an optional `typeOrder`
(`internal/models/payments.go`):

| Value | Effect | Default |
| --- | --- | --- |
| *(absent)* / `"Complete"` | The id is a real Shopify **Order**. Paid via `markOrderAsPaid`. | ✅ |
| `"Draft"` | The id is a **DraftOrder**. This service completes it into a real order — see [Who completes what](#who-completes-what). | |
| `"Cart"` | **Refused.** `GetChargeableByID` returns `cart order type is not supported`. Cart lives on `/cart-payments/*`, which authenticates by signed quote, not by Shopify lookup. | |

Absent is exactly the old behavior — zero change for a caller that doesn't know
about the field.

## Endpoints

| Endpoint | Method | Identifies the sale via | `typeOrder` | Rail |
| --- | --- | --- | --- | --- |
| `/payments/bcv-tasa` | GET | — | — | (all) |
| `/payments/generate-otp` | POST | `orderId` (body) | ✅ | Débito inmediato, step 1 |
| `/payments/validate-direct-debit` | POST | `orderId` (body) | ✅ | Débito inmediato, step 2 (moves the money) |
| `/payments/validate-mobile-payment` | POST | `orderId` (body) | ✅ | Pago Móvil |
| `/payments/validate-mobile-payment-manual` | POST (multipart) | `orderId` (form) | ✅ (form field) | Pago Móvil, manual receipt |
| `/payments/direct-debit-account` | POST | `orderId` (body) | ✅ | Domiciliación, first-time affiliation |
| `/payments/direct-debit-account/otp/:orderId` | GET | `orderId` (path) | ✅ (query `?typeOrder=`) | Domiciliación, request OTP |
| `/payments/direct-debit-account/otp` | POST | `orderId` (body) | ✅ | Domiciliación, charge with OTP |

Registered in `internal/routes/payments.go`. Adjacent groups, documented here
only where they touch payments: `/orders/:id`, `/orders/confirmation/:name`,
`PUT /customers/parent` (`routes/store.go`), `POST /webhook/order/created`
(`routes/webhook.go`, see [Recurring domiciliación](#recurring-domiciliación--webhook--daily-retry)),
and `GET /healthz`.

**Cash and Zelle are not exposed.** `models.ValidateCash` / `models.ValidateZelle`
still exist in `internal/models/payments.go`, but no route, handler, or service
method references them — dead shapes, not endpoints.

## Response contract

Handlers are thin (`internal/handlers/payment.go`): bind JSON, call the service,
map any service error to **HTTP 500 with the raw error message** as
`{"error": "..."}`. Bind failures are 400. There is no error envelope beyond
that, so:

- A failed **pago móvil** match is **HTTP 200** with `{"success": false, "message": "..."}`
  — the front must read `success`, not the status code.
- A failed **domiciliación** is **HTTP 200** with `{"success": false, "code": "ERR0X"}`.
- Only infrastructure failures (Shopify down, BCV down, unmapped R4 code) reach 500.
- One exception: an in-flight débito inmediato is reported as
  **HTTP 500 `{"error": "EN_PROCESO"}`** — see below.

## Débito inmediato

1. **`generate-otp`** — reads the order/draft total, converts to VES, asks R4 to
   send its own OTP to the buyer's phone. R4 sends it, not Mailgun.
2. **`validate-direct-debit`** — charges through `r4Repo.ValidateImmediateDebit`
   and answers off R4's **first** reply. It does not wait for the outcome.

Everything after that first reply happens in a goroutine
(`waitForOperationCompletion`), which is also the only thing that writes the
`r4_appa_debits_direct` row and marks the order paid:

- R4 answers with a **break code** while the bank is still deciding — `AC00`
  (in progress) or `"11"` (pending), per `domains.IsR4BreakCode`. The goroutine
  polls `GetOperationByID` every 3 s, at most 10 times, until the code stops
  being one of those.
- If it lands on `ACCP`, `finalizeCharge` runs and the row is written with the
  completed order's id/name. Any other code writes the row and stops. A failed
  poll writes the sentinel code `"ERROR"`.

What the caller gets back, meanwhile:

- **First reply is a break code** → HTTP 500 `{"error": "EN_PROCESO"}`,
  immediately.
- **Anything else** → HTTP 200 `{"message": "Direct debit validated
  successfully"}`.

> **HTTP 200 here does not mean approved.** The response is not checked against
> `ACCP`: a refusal (`AM04`, `AM02`, `MD15`, …) answers exactly like an approval,
> because the code that could tell them apart only exists inside the goroutine.
> A caller that treats 200 as paid will show a thank-you page for a declined
> débito — which is what both checkouts work around by reading the
> `r4_appa_debits_direct` row instead (`debito-status`). Fixing that means
> answering off the resolved code, and it is the single highest-value change
> this endpoint has left.

> **`EN_PROCESO` has no follow-up channel either.** No poll endpoint, no status
> endpoint, no webhook for the front to learn how that charge ended, on any
> `typeOrder`. The order does get marked paid server-side once R4 answers — the
> buyer's browser just never hears about it.

## Pago Móvil

`validate-mobile-payment` **does not initiate a charge**. R4 pushes received
pago-móvil rows into `r4_appa_mobile_payments`; this endpoint matches one:

- filters: `order_id IS NULL` (unlinked only), `issuing_bank`, `sender_phone`,
  `reference LIKE '%<reference>'` (suffix match), and either today's date when
  `automatic: true` or the supplied `date`;
- retried up to 3 times with a 1 s pause, for the row R4 may still be writing;
- no match → `{"success": false, "message": "no se encontro ningun pago movil…"}`.

Amount is compared with a tolerance of `0.1 USD * BCVTasa`
(`domains.ClassifyCharge`, `internal/domains/mobile_payment.go`):

| Verdict | What happens | Response |
| --- | --- | --- |
| `Exact` | Row linked to the order, order finalized. | `success: true` |
| `Overpaid` | Row linked, order finalized, **excess refunded** via `r4Repo.ChangePaid`. | `success: true` + message naming the refunded amount (or asking the buyer to contact support if the refund call failed) |
| `Underpaid` | Row **deleted**, **full amount refunded**, order untouched. | `success: false` |

Every refund attempt — success or failure — is recorded in
`r4_appa_mobile_payment_reversals` with reason `LESS` / `GREATER`.

When `automatic` is false the customer's débito-inmediato metafield is refreshed
in the background with the bank/phone/DNI used.

### `validate-mobile-payment-manual`

Multipart form (`orderId`, `orderName`, `billImageFile`, optional `typeOrder`).
Uploads the receipt to Google Drive and inserts a row in the manual-orders table
with `ValidateStatus: "PENDING"` and `PaymentMethodID: 4`. It **does not charge,
does not mark the order paid, and does not complete a draft** — a human resolves
it later. On DB failure the uploaded Drive file is deleted.

> Its success payload still reads `"Zelle payment validated successfully"`.
> Copy-paste leftover; the endpoint is pago móvil.

## Domiciliación (direct debit account)

Two flows, plus a third that runs without a browser.

### First-time affiliation — `POST /payments/direct-debit-account`

Body: `dni`, `orderId`, `account` (exactly 20 chars), optional `typeOrder`.

- **Refuses if the customer already has the `direct_debit_account` metafield** —
  returns the generic error, not a code.
- Charges the order total (concept `"Prueba"`) through R4.
- On `ACCP`: writes the metafield (`SetCustomerDebitDirectAccount`), finalizes
  the order, backfills the DB row with the resulting order id/name.
- The DB row stores only the **last 4 digits** of the account.

### Recurring charge with OTP — `GET .../otp/:orderId` then `POST .../otp`

1. **`GET /payments/direct-debit-account/otp/:orderId`** — resolves the
   order/draft, generates a cryptographically random 6-digit code, caches it
   **keyed by `orderId`**, and mails it via Mailgun **to the email Shopify has on
   file for that customer**. The request carries no email field, so there is
   nothing for a caller to redirect.
2. **`POST /payments/direct-debit-account/otp`** — body `orderId`, `otp`,
   optional `typeOrder`. Requires the metafield to exist, validates the OTP,
   parses `account`/`dni` out of the metafield, and charges exactly those — never
   anything the request supplies. On success the order is finalized.

**OTP bypass:** if the order was created by the app whose id is
`RECURRENT_DIRECT_DEBIT_APP_ID` (`target.App.IsID(...)`), the OTP check is
skipped entirely. That is how the webhook path charges with `otp: ""`. A draft
never has an `App`, so this bypass can only trigger on `typeOrder: "Complete"`.

**Affiliation self-healing:** when R4 answers `MD01`/`MD09` (mapped to `ERR02` /
`ERR03`, `domains.IsAffiliationPending`), the customer's `direct_debit_account`
metafield is **deleted**, so the next attempt goes back through affiliation.

### Response codes

`internal/domains/direct_debit_account.go` — the front renders the Spanish copy.

| Code | R4 code | Meaning |
| --- | --- | --- |
| `OK` | `ACCP` | Charged. |
| `AAF01` | — | Customer has **no** affiliation on file. (The name reads backwards; the condition is `DirectDebitAccount == nil`.) |
| `OTP01` | — | OTP wrong, expired, or already used. |
| `ERR01` | `AM04` | Insufficient funds. |
| `ERR02` | `MD01` | Affiliation requested, not active yet. Metafield cleared. |
| `ERR03` | `MD09` | Affiliation refused. Metafield cleared. |
| `ERR04` | `AC01` | Invalid account number. |
| *(none)* | anything unmapped | HTTP 500, generic message. Add new codes to `directDebitAccountResponseCodes`, never in a handler. |

### OTP cache

`internal/services/otp_cache.go`: in-process `map` behind a mutex, **2-minute
TTL**, single-use (`Validate` deletes on match), swept every 5 minutes.

- It does not survive a restart between the two steps.
- It does not work across more than one instance — an instance that didn't serve
  the request step has no record of the code.
- **Nothing rate-limits it.** No throttle by order id, customer, or IP, on either
  step: OTP brute force inside the 2-minute window and OTP-mail spam are both
  open. Same for every other endpoint here.
- `paymentService` and `cartPaymentService` each construct **their own** cache
  instance; codes do not cross between `/payments/*` and `/cart-payments/*`.

## Deliberately not implemented

Two things this service is asked about often enough to be worth stating as
decisions rather than gaps. Both exist in `boneappetit-api`, the sibling service
this one is forked from; neither is coming here unless the decision changes.

**No discounts.** `finalizeCharge` applies tags and nothing else — no
domiciliación discount, no `ApplyDiscountToDraftOrder`, no discount parameter.
The charge is always the order/draft total as Shopify reports it
(`CurrentTotalPriceSet`).

The cart path is the one exception, and it is not this service's doing: the
amount arrives already signed in `X-Cart-Amount`, so whatever was deducted
before it was signed is charged without this service knowing anything was.

**No Cashea.** No down-payment pricing, no `GET /orders/cashea/:idNumber`, and
no request binds a `casheaId`. Bolívar amounts are always `total * BCVTasa`.

## Who completes what

`finalizeCharge` (`internal/services/payments.go`) is the single place an order
is turned paid, and it behaves differently per `typeOrder`:

- **`Complete`** → `markOrderAsPaid(orderGID)`. Nothing else.
- **`Draft`** → tags first (drafts lock once completed; today every caller passes
  `nil` tags), then `CompleteDraftOrder(gid, false)`, then `MarkOrderAsPaid` if
  the resulting order didn't already come back `PAID`.

**With `typeOrder: "Draft"`, this service completes the draft in the same
request that confirms the charge** — for pago móvil, débito inmediato, and both
domiciliación flows. Whatever the front used to call to complete the draft after
payment **must stop being called for those rails**, or it will try to complete a
draft that no longer exists: at best a visible error for a buyer who already
paid, at worst a race between two completions.

Once a draft becomes a real order, the recorded payment row is updated with the
real `order_id` / `order_name` (`updateDirectDebitAccountRecordAfterCompletion`,
and the equivalent inline save on the pago-móvil path).

### Charge succeeded, draft couldn't be completed

`ErrDraftChargedNotCompleted`. The money moved; `draftOrderComplete` failed. The
service **emails support** with the order/draft name and reason
(`alertDraftFinalizationFailed`) and callers explicitly ignore this error rather
than reporting failure. **The buyer must not be told to retry** — a retry
double-charges.

## Recurring domiciliación — webhook + daily retry

Not a `/payments/*` endpoint, but the same service method behind it.

1. **`POST /webhook/order/created`** (`routes/webhook.go`) — HMAC-validated
   against `SHOPIFY_HMAC_SECRET` via the `X-Shopify-Hmac-Sha256` header. Orders
   whose `app_id` isn't `RECURRENT_DIRECT_DEBIT_APP_ID` are ignored. Everything
   else is pushed onto a buffered queue (32) drained by 4 workers, and the
   handler answers **200 immediately, always**, so Shopify never retries.
2. **The worker** calls `DirectDebitAccountWithOTP` with an empty OTP (allowed by
   the app-id bypass above), after a dedup check —
   `HasSuccessfulRecurrentCharge` looks for an existing successful row for that
   order id.
3. **A declined charge** is recorded in the pending-retries table
   (`ON CONFLICT (order_id) DO NOTHING`, so repeated declines don't duplicate).
4. **A daily cron at 09:30:00 `America/Caracas`** (`internal/jobs`,
   `robfig/cron/v3`, registered in `cmd/main.go`) retries every pending row
   across 4 workers. Only orders still `PENDING` in Shopify get charged; any
   other status deletes the row without charging, as does a successful charge.
   **There is no give-up window** — retries continue indefinitely while the order
   stays pending. The cron is **not scheduled when `DEBUG=1`**.

## Gotchas worth knowing before editing

- **Most handlers pass `context.Background()`, not the request context**
  (deliberate, commit `0e0e34c`): a buyer closing the tab must not cancel an
  in-flight bank charge. `GET .../otp/:orderId` and the whole `/cart-payments/*`
  group use `c.Request.Context()` instead.
- **Customer DNI** comes from the request (`dni` + `dniType`) or from the
  Shopify customer's `ParentID` metafield (`dniType-dni`). Use
  `helpers.GetCustomerDNI`; do not re-parse inline.
- **DNI formatting differs by call**: R4 charge requests concatenate
  (`V12345678`), DB rows hyphenate (`V-12345678`).
- **Transactions** use `tx := p.db.Begin()` + `defer db.DBRollback(tx, &errDB)`;
  the deferred helper commits when the pointed-to error is nil. Assign the named
  error variable on failure paths or the transaction commits anyway.
- **No migration runner.** Change the GORM model in `pkg/db/models/` *and*
  `pkg/db/schema.sql`, then apply the change against the database by hand.
