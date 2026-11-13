# HeyGo geo architecture and decision record

Decision date: 19 October 2026. Status: **V1 stack locked; implementation and field validation remain separate gates.**

This decision record supersedes conflicting suggestions in the historical discussion retained below. Earlier recommendations for external navigation, an OSM-first search launch, or a multi-provider launch were brainstorming, not the final decision. Provider research is retained for later evaluation; it does not establish Nigerian field quality, account access or production readiness.

## Implementation follow-up: Admin configuration and testing week

The subsequent implementation request includes **Driver, Rider, backend and Admin Settings**. OSRM may remain on the public demo for this week’s testing; supported hosting is deferred. Its endpoint and demo permission must be editable in Admin Settings, with persisted, audited provider enable/disable controls. This supersedes any earlier suggestion that an environment-only endpoint switch is sufficient. Google cloud provisioning/budget and live device validation remain separate gates.

See [Geo implementation rollout](GEO_IMPLEMENTATION_ROLLOUT.md) for code scope, migration 067, testing-week setup, native device requirements and remaining verification. Source changes are not a claim of deployment or completed real-device navigation tests.

## 27 October follow-up: modular Nominatim explicit search

The subsequent approved direction adds Nominatim behind PlaceService, retaining
Google as an option. Local testing now selects public Nominatim for explicit
Search/Enter requests, with persistent caching, a shared request cap and OSM
attribution. Public Nominatim must not receive autocomplete or generated/load-test
traffic. Hosted Nominatim and permitted autocomplete remain in `todo.md`.
See [Nominatim setup and migration](NOMINATIM_PLACES.md). This changes search only;
the mobile map, navigation and routing choices below remain separate.

## 1. Locked V1 scope

Launch with Google and OSM/OSRM for the first month. Driver launches first. Keep the initial implementation small, measure actual usage and improve after launch rather than implementing every candidate provider now.

| Capability | V1 choice | Boundary |
| --- | --- | --- |
| Mobile map display | Google Maps SDK | Map presentation; not matching or pricing authority |
| Address/place search | Google Places or Nominatim behind PlaceService | Admin selects the provider; local explicit-search testing uses public Nominatim; hosting/autocomplete are pending |
| Pickup/destination confirmation | Device GPS and user-confirmed pin | A search result is a suggestion; confirm meeting coordinates |
| Driver turn-by-turn navigation | Google Navigation SDK inside Driver | Preserve trip controls; no required jump to Google Maps/Waze |
| Fare distance/duration | OSRM using OSM road data, behind RouteService | Inputs to HeyGo pricing; not Google Navigation billing or route authority |
| Fare rules and quote | HeyGo Pricing Engine | Persist quote inputs/version; navigation must not silently reprice an agreed fare |
| Matching and driver proximity | PostGIS | Eligibility, availability and market rules stay in HeyGo |
| Markets/geofences | PostGIS and approved market boundaries | Provider coverage is not permission to operate in a market |
| Driver location | Device GPS → HeyGo backend | Tracking must continue during navigation and appropriate background states |
| Postcode/building identifiers | NIPOST optional later | Never required to request or complete a ride |

External Google Maps/Waze navigation was considered and rejected as the primary UX. HERE Navigate was considered for embedded navigation; its access/licence process prompted choosing Google Navigation. Switching Places to OSM was discussed and deferred. Search, map display, routing and navigation are separate components: changing one does not automatically replace the others.

## 2. Modular target architecture

```text
Rider app
  MapSurface → Google Maps SDK
  Search → HeyGo Geo API → PlaceService → Google Places
  GPS / confirmed pin ──────────────────────────┐
                                               v
                                     HeyGo trip backend
                                  /          |           \
                          PostGIS       RouteService     Pricing Engine
                         /      \           |                 ^
                    Matching  Markets     OSRM ────────────────┘
                             Geofences   (OSM road graph)

Driver app
  Device GPS → HeyGo location API → PostGIS / trip tracking
  NavigationController → NavigationAdapter → Google Navigation SDK
  Existing offer / pickup / active-trip controls remain accessible

Future adapters (disabled until separately approved and tested)
  PlaceService: Geoapify, Huawei Site Kit, AMap Overseas, OSM search
  RouteService: Geoapify, Huawei, AMap Overseas, Google Routes,
                Valhalla / GraphHopper
  NavigationAdapter: HERE Navigate or another validated embedded SDK
  AddressEnrichment: NIPOST / licensed address datasets
```

These are logical boundaries, not a requirement to deploy a new microservice for every box. Reuse existing backend and Flutter service boundaries. Native map/navigation rendering runs on the device; server API keys remain on the server. Native keys are restricted to app identities and the required SDKs.

### Contracts and implementation rules

- `PlaceService.search(query, bias, locale)` returns normalized candidates: label, address, coordinates, provider and provider reference. `resolve(reference)` resolves the selected result when necessary. Keep provider IDs namespaced; an old Google ID cannot be resolved through a newly selected OSM provider.
- `RouteService.route(origin, destination, profile)` returns meters, seconds, optional geometry/maneuvers, provider, computation time and data/profile version where available. Normalize coordinate order and units. Business logic must not depend on OSRM JSON types.
- `NavigationAdapter` exposes start/update destination, stop, guidance/arrival/error events and lifecycle disposal. Native widgets and SDK setup will still require provider-specific work; an adapter does not make an SDK a configuration-only swap.
- HeyGo owns trip start/completion, payment and cancellation state. Navigation arrival is an input, not automatic proof of trip completion.
- Google Navigation computes its own guidance route. OSRM distance/ETA can differ. Distinguish quote estimates from navigation ETA, and preserve the accepted fare policy.
- Configure each capability independently: provider, enabled state, endpoint, timeout, quotas and explicitly allowed fallback. Validate configuration at startup. Disabled/unconfigured providers must not silently activate.
- No automatic paid fallback in V1. Add one only after agreeing its cost, failure triggers, data/display rights and user experience. Cap retries and prevent fallback loops.
- Use namespaced caches with provider-specific expiry/retention rules. Do not pool Google/Huawei/AMap content into an unrestricted proprietary places database.
- Search/map/navigation combinations require compatible terms and attribution. Track data provenance separately from independently collected HeyGo observations.
- Measure provider, operation, latency, timeout/error rate, billable units and cost per completed selection/trip. Avoid logging raw sensitive queries or complete movement traces unnecessarily.
- Roll out provider changes by test environment and market/cohort, with a rollback switch. Keep in-flight trips on their established navigation/session state where possible.

