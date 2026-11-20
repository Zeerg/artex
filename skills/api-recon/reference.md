# api-recon — Reference Manual,

Grep Formula,`config.json` Template and troubleshooting. All grep targeting `js/` Directory execution. Bundle single line can first `js-beautify` Or `sed 's/}/}\n/g'`, Usually a raw grep with context window will suffice.

## Script Explanation,

`scripts/` All files in the directory are**Reference template.**, Must be adjusted according to the target site before execution. Typical changes include:

| Script | Common items to adjust |
|---|---|
| `harvest_static.py` | endpoint Regular expressions, webpack/Vite manifest parsing, micro-frontend publicPath, retries/concurrency |
| `runtime_harvest.js` | neutralize Field names match success values and stub rules with body structure, routes source, WS recording,`waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, Whether to enable L3,`recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` Destructive link, cookie, depth/max, cross-domain filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` Regular expression, KEY naming pattern. |
| `build_perm_tree.py` | `userRouteAuth` Parsing,`ROOTS`/`PREFIX_PARENT` Hierarchicalheuristics, stub outer field names |
| `config.json` | Unified entry for all site-specific parameters |

Encoding | config forgery method | `recon/`), Note relative reference script specific changes in report.

---

## A. Reverse engineering three gates

### A1. Rendering gate — 'How to determine if logged in?

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find chain of `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`, Confirm **Storage key**, **Container** (Cookie vs localStorage), **Encoding**:

| Encoding | config Spoofing method |
|---|---|
| Plain text string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | No Signature/`alg:none` JWT, Or bundle key signature |
| Encryption (SM2/AES/RSA). | Find hardcoded keys; rendering gates only need decodable blobs when forging; otherwise, use a static fallback |

→ Write `cookies` / `localStorage`.

