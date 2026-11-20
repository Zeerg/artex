---
name: api-recon
description: When collecting website API interfaces, call this skill.
---

# API Recon (Frontend interface reconnaissance)

In**Authorized**Under the premise, try to discover as much as possible:**Backend API** (Path, method, parameters, response body),**Frontend Routing**, **UI Feature trigger point** (Tab, Pop-up, table operations, etc.).

---

## Boundary and prohibition (Agent must read) · Violation leads to out-of-bounds)

This skill **Limited to API / parameter reconnaissance**, Not during vulnerability mining or penetration exploitation phase.

### Task Boundary,

| range | Allow | Prohibit |
|---|---|---|
| **Target** | Enumerate path, method, parameters, routes, and UI trigger points | SQLi/XSS/Privilege escalation/break-in/fuzz vulnerabilities, packet manipulation attacks, destructive operations |
| **auth** | Hook + stub/mock Bypass**Client**Login door | Request or guess account passwords from users; try submitting real login form data |
| **Runtime** | Hook interface without credentials using mock responses to allow SPA to enter the login shell layer. | Processes that depend on a real backend session must continue. |

### Dynamic analysis without credentials (Phase 3 default)

1. Success `preload.js` / `runtime_harvest.js` **Intercept and stub** Login, permissions, menu etc. bootstrap interfaces;
2. Response from business query interface **Structurally correct, business code success, data can be empty.** The mock body;
3. Allows the frontend to render login pages even in a no-backend or 401 environment, triggering more XHR/fetch/WebSocket requests;
4. **Empty data, blank tables, placeholder UI are expected**——Do not redirect to real login or vulnerability testing for this.

**One-liner**: Use mock data toexpands frontend routing and component mounting,**Only record outbound requests**; What the backend returns is not important; what matters is the frontend.**Which other interfaces will be sent**.

### Process hard prohibition

| Prohibit | Alternative approach. |
|---|---|
| Phase 1 Complete before grep/curl/Read main entry. `index-*.js` Extract API path | Run `OUTDIR/harvest_static.py` |
| Handwritten `extract_apis.py` Wait for an alternative script to replace harvest. | edit `OUTDIR/harvest_static.py` Restart later |
| Same grep/ command failure ≥2 Still repeating. | Re-run the entire intent from the beginning. It's the outermost layer; one re-run means the number of times inside layers will be multiplied again. |
| Skip door A/B, run directly `scripts/` Original Version | Copy to OUTDIR and rename according to the target. |
| Real username/password, OTP, OAuth authentication. | stub/mock (see above) |
| Bypass stub with the excuse of using real data for privilege escalation/injection testing. | Record only outbound traffic; this is within the recon boundary. |
| Irreversible operations such as deletion, export of sensitive data, and bulk write. | coverage Click Also Works |
| Incomplete runtime + Dynamic enumeration, claiming all pages and interfaces have been obtained. | See 'Complete Definition' or annotate limitations. |
| Trigger matrix for incomplete parameters + diff, Claims to have mastered all parameters | Phase 3b Matrix + Phase 5 diff |
| Infer required/optional fields using a single runtime sample | Multi-sample diff or validation rules/error backtracking |

---

## Two-layer model. + Run mode.

| Layer | Output | Limit |
|---|---|---|
| **Static** (JS bundle) | Full endpoint paths, route drafts, grouping point field candidates. | No HTTP method; parameters must be Phase 1b; missing runtime URL concatenation |
| **Runtime** (active sessions) | method + body + Multi-select + Dynamic URL + WS/SSE; Multi-sample diff completion parameters. | Request sent only when page actually renders; single sample insufficient to determine required/optional |

| Run mode. | Engine | Applicable |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API List, METHOD/params/response body, WebSocket/SSE, repeatable batch runs. |
| **coverage** | browser + `preload.js` | Click Tab/Popup/Table, deeper functionality points covered |
| **both** | First depth then coverage | Most comprehensive, takes longest time |

**Parameter methodology** (No universal script): path uses harvest/regex; parameters use **Anchor Point Expansion, + UI Binding chain + Multi-sample diff + Error Backtracking,** (grep Recipe see [reference.md](reference.md) J Section).

---

## Complete Definition,

File upload | multipart |