### Existing source gaps to address

The prior source review found OSRM calls in both `services/trip-service/service/grpc.go` and `services/api-gateway/handler/driver_routes.go`; consolidate their provider mapping behind a shared routing contract. The trip route path defaults to the public OSRM demonstration endpoint. A supported managed or operated endpoint is a launch gate; do not assume the demo server is production infrastructure.

The existing Ride Agent Google adapter uses Text Search, not an established autocomplete flow. Autocomplete/session billing optimization needs an explicit implementation rather than changing a pricing label. Driver map/navigation integration, real keys, permission handling and background tracking still require runtime tests. This document does not claim those changes are shipped.

## 3. Where HeyGo needs geo services

| Area | Required geo function | Cost/control implication |
| --- | --- | --- |
| Rider pickup and destination | Search, selection, pin confirmation, optional reverse geocoding | Debounce search; request minimal fields; explicit sessions where applicable |
| Saved Home/Work and recent places | Recover confirmed location and provider reference | Saved provider IDs may still require paid resolution; retention rules apply |
| Ride quote | Road distance/duration | OSRM + HeyGo pricing; persist quote evidence |
| Dispatch | Nearby eligible drivers, market/package checks | PostGIS first; avoid routing every driver through a paid matrix |
| Driver to pickup / drop-off | Embedded guidance, rerouting, arrival events | Google Navigation destination events; avoid reinitializing on widget rebuilds |
| Live tracking | Timestamped GPS, accuracy and freshness | Device GPS has no Maps API charge; transport/storage still cost money |
| Admin operations | Location display, trip route, service areas and incident context | Web maps can have separate billing from mobile maps |
| Markets / airports / restricted zones | Versioned approved polygons and membership | Operational approval remains authoritative |
| Future pickup intelligence | Correct gate, safe stopping point, reported access restrictions | First-party evidence and review; not an inferred provider guarantee |
| Optional address enrichment | Postcode / building ID | Separate consent, access, rights and paid lookup checks |

## 4. Cost model and controls

Prices are research snapshots from 19 October 2026, in USD, before tax. Recheck account-specific eligibility, terms and prices before activation. Free allowances are shared according to provider billing rules, not a separate allowance for every app/user. Trials are not the long-term operating model.

| Item | Published price / cost basis | Interpretation |
| --- | --- | --- |
| Google native Maps SDK | Unlimited free usage for standard Maps SDK SKU | Map IDs/cloud styling may trigger Dynamic Maps billing; web maps differ |
| Google Navigation | 1,000 free destination events/month; then $25/1,000 in first paid tier | Bills destinations passed to navigation methods, not GPS samples or minutes |
| Google Autocomplete Requests | 10,000 free/month; then $2.83/1,000 first paid tier | Session/termination rules affect actual charges; not equivalent to Text Search |
| Google Text Search Pro | 5,000 free/month; then $32/1,000 first paid tier | Current research baseline; requested fields determine SKU |
| Google Place Details Pro | 5,000 free/month; then $17/1,000 first paid tier | Minimal fields may qualify for another SKU |
| Google Routes Essentials | 10,000 free/month; then $5/1,000 first paid tier | Benchmark only; OSRM remains V1 fare-routing provider |
| OSM / OSRM / PostGIS | Infrastructure, storage, backups, updates and maintenance | Open-source/data access does not mean free operated hosting |
| GPS / backend | Device GPS plus Cloud Run, database, messaging/network resources | No Google geolocation API needed merely to read phone GPS |

Illustrative Navigation-only budget, assuming exactly two destination events per ride (pickup and drop-off): 500 rides → $0; 1,000 → $25; 5,000 → $225; 10,000 → $475. Retries, destination changes, test usage and repeated destination calls can add events. This is not an estimate of the whole geo bill.

Add billing alerts, per-operation counters and carefully chosen API quotas. Budget alerts alone do not stop spending. A quota-induced outage needs an honest unavailable/retry state; it must not create fake distances, successful rides or unsafe guidance. Optimize redundant requests before changing provider.

## 5. Deferred alternatives and what each actually replaces