### A2. Interceptor gate. — 'What triggers the jump /login?'

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(Not logged in|Please re-login|Login expired|unauthorized|Login Expired|Authorization|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Confirm:**Field name**, **Success value** (Usually `0` Or `200`), **Failure value triggered jump**. Verify using a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Write `neutralize.fields` + `neutralize.success`.

### A3. Content gate — 'where does the menu/permission come from?'

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two-layer data.** (Common enterprise backend):

| API | typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACL. |
| `.../permissions/all` | `tree[{ code, position, children }]` | Rendering sidebar menu |
| bundle Inside `userRouteAuth` | `{ CODE: { url, name? } }` | code → Frontend path. |
| bundle Inside `routeMap` | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Confirm with consumer code:`getResultTree(tree, permissions)` How to filter, `v-if` / `hasAuth(code)` Check which field.

**Manual forge** (Small site): Build a permissive payload. → `stubs`.

**Full permission tree restoration** (Large sites, sidebar/submodules still blank): see **I section**.

---

## B. config.json Template

```json
{
 "baseUrl": "https://target/",
 "runtimeMode": "both",
 "chromium": "/usr/bin/chromium",

 "cookies": [
 { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
 ],
 "localStorage": { "token": "faketoken", "isLogin": "1" },

 "neutralize": {
 "fields": ["response_code", "code", "errno", "ret", "status"],
 "success": 0,
 "flags": { "success": true, "message": "ok" }
 },
 "forward": true,
 "loginUrlPattern": "/login",
 "apiPattern": "/api/|/rest/|/graphql",

 "mockTier": "L1+L2",
 "recordDetail": true,
 "observe": {
 "storageReads": false,
 "cookieReads": false,
 "xhrHeaders": true
 },
 "neutralizeVueRouter": true,
 "stubs": [
 {
 "match": "permissions/all|/menu|role_permissions",
 "body": {
 "response_code": 0, "code": 0,
 "data": {
 "permissions": ["*"],
 "menus": [
 { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
 { "name": "alert", "path": "/alert", "show": true, "children": [] }
 ]
 }
 }
 }
 ],

 "explore": {
 "clickTabs": true,
 "clickTables": true,
 "pushStateFallback": true,
 "maxMenuItems": 50
 },

 "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
 "waitMs": 1500, "perRouteMs": 900, "headless": true,
 "waitUntil": "domcontentloaded",
 "routeTimeout": 12000,
 "proxy": "",

 "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field description:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` Prefix:`b64json:` → base64(JSON); `json:` → Original JSON; no prefix → Literal
- `forward: true` forward real requests and rewrite code fields;`false` Complete offline stub
- `mockTier`: coverage Mode preload enable hierarchy, like `L1+L2`, `L1+L2+L3`
- `routes` From `routes.txt`; forge Menu post harness automatically appends `<a href>`
- `captureResponses` / `recordWs` Only depth mode is valid
- `waitUntil`: Large SPA uses `domcontentloaded`, Avoid `networkidle2` Suspend
- `routeTimeout`: Single route `page.goto` Timeout (milliseconds)
- `proxy`: Puppeteer `--proxy-server`; Can also be set `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Double stub templates (role_permissions). + permissions/all)

```json
"stubs": [
 {
 "match": "role_permissions",
 "body": {
 "response_code": 0,
 "data": {
 "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
 "role_type": "SUPER_ADMIN"
 }
 }
 },
 {
 "match": "permissions/all",
 "body": {
 "response_code": 0,
 "data": [
 {
 "code": "MONITOR",
 "position": 1,
 "children": [
 { "code": "MONITOR_ALERT", "position": 1, "children": [] }
 ]
 }
 ]
 }
 }
]
```

Outer field name (`response_code` / `code` / `data`)must be consistent with A2 interceptor gate;`permissions` Must cover all leaf code in the tree.

---

## C. coverage mode: preload configuration

Edit `scripts/preload.js` Top `CONFIG` Object, or replace before CDP injection:

```javascript
const CONFIG = {
 loginPathRe: /\/(login|signin)(\/|$|\?)/i,
 mockTier: 'L1+L2',
 forward: true,
 recordDetail: true,
 extractUrlsFromResponse: true,
 neutralizeVueRouter: true,
 observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
 neutralize: { fields: ['response_code', 'code'], success: 0 },
 stubs: [ /* Same as config.json stubs */ ],
 apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Validation:`window.__API_RECON_PRELOAD__ === true` And pathname is stable.

Export recorded results:

```javascript
JSON.stringify({
 apis: [...window.__API_RECON_LOG__],
 detail: window.__API_RECON_DETAIL__,
 routes: [...(window.__API_RECON_ROUTES__ || [])],
 observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook Capability

preload (coverage)Combining with runtime_harvest (depth) built-in browser hook capabilities and coverage:

| Hook Capability | Value of API discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | Record request URL/method. | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Discovering Authorization headers, etc. | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie Read | Confirm session key name | ⚠️ Optional `observe.storageReads/cookieReads` |
| Vue Get route. | Complete frontendRoutes | ✅ `__API_RECON_ROUTES__` (Routes loaded) |
| Vue Block route guards and login redirects | Trigger API with module expansion | ✅ `neutralizeVueRouter` + Native redirection handling |
| React Get route. | Supplement routing | ⚠️ Static + Click; no dedicated Hook |
| Block page redirection (login path). | Stay on page analysis | ⚠️ Only blocks login path to avoid blocking business navigation |
| Hook Encryption library (CryptoJS/SM etc.) | Encrypted Parameters → Plain text API body | ❌ Manually hook the encryption function parameters; conclusions should be written to config. |
| anti-debug bypass | Otherwise, runtime cannot record API calls. | ❌ Must be handled manually; static still available. |

---

## E. Endpoint Extract regex (static too few)

In `harvest_static.py` To `extract_endpoints` Relax, or manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Phenomenon | Reason → Handle |
|---|---|
| Static API is rare | endpoint Dialect mismatch → Relax regular expression (D section) |
| chunk Number ≪ manifest | CSS-only Or chunk is not deployed; 404 has been retried. |
| runtime Still displays the login page | Rendering door error → Review A1: key name, container, encoding, domain |
| Entered shell but module is blank. | Content gate → forge Menu (A3);`routes` path May Not Be Correct |
| Each route only has bootstrap/locale. | Incomplete permission code → I Restore permission tree; check `role_permissions` + `permissions/all` Double stub |
| Sidebar has an item but the subpage is blank | tree Missing intermediate nodes or code not matching `userRouteAuth` Inconsistent |
| Skip login for each API | Interceptor gate. → Confirm `neutralize`; Extend walk logic for nested fields |
| WS Frame is 0. | Subscribe after user interaction; extend. `perRouteMs` |
| Empty response body. | Only `forward: true` Real response at the time |
| Chromium Missing | Install chromium or set up. `config.chromium` / `CHROMIUM` |
| Mock Many still log in | Hook Too late or missing `location.href` setter → document-start + preload |
| List Fully Empty | L3 Normal for empty array; continue clicking Tab/Settings/Details |
| Mistakenly treat Redux action as route | Filter internal paths containing get/set/change/clear/toggle/upload |
| Vue Still jump to login. | preload Not document-start → Change injection timing; or `neutralizeVueRouter: false` Manually clear guards |
| Response contains a URL but not logged. | Start `extractUrlsFromResponse`; Or From `__API_RECON_DETAIL__` Manual extraction. |
| Unknown Authorization header name | Start `observe.xhrHeaders` Or DevTools to view request headers |
| runtime Extremely slow / Timeout | edit `waitUntil: domcontentloaded`; Decrease `routeTimeout`; Do Not Use `networkidle2` |
| Proxy connection failed | Check `proxy` / Environment variable; Puppeteer and curl proxy ports are consistent |

---

## G. hardened Target

When the server gradually validates sessions (non-forgeable signature cookies, server-rendered and non-stub menus), runtime will hang at the shell. Expected behavior:

- **Static data is sufficient for endpoint enumeration.** — The module path is in the code.
- If authorized, use**Real session**Run the same harness:`forward: true`, No need to neutralize, capture real methods/params/responses

---

## H. Single task list

1. Confirm authorization scope
2. **Read** `scripts/harvest_static.py` → Adjust by target → Run → Review `api_static.txt`, `routes.txt`
3. **Phase 1b**: path Anchor Point Expansion, + Binding layer → `param_candidates.json` (J Section)
4. Reverse A1/A2/A3 → Write site-specific `config.json`
5. **Read and adjust** `runtime_harvest.js` / `preload.js` Execute Later
6. `runtimeMode=depth`: `npm install` → Run adjusted harvest script
7. `runtimeMode=coverage/both`: document-start Inject adjusted preload → browser MCP Dynamic Enumeration + **Parameter trigger matrix**
8. Module not rendered → **I Restore Asset Tree** → patch stubs → Retry
9. parameter sample diff + Error Backtracking, → `params_merged.json`
10. Merge → `site_map.json` + `api_merged.txt`, Truthfully annotate coverage, gaps, and script changes

---

## I. Permission tree restoration (Phase 4 deepening)

When forge is simple `menus: [{ path, show: true }]` Used when invalid or sub-module still doesn't mount.

### I1. Locate auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record:**Permission API path**, **Response field name**, **Consuming chunk file name.**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produce recon/route_map.json
```

If `[!] no routeMap pattern found`: Relax `extract_route_map.py` use regex or manual grep:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build permission tree + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (Contains webpack aliases `He=o.DASHBOARD`)
2. Use `route_map.json` Resolve alias → Real path
3. Infer parent by code prefix (`MONITOR_ALERT` → `MONITOR`)
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. `--config` Automatically write in `config.json` To `stubs` With extension `routes`

**Adjust by target** (At the top of the script):
- `DEFAULT_ROOTS`: Top-level module code list
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent Mapping
- `DEFAULT_EXTRA_PARENT`: Non-prefix related orphan nodes

### I4. Verify stub consistency

```bash
# permissions Quantity should be ≈ userRouteAuth Number of items
wc -l recon/perm_codes_all.txt
# routes Should cover all links in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Re-run runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# Compare forge before and after runtime_api.json Count; check for module APIs like /attack, /asset, etc.
```

| forge Per | forge After (success) |
|---|---|
| Each route is the same 3–5 Bootstrap | Different routes trigger different module APIs |
| Only `/api/locale/language` | Appears `/api/web/...` Module endpoint. |
| `routes.txt` Single route. | `routes` 80–110+ From route_map. |

### I6. Still Failing When

- **coverage Pattern**: Click sidebar. + Tab, Permission gating may only request after interaction
- **stub Field**: Compare with the real API (curl). + Real session) and stub nesting.
- **Additional guard**: grep `hasPermission|checkRole|func.` Extend at button level `role_permissions.permissions`
- **Static Fallback**: Module API path still exists `api_static.txt`, runtime Only supplement METHOD/body; parameters retained `param_candidates.json` + Recorded Samples

---

## J. Parameter reverse engineering (Phase 1b / 5b / 5c).

**Methodology, non-generic script.** Find path using regex; find parameters by expanding the window with anchors + UI Binding chain + Multi-sample diff + Error backtracking.

### J1. Anchor Point Expansion, — Finding the package object from the path.

```bash
# Anchor with known Phase 1 path
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layer and transmission form

```bash
# axios / Unified request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# Path Parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate — Required / Format / Enumerations

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — Form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime Fill in: DevTools → Network → Request → **Initiate program.** (call stack)From `fetch`/`send` Trace back package function.

### J5. Encrypted Parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields on ciphertext** — Hook Encryption function.**Input parameters**, Record plaintext payload before encryption; conclusions should be written `config.json` / `param_candidates.json`.

### J6. Must be an ID, cannot write project Chinese names**)

Record each module once per operation, diff request body/query:

| Operation | Focus |
|---|---|
| List First Screen | Pagination default value |
| Search | keyword, filters |
| Advanced filtering. | optional Field |
| New/create/edit | Complete entity |
| Batch/export | `ids[]`, `exportType` |
| Sorting/page navigation | `sortField`, `order` |

Output `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite + runtime ≥2 Sample consistency |
| **Medium** | Static only, or only once runtime. |
| **Low** | Response/error fallback, not re-verified, |
| **Pending trigger** | Static known fields, UI/permissions not yet implemented |

### J8. Scenario Quick Setup

| Scenario | Order |
|---|---|
| REST List page | J1 Package Object → J6 Four diffs → J3 rules |
| New/Edit form. | J3 Form name → J4 submit Link → runtime Submit + Intentionally leave empty to see 400 error. |
| GraphQL | J2 variables Declaration → runtime Record variables for each operation |
| Encrypt body | J5 Hook Input parameters → The fields before encryption are the real params. |

### J9. Mapping to api-recon stage

| api-recon | Parameter recon |
|---|---|
| Phase 1 Static | J1 Anchor Point Expansion, |
| Phase 2 A2 Interceptor | Global injected fields (tenantId, sign). |
| Phase 3 runtime | J6 Trigger matrix. + `param_samples.json` |
| Phase 4 Permission tree | Different module forms differ → Only triggers full fields with sufficient permissions |
| Phase 5 Merge | `params_merged.json` + Confidence; do not single-sample mandatory fill |

### J10. Troubleshooting

| Phenomenon | Handle |
|---|---|
| The static field 'runtime' has never appeared. | Label as 'Pending Trigger'; Supplement permission tree / click advanced filtering /try each cascading option |
| Different body shapes with the same path | Deepen — Sort `action` Record separately, do not forcibly merge schema |
| stub Responding falsely but wanting to see params | **Monitor outbound requests.** body/headers, Do not infer from stub response. |
| 400 Nested field error. | Pay attention to the outer wrapper. `data`/`bizData`/`variables` |
| GraphQL Only see operation name | Expand `variables` JSON; Static find `$var: Type` |

---