- [ ] **Static**: Phase 1 harvest Output `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: At least depth or coverage must be specified; coverage/both must **Hook Take effect + Dynamic enumeration of rings**
- [ ] **Shell**: Accessing a business path when it should not be. `/login` (Note hash routing)
- [ ] **Parameter**: coverage/both Complete parameter trigger matrix + `param_samples.json`; Phase 5 Merge `params_merged.json`
- [ ] **Depth** (If the module page is blank): Phase 4 permission tree restoration and rerun until it appears **module Level API** (Not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 Produce complete output (see Phase 5 output table);`insert_assets` Write service and endpoint assets

---

## Script with access control

`scripts/` Reference template only,**Prohibit**Run the original version and treat it as the final result

**Rules**: Read first → Modify by Target, → Write `OUTDIR` (Like `recon/`)→ Remember `CHANGES.md`; If not matching, rewrite by methodology, just borrow structure.

| Access Control | When | Reference Script → OUTDIR Copy | Common mandatory items |
|---|---|---|---|
| **A (Static)** | Phase 0 Then,**First time**Before running harvest/spider | `harvest_static.py` / `spider_mpa.py` | **Most sites have default regex that can be run directly.**; Only change the endpoint regex and webpack/Vite when manifest/dialect does not match. `publicPath`, MPA exclude/cookie |
| **B (Runtime).** | Phase 2 After, before running depth/coverage. | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage Key, neutralize success value, stubs, login regex, API prefix, hash/history |

**SPA Force order.** (Not interchangeable; Phase number takes precedence over 'explore then script'.

| Steps | must | Prohibit |
|---|---|---|
| Phase 0 After completion | Next Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read Main entry `index-*.js` (Usually >500KB) |
| Access Control A | copy script → Minor Modification as Needed, → **Run Immediately** | Manually extract API before deciding whether to harvest |
| Phase 1 Before completion | `wc -l` Verify output; 404 change harvest retry. | Manually write an extract script; repeatedly grep for URLs that have not been downloaded |
| Phase 1b start | grep Only `OUTDIR/js/*.js` | Use main bundle instead of harvest |

- ✅ Copy `harvest_static.py` → (Optional) Change regex → **Run Immediately**
- ❌ curl Main bundle → grep Multiple → Write temporary extract → Harvest last
- **MPA**: Phase 0 Run the next Bash command. = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and output constraints

| Lateral Movement | Description |
|---|---|
| Large file | >100KB To `index-*.js` **Prohibit** Read/grep Enter context; use OUTDIR script batch processing |
| grep Output | must `\| head -20` Or `-m 5`; Keep only the path summary in the conversation, do not paste bundle fragments. |
| List And Pair | Use `wc -l`, `ls \| wc -l`; Do not Read entire directories |
| regex Initial probe | Optional,≤1 Time, only ≤50KB Small chunk or HTML; formal static content is based on harvest. |
| reference | Recipe/template/troubleshooting see [reference.md](reference.md), Do not repeat inline full text. |

---

## Execution roadmap

```
Phase 0 Categorize + OUTDIR
 → Access Control A → Phase 1 harvest (★ Run Immediately ★)
 → Phase 1b Parameter Reversal
 → Phase 2 Authentication three gates → config.json
 → Access Control B → Phase 3 Runtime + Parameter matrix.
 → Phase 4 Permission tree (necessary when)→ Retry Phase 3
 → Phase 5 Merge report. + insert_assetsBatch insert all discovered services, endpoint API assets; under any circumstances, do not allow missing discovered assets during insertion
```

Check sequentially;**Cannot proceed to the next Phase if the previous item is not completed**.

1. [ ] **Phase 0**: Initial exploration of SPA/MPA; create `OUTDIR` → [Phase 0](#phase-0--Categorize)
2. [ ] **Access Control A + Phase 1**: copy script → **Immediately** harvest → `wc -l` List And Pair → [Phase 1](#phase-1--Static)
3. [ ] **Phase 1b**: Anchor Point Expansion, + Binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--Parameter Reversal)
4. [ ] **Phase 2**: Authentication three gates → `config.json` → [Phase 2](#phase-2--Authentication three gates)
5. [ ] **Access Control B**: Adjust runtime script, → [Phase 3](#phase-3--Runtime)
6. [ ] **Phase 3**: depth / coverage / both; Confirm shell entry; parameter trigger matrix → `param_samples.json`
7. [ ] **Phase 4** (If needed): permission tree → patch stubs → Retry Phase 3 → [Phase 4](#phase-4--Permission tree restoration)
8. [ ] **Phase 5**: merge output + Reduce + `insert_assets` → [Phase 5](#phase-5--Merge and report)

---

## Phase 0 — Categorize

fetch entry HTML,**Create `OUTDIR`** (Do not change skill content. `scripts/`):

- **SPA**: Skeleton + `<div id=app>` + chunk → Phase 1–5
- **MPA**: SSR + `<form>`, No endpoint bundle → Gate A after:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Output `forms.txt`, `links.txt`, `api_inline.txt`. SPA If forms ≈ 0 → Switch to Phase 1.

---

## Phase 1 — Static

Adhere to [Script with access control](#Script with access control) · [Tool and output constraints](#Tool and output constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: Parse HTML script → webpack/Vite manifest → Download all lazy chunks → Output `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk Number vs Manifest: For 404 errors, modify harvest retries; do not manually curl each chunk.
- `api_static.txt` Insufficient → Relax OUTDIR endpoint regex and rerun (see reference)

### Phase 1b — Parameter Reversal

path From Phase 1; parameter fields must be recon'd separately. See grep rules at [Tool and output constraints](#Tool and output constraints).

**Complete Standard**: Important interfaces should respond——Field name, transmission position, type inference, whether required, sample value, confidence.

#### 1b.0 — Transmission Form,

| Form | Where are the parameters? | Static priority first |
|---|---|---|
| REST JSON | body + query | path Anchor point next to `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql Template,`$page: Int` |
| Traditional form | urlencoded | `<form>`, `FormData` |
| File upload. | multipart | `FormData.append` |
| Path Parameters | `/user/:id` | Routing table + `useParams` / `$route.params` |
| Encryption/Signing | Encapsulate `sign`/`data` | Hook Encryption function parameters (refer to section D). |

Output: annotate each interface `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor Point Expansion,

Expand the window to find grouping objects based on a known path:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | parameter clues |
|---|---|
| axios Instance | `data` / `params` |
| Unified request | Interceptor injection of global fields |
| OpenAPI Client | Generate method signature. |
| React Query / SWR | hook Second parameter. |
| Vue composable | composable Input parameters |

Type residue: `yup`/`zod`/rules, `Form.Item name=`, Embed Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Source binding | Technique |
|---|---|
| Form submit | Follow submit → transform → API |
| Table Search | `getFieldsValue()` → `params` |
| Routing | `:id` / `?tab=` |
| Interceptor | global `tenantId`, Pagination, sign |
| Enumerate select options. | `options` → API Enumerated value |

DevTools call stack From `fetch`/`XHR.send` Trace back package function.

#### 1b.3 — Packaging three questions (≠ Phase 2 Authentication three doors)

| ask | What to answer. |
|---|---|
| **Assembly** | payload Where to build, transform traces. |
| **List And Pair** | required, pattern, enum |
| **Transmit** | path / query / body / multipart / Head |

Interceptor gate (Phase 2) also reads global injection fields (Authorization,`X-Tenant-Id`, sign).

#### 1b.4 — Connect with Phase 3

Candidate fields come from static/bound layers;**required/optional/conditional dependency**Must use Phase 3 parameter matrix + diff + Phase 5 Error backtracking.

---

## Phase 2 — Authentication three gates

In `OUTDIR/js/` grep (With `head`), Write `config.json` (Formula see reference):

| Door | Issue | Keywords |
|---|---|---|
| **Rendering gate** | How to determine if logged in? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate.** | What triggers a jump `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where does the menu/permission come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Prohibit using localStorage key names as credentials——Must confirm from the chunk/request chain.

**Exit = Access Control B**: The conclusion falls to `config.json`, And Modify `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observe (optional)

Use OUTDIR within `preload.js` Confirm session key name, Authorization, nested API URL:

| Version | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers Observe |
| `extractUrlsFromResponse: true` | Sub-APIs in the response |
| `observe.storageReads/cookieReads: true` | Fill in config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage Export per round:`__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Must have passed doordeny B; comply with [Boundary and prohibition](#Boundary withforbidden-agent-required-reading--violation-means-out-of-bounds) · Credential mock strategy is not applicable.

`config.json` Settings `"runtimeMode": "depth" | "coverage" | "both"` (Template see reference).

### Hook With stub (depth + coverage Shared)

| Layer | range | Purpose |
|---|---|---|
| L1 precise | auth/Permissions/bootstrap stub | Passed initial screen authentication |
| L2 Negative Correction | All JSON responses | Login code not logged in. → High-risk |
| L3 Fallback | Not hit L1 `/api` Equal | empty successful body, to expand UI |

- **depth**: fake auth + `forward` Modify business code. + `stubs`; Iterate `routes` (hash/history); Output `runtime_api.json`
- **coverage**: **document-start** Completed `preload.js` (CDP `addScriptToEvaluateOnNewDocument` Or Userscript)

Validation:`window.__API_RECON_PRELOAD__` exists; business path not back `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage Dynamic enumeration (must do)

1. Main navigation/Sidebar — click each item, wait for network 1–3s
2. Tab — `role=tab`, `.ant-tabs-tab`
3. Table — First row for viewing/editing/details.
4. Toolbar — Export, filter, create (**Avoid irreversible deletion**)
5. Every module entry — Merge API/Route.
6. SPA — For `routes.txt` Uncovered path controlled `pushState` (MPA Prohibited)

**Parameter trigger matrix** (Required): Record each module once per operation type,**diff Multi-sample**:

| Operation | Usually extra parameters. |
|---|---|
| List First Screen | pagination + Default filter. |
| Click search | keyword, filter |
| Advanced filtering. | More optional |
| New/create/edit | Complete entity |
| Batch/export/sort | `ids[]`, `exportType`, `sortField` |

**stub Outbound body/headers remain true**——Based on the request. Record → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + Sidebar click + `pushState`
- **both**: Perform 3a depth first, then 3b coverage.

---

## Phase 4 — Permission tree restoration

**Trigger**: Blank module page / only bootstrap per route (like locale)→ Content door not passed.

| Phenomenon | Meaning |
|---|---|
| Shell Success | Rendering gate + Interceptor gate passed |
| Missing items/clicking on blank sidebar | stub shape Or insufficient permission codes |
| Each route API is the same and rare. | `v-if permission` Not passed |
| `routes.txt` Significantly less than bundle | Must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical Chain:`role_permissions` (flat codes)+ `permissions/all` (tree)→ `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate output: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub Check: Outer layer `response_code` Consistent with the interceptor gate; flat codes align with tree structure;`routes` Coverage `route_map` All links.

Update `config.json` after**Retry Phase 3**. Large SPA can be adjusted `waitUntil`, `routeTimeout`, `perRouteMs` (See reference A3/I section).

---

## Phase 5 — Merge and report

### Output table

| File | Stage | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundle with path |
| `param_candidates.json` | 1b | Static parameter field candidates |
| `config.json` | 2 | Three doors + runtime Version |
| `runtime_api.json` | 3a | depth Detailed recording (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multi-sample, click logs, detail. |
| `route_map.json` Equal | 4 | Intermediate file in permission tree (if executed). |
| `params_merged.json` | 5 | Merge parameter fields + Confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, params, features, limitations |
| **insert_assets** | 5 | Write all services and endpoint assets to the asset library |

### 5b — Parameter Merging,

From `param_samples.json` diff, **No universal merge script**. Confidence rules see reference J7 (high/medium/low/pending).

### 5c — Error Backtracking,

Within the authorized range, incomplete requests can be read 400 ().**This parameter belongs to recon, not vulnerability testing.**): `field 'x' is required`, Enumeration errors etc. Note `data` Packaging,`variables`, Encryption before `bizData`.

The report must note: runtimeMode, static/runtime API count, parameter confidence, un-covered modules, relative reference script of `CHANGES.md` Summary.

`site_map.json` Suggested structure:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields with grep recipe see: [reference.md](reference.md).

---

## General Instructions,

- **Framework-agnostic.**: webpack/Vite/Angular lazy load Method same.
- **Transmit**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web Out of scope
- **SSR**: Client fetch can be recorded; RSC/Server Actions are not fully enumerable.
- **Blind Spot**: JSVMP, WASM, HMAC/mTLS Strong validation → Static + Annotation Limitations
- **Parameter blind spot.**: Conditionallinkage, hidden parameters, WASM packaging → 'Pending/triggers/unreachable
- **Static is a safety net**: runtime Even when blocked, static enumeration of endpoints can still occur.

---

## Additional Resources,

- Grep Formula,`config.json` Templates, troubleshooting, Hooks, parameter reverse J sections, site_map templates:**[reference.md](reference.md)**
- Refer to script path at [Script with access control](#Script with access control) show