| Alternative | Potential role | Research finding | Gate before use |
| --- | --- | --- | --- |
| Geoapify | Hosted OSM-based search/geocoding/routing/tiles | Priority alternative; researched free plan 3,000 shared credits/day, 5 requests/sec; credits vary by operation | Recheck commercial terms, daily peaks, local results, attribution and display compatibility |
| Huawei Map Kit / Site Kit / Location Kit | Hosted maps, search, details, geocoding, routing | Priority cost experiment later; published enterprise $300 monthly shared coupon, conditional eligibility, expires monthly | Confirm actual account issuance, eligible kits/region, Nigerian quality, iOS/Flutter capability and combination rights |
| AMap Overseas / Alibaba Cloud | Places, autocomplete, routing and matrix | Overseas offer distinct from mainland AMap; advertised 10,000 monthly free Places and Routes usages, then $2.50/1,000; autocomplete advertised $1.415/1,000 after allowance | Verify Nigerian coverage, exact overseas endpoints, coordinate conventions, account access, matrix unit and ID-resolution billing |
| Nominatim / other OSM search | Address, street, landmark search and reverse geocoding | OSM data can replace Places through a search service; quality depends on mapped data | Hosted plan or operated infrastructure; test addresses/entrances; public Nominatim prohibits autocomplete and caps whole-app traffic at 1 request/sec |
| OSRM | Primary V1 road routing | Road graph routing, distance/duration; not a complete navigation SDK or live traffic service | Production endpoint, regional data/update process, capacity, health checks and monitoring |
| Valhalla / GraphHopper | Future routing engines | Alternatives for different routing requirements | Compare features, compute/operations cost, profiles, updates and local routes; hosted GraphHopper has separate commercial pricing |
| Google Routes | Routing/ETA benchmark or explicit future fallback | Independent API/SKUs; traffic options can change price tier | Measure benefit over OSRM and approve fallback spend |
| HERE SDK Navigate | Embedded turn-by-turn alternative | Navigate access requires contacting HERE / appropriate contract; Explore is not equivalent navigation access | Obtain licence/evaluation credentials, quote and Flutter/device validation |
| Mapbox | Map rendering / native navigation alternative | Flutter maps support and native navigation SDKs are separate capabilities | Confirm supported Flutter navigation integration, bridge maintenance, licence and pricing |
| TomTom | Hosted search, maps, routing or navigation candidate | Commercial alternative researched; no HeyGo price/quality conclusion | Obtain relevant product pricing, Flutter support and Nigeria field results |
| Overture Maps | Open map/place/building datasets | Data source for a future owned search/enrichment pipeline, not a turnkey API | Dataset-specific licence, freshness, local coverage and ingestion/serving cost |
| OpenAddresses | Address datasets | Potential enrichment source, not an address-search API | Nigerian coverage and each source's licence/quality |
| GeoNames | Settlement/place-name gazetteer | Useful for cities/localities; not a precise business/entrance search replacement | Attribution/licence, precision and service limits |
| QGIS | Desktop GIS tooling | Useful for authoring/inspecting market polygons and data QA | Controlled export and approval workflow; not a mobile map/search backend |
| Leaflet | Web map renderer | Useful for a future web/Admin OSM map | Tile provider and attribution; not native Flutter navigation or a places database |
| NIPOST | Postcode/building identity enrichment | Optional official identifiers; not road routing or navigation | Authorized access, lookup price, exact building match and retention/reuse rights |
| Baidu / Tencent | Additional Chinese-provider watchlist | Not selected or established as suitable for HeyGo Nigeria | Overseas availability, commercial access, coordinates, Nigeria coverage and terms need fresh research |

Huawei first-band research snapshot per 1,000: keyword search $22.50, details $12, driving routes $3.50, JS map loads $5, static map API $1.50, raster tiles $0.25; Android SDK map loading advertised free. The $300 is a shared conditional coupon, not $300 per kit and not an assumed HeyGo entitlement. Compare both with-coupon and no-coupon totals.

AMap Places-by-ID was described within the overseas Places offering, but separate details billing/shared allowance was not established. Do not count details as free, or assume a matrix usage equals a whole request. Low published prices do not establish equivalent results or Nigerian navigation availability.

The earlier comparison pack remains at `/private/tmp/outputs/heygo-geo-comparison-20261019/`: Markdown and CSV tables include needs, providers, pricing, conditional Huawei/AMap calculations and 72 **UNRUN** field-test rows. These are historical research scenarios, not the locked V1 bill; they predate the final embedded Google Navigation decision. Temporary files are supporting material, not the durable source of this decision.

## 6. When to explore alternatives and conditions for changing

**Timing:** use the first month to launch and measure. Review after the first 100 completed trips, again around 1,000 trips, and at the end of month one. These are proposed operational checkpoints, not automatic migration thresholds. A safety/reliability failure can require intervention earlier.

| Trigger | First action | Candidate exploration |
| --- | --- | --- |
| Search has repeated missing/wrong local results | Record anonymized cases, confirm pin UX and geographical bias | Geoapify, OSM search, Huawei or AMap on the same queries |
| Places is materially driving the bill | Remove redundant calls, correct sessions/fields, measure cost per completed selection | Search-only provider comparison; keep Google navigation unchanged |
| Navigation cost becomes material | Check duplicate destination calls and usage per ride | HERE quote/evaluation or supported embedded alternative; do not assume OSRM alone replaces navigation |
| OSRM route quality / ETA / uptime is inadequate | Check graph freshness, endpoint capacity, profiles and error evidence | Another operated engine or approved routing provider; traffic benefits measured separately |
| Coupon expiry or provider price/terms change | Reforecast at full recurring price | Compare total operating cost including engineering and support |
| New market has poor coverage | Run local address, entrance and road tests before launch | Market-specific provider only if operational complexity is justified |
| Repeated provider outage / quota exhaustion | Apply bounded retry and honest failure state | Explicitly approved compatible fallback; no hidden paid cascade |

Before any switch, require:

1. A measurable problem and a baseline: actual monthly bill, units per trip/selection, p50/p95 latency, errors, route/ETA accuracy, search success and support complaints.
2. A matched local test set: popular and obscure landmarks, duplicate branches, incomplete street addresses, estates/gates, airport pickup points, inaccessible roads and bad network conditions. Correct branch and safe entrance matter more than merely returning a result.
3. Agreed numerical acceptance thresholds and a geo budget. These remain **to be agreed**, not invented as approved policy. Savings must justify migration and ongoing operations, with and without promotional credits.
4. Confirmed commercial access, Nigeria coverage, Android/iOS support, attribution, display compatibility and permitted caching/retention. No credentials in documentation.
5. Adapter/contract tests, staging and real-device tests, limited rollout, monitoring and a demonstrated rollback. Keep payment, trip state and agreed fare behavior stable.
6. Change one capability at a time. Search, fare routing, map rendering and embedded navigation can evolve independently, subject to compatible provider terms.

## 7. Longer-term HeyGo geo intelligence

Later, first-party evidence can improve safe pickup points, estate entrances, actual travel-time models, access restrictions and driver-reported map issues. Consider a small curated pickup-point dataset before a proprietary mapping platform. Reports and repeated deviations are review signals, not automatic proof that a road or turn is invalid.

Do not build a trace warehouse or ETA model simply because it may be useful someday. Agree purpose, sampling, consent/notice, access and retention first. Latest driver location is not equivalent to a retained trip trace. Keep independently collected HeyGo observations identifiable and separate from provider-restricted content; review derived-database licensing before mixing/distributing datasets.

## 8. Implementation and launch checklist

- Integrate Google Navigation in Driver through an adapter while preserving mounted trip controls and lifecycle.
- Enable only required SDKs/APIs with appropriately restricted keys; establish billing visibility and quotas.
- Validate pickup → drop-off guidance, rerouting, denied permissions, stale GPS, connectivity loss, background/resume and trip cancellation on Android and iPhone.
- Keep tracking and backend trip state authoritative while navigating. Do not repeatedly set destinations on UI rebuilds.
- Normalize OSRM consumers and provision a supported routing endpoint with monitored data updates.
- Wire search through PlaceService; intentionally choose autocomplete versus full text search and fields/session handling.
- Confirm launch market/geofence boundaries and location freshness rules.
- Resolve Driver auth, financial recovery, cancellation and session data isolation blockers in the separate production audit. A map decision does not resolve those blockers.
- Collect first-month measurements, then evaluate only the component that demonstrably needs improvement.

## 9. Research references

Sources researched on 19 October 2026; pricing/access claims must be rechecked before purchase or activation.

- Google prices: https://developers.google.com/maps/billing-and-pricing/pricing
- Google billing triggers: https://developers.google.com/maps/billing-and-pricing/sku-details
- Google Flutter Navigation: https://developers.google.com/maps/documentation/cross-platform/navigation
- Google Navigation Nigeria coverage: https://developers.google.com/maps/documentation/navigation/android-sdk/coverage-nav-sdk
- Google service terms: https://cloud.google.com/maps-platform/terms/maps-service-terms
- OSM licence: https://www.openstreetmap.org/copyright
- Public Nominatim policy: https://operations.osmfoundation.org/policies/nominatim/
- Nominatim search: https://nominatim.org/release-docs/latest/api/Search/
- OSM tile policy: https://operations.osmfoundation.org/policies/tiles/
- OSRM: https://project-osrm.org/
- Geoapify: https://www.geoapify.com/pricing/ and https://www.geoapify.com/pricing-details/
- Huawei published prices: https://developer.huawei.com/consumer/de/hms/price/
- Huawei charging/coupon: https://developer.huawei.com/consumer/en/doc/HMSCore-Guides/about-charging-0000001052557393
- Huawei pricing: https://developer.huawei.com/consumer/en/doc/HMSCore-Guides/pricing-0000001061986005
- AMap Overseas: https://www.alibabacloud.com/en/marketplace/amap?_p_lc=1
- HERE editions/access: https://docs.here.com/here-sdk/docs/flutter-introduction-editions
- Mapbox mobile SDK scope: https://docs.mapbox.com/help/dive-deeper/mobile-apps/
- TomTom: https://docs.tomtom.com/pricing
- NIPOST API: https://docs.postcode.gov.ng/ and terms: https://www.postcode.gov.ng/terms
- QGIS: https://qgis.org/ ; Leaflet: https://leafletjs.com/
- OpenAddresses: https://openaddresses.io/ ; Overture: https://overturemaps.org/ ; GeoNames: https://www.geonames.org/

---

## Historical brainstorming — retained for context, not current approval

The text below is the earlier conversation. Its recommendations and example architectures may conflict with the locked decision above. Preserve it as research history; use sections 1–8 for V1 planning.

where can OSM come in here?

Is  OSM possible? i read grab started withOSM then now they havetheir ownmap after usingOSM for a longtime
Yes. OSM is absolutely possible for heyGo, and after checking Grab's actual engineering material, your understanding is broadly right — with one important nuance:

Grab didn't simply “leave OSM and build a completely separate map.” OSM became part of the foundation from which Grab built a proprietary mapping stack and proprietary hyperlocal intelligence. Grab's engineering team explicitly says, “GrabMaps is based on OpenStreetMap (OSM).”

That is actually a very interesting blueprint for heyGo.

Where OSM fits
Think about maps as several separate products:

Base road map → OSM

Roads, intersections, road classifications, one-way roads, street names, etc.

↓

Map rendering → heyGo

We control how the map looks inside the rider/driver apps.

↓

Routing engine → heyGo/open source

OSRM / Valhalla / GraphHopper can calculate:

A → B

using the OSM road network.

↓

heyGo intelligence

This is where our proprietary advantage develops:

actual driver GPS traces
actual trip times
pickup/drop-off points
traffic patterns
road speeds by time
bad pickup locations
airport pickup zones
estate entrances
road restrictions
driver corrections

↓

Google / another provider

Fallback for things we cannot confidently answer.

So OSM can actually sit underneath the entire heyGo location stack.

And Grab is a very good precedent
Grab says it began incorporating OSM into its ecosystem years ago, and by 2019 was already describing itself as a major OSM user and contributor in Southeast Asia.

Then something interesting happened.

Their drivers were generating huge amounts of information that generic mapping providers didn't have.

For example, Grab says its system detects situations where drivers repeatedly don't follow the route the map expects. That can indicate a road blockage or map problem. Grab can then investigate and update the mapping information.

They eventually built:

GrabMaps

which became their own mapping/location platform. In 2022 Grab announced that it expected to become fully self-sufficient with GrabMaps.

Today they describe their base map as being built from millions of transactions, partners, their mapping community and other sources.

And Grab is still interacting with OSM. They say they've mapped more than 900,000 km of previously missing roads and added them to OpenStreetMap.

So this isn't really:

OSM → abandon OSM → GrabMaps

It's closer to:

OSM → OSM + Grab data → Grab mapping infrastructure → proprietary Grab location intelligence

That distinction matters enormously for us.

heyGo could follow a similar path
Initially:

OSM + external services

Then:

OSM + heyGo driver data + external services

Then:

OSM + heyGo routing + heyGo location intelligence + limited external services

Eventually:

heyGo Maps / heyGo Geo

OSM provides the underlying open geographic dataset while heyGo provides the Nigerian mobility intelligence.

And we don't have to wait until we're enormous.

For example, imagine 5,000 heyGo drivers moving around Lagos every day.

Every phone already generates:

latitude
longitude
timestamp
heading
speed

Over time, we can discover:

Road speed

Drivers consistently travel this road at 17 km/h around 6 PM.

Road problems

OSM says drivers can turn here.

But:

96% of heyGo drivers don't.

Flag it.

Pickup intelligence

Passengers keep placing their pin here:

●

Drivers actually stop here:

●

After 600 trips, heyGo learns:

Recommended pickup point → ●

That's incredibly important in Lagos.

Generic mapping might know where Ikeja City Mall is.

heyGo needs to know:

Where should a ride-hailing driver actually meet someone at Ikeja City Mall?

Those are two different mapping problems.

There's one important catch with OSM
OSM data is free. OSM's infrastructure isn't a free production backend.

The OpenStreetMap Foundation explicitly says the data has no licensing/usage fee, but its public tile servers are not intended to act as an unlimited commercial map service. At scale you should use a provider or operate your own infrastructure.

So we shouldn't build:

heyGo app → free openstreetmap.org server

That's not the architecture.

We build:

OSM data

↓

our/provider infrastructure

↓

heyGo

The cost then becomes mostly infrastructure rather than paying Google per API transaction.

OSM also uses the ODbL license, which means attribution and share-alike/database obligations need to be designed into how we combine and distribute map databases. We should have counsel review the exact data architecture before we start mixing proprietary mapping datasets with OSM-derived databases.

But that doesn't mean all heyGo trip intelligence suddenly becomes public. There are important distinctions between OSM-derived databases, produced works and independently collected data; that's something we'd deliberately architect around rather than casually mixing everything into one database.

This changes what I would recommend for heyGo
I wouldn't actually start Google-first anymore.

I'd investigate an OSM-first, Google-fallback architecture.

Something like:

                    heyGo Geo Layer
                          │
       ┌──────────────────┼─────────────────┐
       │                  │                 │
    Search             Routing           Maps
       │                  │                 │
       ▼                  ▼                 ▼
  heyGo Places      Valhalla/OSRM      OSM-based map
       │                  │
       └──────────┬───────┘
                  │
            OSM road graph
                  │
          ┌───────┴────────┐
          │                │
     heyGo data       External APIs
          │            (fallback)
          │
 GPS traces
 trip history
 traffic speeds
 pickup points
 road corrections
 driver feedback
And I'd add one thing Grab didn't have when it started: AI.

Driver doesn't need to edit maps.

Driver can say:

“You can't enter Admiralty from this side anymore.”

Or:

“Pickup for this estate is actually the second gate.”

Or:

“This road floods when it rains.”

heyGo turns those reports + GPS evidence + other drivers' behavior into structured candidate map updates.

That potentially gives us a Nigeria-specific mobility map built passively by the driver network.

And that ties directly into something we've already achieved: building the driver community before launch becomes more valuable than just having drivers available for trips. They can eventually become our distributed mapping network.

So I think OSM should be seriously evaluated as a core piece of heyGo's architecture, not merely as a cheap Google Maps replacement.

The next thing I'd do is design the actual heyGo mapping stack for launch — exactly which parts use OSM, which server/software we need, which parts still need Google/Mapbox/etc., and what we'd need to change in the existing rider and driver apps. That will tell us whether this is practical now, rather than just theoretically attractive.

My problem is trying to be perfect from thr beginning
Yes — and in this particular case, trying to perfect the mapping stack before launch could become a serious distraction.

heyGo does not need GrabMaps on Day 1. It needs reliable pickup → matching → routing → trip completion. The mapping architecture can evolve behind that experience.

I’d simplify the decision to this:

Launch with the easiest reliable mapping stack. Instrument everything. Optimize when the bill justifies it.

You can still make one architectural decision now that protects the future: don't tightly couple heyGo's business logic to Google. Put a small internal abstraction between heyGo and whichever mapping provider you're using.

So instead of your application doing:

Rider App
   ↓
Google Places
Google Routes
Google Geocoding
do:

Rider / Driver Apps
        ↓
    heyGo Geo API
        ↓
 ┌───────────────┐
 │ Search        │ → Google initially
 │ Geocoding     │ → Google initially
 │ Routing       │ → Google initially
 │ ETA           │ → Google initially
 │ Map display   │ → Google/OSM
 └───────────────┘
        ↓
  heyGo trip data
Then six months later you can change one component:

Routing:
Google → OSM + Valhalla
without rewriting the ride system.

Later:

Search:
Google → heyGo Places → Google fallback
Later:

ETA:
Google → heyGo ETA model → Google fallback
And eventually:

Google dependency
████████████████ 100%

        ↓

████████░░░░░░░░ 50%

        ↓

██░░░░░░░░░░░░░░ 10%
That's enough future-proofing.

More importantly, don't build the map-data collection system yet either. Just retain the useful first-party trip telemetry you're legitimately allowed to retain: GPS traces, requested pickup coordinates, actual pickup coordinates, destination coordinates, timestamps, trip duration, etc., with appropriate user consent/privacy controls.

You can figure out what to do with that dataset later.

Because right now, a month spent engineering an OSM routing cluster to save $200 in API fees would be irrational. When heyGo is doing enough rides that Google sends you a $10,000 or $50,000 bill, that's a wonderful problem. It means the product is working.

So I'd put “heyGo Maps” in the architecture, not on the current roadmap.

Your current roadmap stays focused on getting people into cars.

And when scale arrives, the data you've accumulated gives us the raw material to progressively build the Grab-style mapping layer we were just discussing. That's the balance: don't build for millions of rides before you've done thousands, but don't architect yourself into a corner either.

I mean i am trying to be perfect but really i dont have to be. We just have a community and could explain to them. Things will take shape as we move on so no rush.  So its ok not to start perfectly. Or figure our architecture that doesnt use more than the the free trials before switching
Yes. I think that's the healthier way to frame where heyGo is right now.

You don't need to have the final architecture before the first real trip. You need an architecture that's good enough to learn without creating an expensive mess.

And importantly, your community changes the equation. You're not quietly launching something and pretending it's a mature Uber replacement. You've been involving drivers in shaping heyGo already. You can be transparent:

“We're building this with you. Some things will change as we learn what works.”

That's completely compatible with a serious product—as long as safety, payments, trip integrity, and core reliability aren't treated as experiments.

For maps specifically, I would stop trying to solve the eventual $1-million-a-year problem today.

Give engineering a simple constraint
Instead of saying:

“Design our ultimate mapping architecture.”

Say:

“Get us through validation at minimal mapping cost, while keeping providers replaceable.”

That's a much smaller problem.

For the first version, we can use Google's free monthly usage allowances/credits where applicable, monitor every API call, set hard budget alerts/quotas, and avoid unnecessary calls. Google currently gives each Maps SKU its own monthly free usage cap rather than the old blanket $200 monthly credit—for example, many Essentials SKUs have 10,000 free billable events per month.

Then our progression can simply be:

VALIDATION
Google / easiest reliable solution
↓
Measure actual API usage per trip
↓
First 100 trips
↓
First 1,000 trips
↓
Now we have REAL numbers
↓
Identify what is actually expensive
↓
Replace that component with OSM/open-source/provider B
↓
Repeat
That's fundamentally different from us sitting here trying to predict exactly what heyGo's mapping architecture should look like at 1 million trips.

We might discover that Places search is the expensive part.

Or Route Matrix.

Or routing.

Or something we aren't anticipating.

Then we optimize that.

There is one thing I'd still build correctly now: a thin provider abstraction. Don't scatter Google-specific calls throughout the rider app, driver app, pricing engine and matching engine. Have something like GeoService.search(), GeoService.route(), GeoService.geocode(), GeoService.matrix() internally. Google can sit behind those methods today; OSM/Valhalla/another provider can sit behind them tomorrow.

That isn't overengineering. It's a relatively cheap escape hatch.

And then ship.

You already have enough big things to validate: whether riders request rides, whether drivers accept, whether your upfront pricing works, whether 0% commission changes driver behavior, whether ETAs are acceptable, whether matching works, whether payment directly to drivers works, etc.

Mapping infrastructure shouldn't delay finding those answers.

So yes: heyGo is allowed to be Version 1. In fact, it should be.

You can tell the community that explicitly. Then every few hundred trips, the product gets a little smarter. That's considerably more realistic than trying to manufacture the system heyGo will need three years from now before you've run the first thousand rides.

Architecture for version 1
For heyGo V1, I would deliberately make the architecture boring. The goal is: launch, stay within/near free allowances during validation, collect the right first-party data, and preserve an easy path to OSM later.

heyGo V1 architecture
                    ┌─────────────────────┐
                    │     RIDER APP       │
                    │                     │
                    │ Search destination  │
                    │ Request ride        │
                    │ Track driver        │
                    └──────────┬──────────┘
                               │
                               ▼
                    ┌─────────────────────┐
                    │    heyGo Backend    │
                    │                     │
                    │ Trip Service        │
                    │ Matching            │
                    │ Pricing             │
                    │ Geo Service         │
                    │ Driver Location     │
                    └──────────┬──────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
          ▼                    ▼                    ▼

 ┌────────────────┐   ┌────────────────┐   ┌─────────────────┐
 │ DRIVER LOCATION│   │  GEO SERVICE   │   │    TRIP DATA    │
 │                │   │                │   │                 │
 │ Phone GPS      │   │ Search         │   │ Pickup GPS      │
 │ lat/lng        │   │ Geocode        │   │ Dropoff GPS     │
 │ heading        │   │ Route          │   │ Actual route    │
 │ speed          │   │ ETA            │   │ Duration        │
 │ availability   │   │ Distance       │   │ Time/date       │
 └───────┬────────┘   └───────┬────────┘   └─────────────────┘
         │                    │
         │                    ▼
         │             ┌──────────────┐
         │             │    GOOGLE    │
         │             │              │
         │             │ Places       │
         │             │ Geocoding    │
         │             │ Routes       │
         │             │ Maps         │
         │             └──────────────┘
         │
         ▼
 ┌────────────────┐
 │ heyGo Database │
 │ + Redis/Geo    │
 └────────────────┘
The important architectural decision is that Google is a service provider, not heyGo's architecture.

1. Map display
For V1, I'd use whatever is already easiest and stable in your current apps.

Don't rebuild it.

If you're already using Google Maps SDK, keep it.

Later:

Google map
   ↓
OSM-based map
can happen independently.

2. Destination search
V1:

Rider types:

"Landmark..."

        ↓

Google Places Autocomplete

        ↓

Place + coordinates
Don't build heyGo Places yet.

Do basic optimization: don't start hammering autocomplete after the first character, debounce typing, use sessions correctly, restrict results geographically where appropriate, and request only the fields we actually need.

3. Driver GPS — heyGo owns this
This is one thing I would not send through Google.

Driver phone:

GPS
 ↓
lat
lng
speed
heading
timestamp
 ↓
heyGo Backend
That's normal device location data.

heyGo maintains the live driver state.

4. Nearby driver matching — don't use Google
Suppose there are 500 online drivers.

Rider:

      ●
We shouldn't ask Google to calculate routes from all 500.

Use geospatial proximity:

500 online drivers
       ↓
Drivers within X km
       ↓
20 candidates
       ↓
Rank/filter
       ↓
Maybe top 5–10
PostGIS is excellent for this. Redis GEO is another straightforward option.

Only after the shortlist do we need more expensive road/ETA calculations.

That alone can prevent a lot of unnecessary map API usage.

5. Fare calculation
Your pricing engine should receive:

distance
duration
pickup
destination
It shouldn't know or care whether those came from Google.

So:

GeoService.route(A, B)

returns:

{
 distance_km: 13.4,
 duration_minutes: 31
}
Pricing then uses those numbers.

This is important because of the fare simulator/formula we've been working on. Mapping provides inputs; heyGo determines economics.

6. Driver navigation
I would not build navigation in V1.

This is somewhere we can save enormous engineering effort.

Driver receives:

Pickup: Lekki Phase 1

and taps:

Navigate

Then launch their installed navigation app with the destination coordinates.

Google Maps/Waze/etc. handles turn-by-turn navigation.

We're building ride-hailing, not navigation software.

Eventually heyGo could have embedded navigation. Not now.

7. Trip tracking
During the ride:

Driver phone
     ↓
GPS updates
     ↓
heyGo
     ↓
Rider sees driver moving
Again, Google doesn't need to process every location update.

The map is merely displaying coordinates.

8. Start collecting the future dataset immediately
This costs almost nothing architecturally and gives us options later.

For each trip:

requested pickup
actual pickup

requested destination
actual dropoff

estimated distance
actual GPS distance

estimated duration
actual duration

GPS trace

time/date

driver ID
trip ID
With appropriate retention, consent and privacy controls.

Don't try to build intelligence from it yet.

Just don't throw the data away.

The V1 cost philosophy
I'd actually give engineering a rule:

Every external map request must have a reason.

We log:

provider
API
trip_id
request_type
timestamp
success
latency
estimated_cost
Then we can eventually open Admin and see:

MAP USAGE

Trips                 3,284

Places requests       12,481
Geocoding requests     2,930
Route requests         9,202
Matrix elements       18,443

Map API cost           $XX

Cost / trip            $0.0X
Now we aren't guessing.

At 100 trips we know something.

At 1,000 trips we know much more.

At 10,000 trips we'll know exactly what needs replacing.

Then OSM comes later
This is why the GeoService layer matters.

Today:

GeoService
    │
    ├── Search ───── Google
    ├── Geocode ─── Google
    ├── Route ───── Google
    └── Matrix ──── Google
Maybe later:

GeoService
    │
    ├── Search ───── Google
    ├── Geocode ─── OSM-based service
    ├── Route ───── Valhalla + OSM
    └── Matrix ──── Valhalla + OSM
Later still:

GeoService
    │
    ├── Search ───── heyGo Places
    │                  ↓ fallback
    │                Google
    │
    ├── Geocode ─── heyGo/OSM
    ├── Route ───── heyGo/OSM
    ├── ETA ─────── heyGo model
    │
    └── Traffic ─── heyGo driver network
Rider app doesn't change.

Driver app doesn't change.

Pricing doesn't change.

Trip service doesn't change.

We're just replacing what's behind GeoService.

That's the only future-proofing I'd insist on for V1.

Everything else can evolve.

And I'd explicitly not build OSM servers, custom routing, custom geocoding, proprietary traffic prediction, map editing, turn-by-turn navigation, or a GrabMaps-style system right now.

Get the first rides moving. The first 1,000 trips will teach us more about the mapping architecture heyGo actually needs than another month of architecture discussions will.

Nigeria launched its post code. Could this be useful against google
Yes — potentially very useful for heyGo, but not as a complete replacement for Google.

Nigeria's new system is much more interesting than a traditional postcode. On October 1, 2026, NIPOST launched the National Digital Alphanumeric Postcode System: every addressable building gets a unique 11-character code tied to a precise GIS location.

Even more relevant: NIPOST explicitly intends businesses to integrate it. Its site advertises APIs, web/mobile SDKs and bulk access, and NIPOST has already started inviting innovation hubs to apply for API access/tokens.

That makes me rethink part of the V1 architecture.

Don't think "postcode vs Google"
Think:

                   heyGo Location

          ┌──────────────┴──────────────┐
          │                             │
     WHERE IS IT?                 HOW DO I GET THERE?
          │                             │
          ▼                             ▼
  NIGERIA POSTCODE                    OSM
  + heyGo Places                      Routing
          │                             │
          └──────────────┬──────────────┘
                         │
                         ▼
                       heyGo
                         │
                  Google fallback
NIPOST itself makes essentially this distinction: the Digital Postcode identifies the building/location; Google Maps and other mapping services can still provide navigation.

And that division is extremely useful to us.

Imagine a rider enters:

LA 12 K23 IV 95

heyGo asks the NIPOST API:

Postcode
    ↓
NIPOST
    ↓
Latitude: 6.xxxxx
Longitude: 3.xxxxx
Building/address
Now we've obtained the destination without Google Places or Google Geocoding.

Then:

Coordinates
     ↓
OSM road network
     ↓
Valhalla / OSRM
     ↓
Route + distance + ETA
Potential Google calls:

ZERO.

That's where this gets interesting.

And heyGo can make postcode adoption useful
I wouldn't force riders to type postcodes. That would destroy the UX.

Instead, give them several ways:

Where to?

🔎 Search for a place
📍 Choose on map
🏠 Enter postcode
⭐ Saved places
If someone gives their postcode once:

FC02A09DB09
      ↓
heyGo resolves it
      ↓
Save as:
"Home"
From then on:

Home

No search API at all.

Same for:

Work

Gym

Mum's house

Church

Airport

etc.

That's exactly the kind of small optimization that compounds.

There's potentially an even bigger opportunity
Remember what we were discussing about building heyGo Places eventually?

Nigeria just gave us a national addressing primitive.

Instead of inventing our own identifier:

heygo_place_827491
we can potentially associate our place intelligence with the national building reference:

NIPOST CODE
     │
     ├── coordinates
     ├── building
     │
     └── heyGo intelligence
            │
            ├── recommended pickup
            ├── entrance
            ├── driver notes
            ├── pickup popularity
            ├── accessibility
            └── historical trip data
For example:

Eko Hotel
National postcode: XXXXX...
             │
             ▼
heyGo knows:
• Main entrance coordinates
• Preferred ride-hailing pickup
• Drivers enter from X road
• Average pickup delay: 4 min
• Best pickup point after 10 PM
NIPOST provides the identity of the location.

OSM provides the road network.

heyGo eventually provides the mobility intelligence.

That's a very compelling combination.

But I would NOT bet V1 entirely on it yet
The system is literally two weeks old.

NIPOST says businesses can integrate, but its own materials indicate that some developer/business integration services are still being finalized.

And there are already reports of rough edges around text address → postcode search, even while postcode → building lookup appears to work much better.

So I'd treat it as an opportunity, not a dependency.

For V1:

heyGo GeoService

SEARCH
├── Saved heyGo places
├── NIPOST postcode
└── Google Places              ← fallback/general search

LOCATION ID
├── NIPOST                     ← where available
└── lat/lng

DRIVER LOCATION
└── heyGo GPS

NEARBY DRIVERS
└── heyGo/PostGIS

ROUTING
├── Google initially
└── OSM later

NAVIGATION
└── External navigation app

MAP
└── Existing solution
Then we monitor NIPOST.

If the APIs become stable and adoption grows, we move it upward.

And there's something else I'd do now rather than later: contact NIPOST.

Not to negotiate some huge partnership. Just tell them:

heyGo is building a Nigerian ride-hailing platform and wants to integrate the National Digital Postcode into pickup and destination selection.

They are actively looking for organizations to integrate the system.

That could potentially get heyGo early API access, technical support, and perhaps put us among the early mobility platforms implementing Nigeria's new addressing infrastructure.

NIPOST Digital Postcode developer/integration site

And that is worth pursuing. Not because it eliminates Google tomorrow, but because NIPOST + OSM + heyGo's own trip data could eventually eliminate a surprisingly large portion of what we're paying Google to do.

So the location is tied to post code. Why would they need to type post code
Exactly. They shouldn't need to type the postcode. That's the more important implication.

If NIPOST's system gives every addressable building a unique code associated with geographic coordinates, then the postcode can function as backend location infrastructure, not rider-facing UX.

The rider experience can remain completely normal:

Where to?

"15 Admiralty Way, Lekki"
          ↓
       heyGo
          ↓
Find location/address
          ↓
National Postcode / location record
          ↓
      lat + lng
Or even simpler:

Rider drops pin
      ↓
GPS coordinates
      ↓
heyGo
      ↓
Determine corresponding postcode/building
      ↓
Store:
coordinates + postcode + heyGo place ID
The rider never needs to know the code.

This is where it becomes interesting against Google
One of the things Google provides is the translation between human concepts and coordinates:

"Eko Hotel" → coordinates

"15 Admiralty Way" → coordinates

coordinates → "15 Admiralty Way"

The national postcode system potentially gives heyGo another authoritative location identifier at the building level.

So we could eventually have:

                    Rider Search
                         │
              "The Wheatbaker Hotel"
                         │
                         ▼
                  heyGo Places
                         │
              ┌──────────┴─────────┐
              ▼                    ▼
        NIPOST location           OSM
        / building ID        place/road data
              │                    │
              └──────────┬─────────┘
                         ▼
                     lat / lng
                         │
                         ▼
                       ROUTE
Google becomes the fallback when our own sources can't resolve what the rider means.

There is one distinction, though: having every building geocoded doesn't automatically solve search.

If someone types:

"Chicken Republic Lekki"

the postcode database might know the building and its coordinates, but we still need something capable of understanding which business/place the rider means.

That's where Google Places is extremely powerful.

But heyGo can progressively build that layer.

For example, the first time:

"Chicken Republic Lekki"
        ↓
Google Places
        ↓
coordinates
        ↓
heyGo associates location
        ↓
NIPOST building/postcode
        ↓
heyGo Places
Later another rider searches the same thing:

"Chicken Republic Lekki"
        ↓
heyGo Places
        ↓
FOUND
        ↓
No Google call
Subject, importantly, to Google's restrictions on storing/caching Google-derived Places data—we'd want the durable heyGo record grounded in data we're actually permitted to retain or independently collect.

And then driver/rider behavior improves it:

Building location
        │
        ├── official coordinates
        │
        └── heyGo pickup point
                  ↓
            Gate 2, not Gate 1
That second part is ours.

So your instinct is correct: I wouldn't put "Enter postcode" prominently into the heyGo UX at all.

The postcode can be invisible infrastructure.

The user just searches, taps a place, drops a pin, selects Home/Work, or shares a location. Behind the scenes heyGo can increasingly resolve that into:

National location identity + coordinates + heyGo mobility intelligence.

That makes the new Nigerian postcode system considerably more interesting to us than merely giving riders another thing to type.
