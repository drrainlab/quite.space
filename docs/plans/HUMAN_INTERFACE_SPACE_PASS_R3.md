# Quiet Spaces — Human Interface & Space Pass (UI-волна, ревизия 3 — финал)

## Context

UX/UI ТЗ «Human Interface & Space Pass»: увести протокол (events/principal/
fingerprints/schema) под «человеческий» слой (70% знакомого мессенджера / 20%
атмосферы / 10% механики), стиль **Quiet Terminal Glass**, Space Pass как
фирменная механика входа, Protocol view для технарей. Решения юзера:
async-принятие пропуска, EN + i18n-словарь (RU следом), все 4 гейта.

**Главная правка после ревью юзера**: Space Pass — НЕ временный ключ
пространства, а **подписанное право постучаться**. Пропуск НЕ содержит
epoch-ключей; открытие пропуска ничего не расшифровывает. Формула:
«Space Pass не открывает пространство — он позволяет безопасно постучаться;
после принятия устройство владельца включает новое устройство в
криптографический круг». Это чище крипто-семантики, честнее метафоры и
делает пропуск QR-компактным.

## Space Pass = Join Pass (протокол)

Вся крипта существует (HPKE wrap, epoch rotation, relay dead-drop). Новое —
асинхронный, идемпотентный, crash-safe поток вступления.

### Формат пропуска (bearer-secret, подписан terminal-ключом space, canonical CBOR)

```
version, kind="quiet-spaces-pass",
space_id (32Б; используется в API/логах/маршрутизации),
space_signing_pub (32Б; ОБЯЗАТЕЛЬНАЯ проверка SHA256(space_signing_pub)==space_id — bootstrap trust),
acceptor_device_id, acceptor_key_id, acceptor_device_xpub (32Б — HPKE-адрес владельца),
rendezvous_token (random 16–32Б — адрес dead-drop, НЕ связан с pass_id),
pass_id (= SHA256("qs.pass.id.v1" ‖ bearer_secret)[:16]),
issued_at, expires_at, max_uses, membership_profile="member.v1",
nonce, extensions{}, signature
```

**bearer-secret** (32 случ. байта) НЕ в теле пропуска на диске — только в
ссылке/QR; `pass_id` выводится из него. НЕТ pass-keypair, НЕТ epoch-ключей,
НЕТ manifest-фрейма. Domain-separated подпись ("qs.pass.v1"). Parser:
неизвестные top-level поля → reject; `extensions` — ignore неизвестных
non-critical, `critical_extensions` → reject если не поддержаны; лимиты строк;
clock-skew допуск. **QR-бюджет: комфорт ≤800–900Б, hard ≤2КБ** (тест длины +
реальный телефон-скан). Ссылка: `quietspaces://join#p=<base64url>` +
web `.../join#p=…`; клиент СРАЗУ `history.replaceState` после парса (fragment
остаётся в history/скриншотах), pending-intent переживает onboarding/рестарт.

### Поток (async state machine, crash-safe saga)

1. **Mint** (`terminals.NewPass`): bearer_secret → pass_id; реестр в keystore
   (append key `passes`: pass_id, space, expires, max_uses, used, revoked +
   **saga-журнал по request_id**, см. ниже). Acceptor = controller device.
   max_uses v1: 1 или ≤10 (без «anyone» — нужен rate-limit сначала).
2. **Open+Request** (получатель): проверить SHA256(space_signing_pub)==
   pass.space_id, verify domain-sep подписи + expiry → создать
   identity если нужно → HPKE-конверт на acceptor_device_xpub с
   `membership.join.requested.v1` {request_id, pass_id, **bearer_secret**,
   device_id, device_xpub, display_name, device_signature}; отправить на
   rendezvous_token (direct LAN или relay dead-drop).
   `request_id = SHA256(pass_id ‖ device_id ‖ nonce)`.
3. **Wait**: получатель НИЧЕГО не читает (эпох нет). UI: явные состояния.
4. **Accept** (controller-реплика, at-least-once delivery + idempotent
   crash-safe acceptance). Saga-журнал по request_id, шаги переживают
   рестарт: `received → validated (pass_id из secret == реестр; device
   signature; **pass валиден в момент резервирования — clock новичка не
   доверяем**) → use_reserved (атомарно check+consume) → member_added
   (AddMember) → epoch_rotated (RotateEpoch, новый epoch завёрнут и на
   device_xpub) → member_event_published (`membership.member.added.v1` —
   каноническое событие В ЛОГЕ, подписано acceptor, шифр новым epoch:
   {request_id, pass_id, device_id, display_name, accepted_by, accepted_at})
   → response_persisted → response_published (`membership.join.accepted.v1`
   HPKE на device_xpub: {request_id, space manifest frame, epoch_n, wrapped
   epoch}) → completed`. Повтор request_id → вернуть СОХРАНЁННЫЙ ответ, не
   повторять шаги. **Single authoritative acceptor** = controller (terminal
   seed); мульти-owner — задокумент. ограничение v1.
5. **Ready** (получатель): absorb accepted → открыть реплику с epoch_n →
   sync. **History v1 = с момента принятия** (past-эпохи не заворачивались).
6. **Confirmation = автоматическое** (устройство владельца по правилам
   пропуска). UI: «Waiting for Alice's device to confirm» → «Entry confirmed»
   (НЕ «Alice accepted» — это звучит как ручное решение). Ручной
   owner_confirmation — отдельный режим позже.
7. **Close/Revoke pass** ≠ **Remove member**: revoke — пометка, блокирует
   новые запросы И ещё-не-принятые pending, НЕ трогает принятых, НЕ ротирует.
   Remove member — отдельная операция: удалить + RotateEpoch + событие.
8. **Acceptor key lifecycle**: ротация acceptor-ключа **автоматически
   отзывает все связанные passes** (v1-правило, честнее хранения старого
   ключа).
9. **Expiry-while-waiting**: запрос отправлен до expiry, но владелец получил
   после → состояние `expired_while_waiting`, UI «The pass expired before the
   owner's device could confirm — ask for a new pass».

Схемы (canonical, три РАЗНЫЕ): `membership.join.requested.v1` (внешний
HPKE-конверт владельцу), `membership.join.accepted.v1` (внешний HPKE-ответ
новичку), `membership.member.added.v1` (каноническое событие пространства —
через него ВСЕ участники узнают о новом члене; human projection →
«Bob entered with Alice's pass»). Rendezvous-лимиты: max pending/pass, max
envelope size, rate-limit per rendezvous_token. Тесты: mint→request→accept→
newcomer-syncs; идемпотентный повтор (один use/member/rotation, тот же
accepted); single-use; revoke блокирует pending; expiry-while-waiting;
**pending НЕ даёт чтения до accept**; member.added виден другим участникам;
acceptor-key-rotation отзывает passes; dead-drop через relay; QR size.

## Реализация — 4 гейта (коммит + живая проверка на каждом)

### Gate UI-0 — Human Shell + Glyph Core

- **Разбить index.html** → `assets/`: index.html, styles.css, app.js,
  glyphs.js, i18n.js (go:embed берёт директорию).
- **Human projection layer**: `projectEntryToHumanModel(entry, ctx)` строит
  UI-модель ДО DOM (schema-имена не текут в разметку). `renderEntry(entry,
  {mode:'human'|'protocol', locale, members, connection})`.
- **Glyph Core v1** (glyphs.js): детерминированный SVG из id-хэша, типы
  human/device/space/pass, кэш по хэшу, 24px + large; заменяют hex в
  members/сообщениях/списке уже здесь (иначе UI-1/2 зависят назад).
- **Токены Quiet Terminal Glass** (§5–8): canvas #0d0a10, непрозрачные
  content-surfaces, glass blur 20–32px ТОЛЬКО nav/modal/composer/Pass;
  палитра orchid/mint/amber; радиусы §8.1; 4px-шкала; sans для текста, mono
  только статусы/фразы/время. Архетип-палитры пространств приводятся к
  системе (акцент = подмешанный тон).
- **i18n foundation** (§9 ревью): `t('key',{count})` с plural/interpolation/
  Intl relative-time, НИКАКОЙ конкатенации; fallback locale; dev-проверка
  недостающих ключей. EN-словарь; RU — следом.
- **Терминология** (§10): весь UI через t(); нет events/principal/peer/schema/
  duplex. Author→display name (member card, fallback глиф+«member»). `→card`
  → hover «Save as card».
- **Layout** (§9): desktop 3 колонки (сворачиваемая правая), header «N people
  here · direct connection» (клик→детали), компактный «● Direct · 2 here»
  вместо `LAN :… peers`; человеческие системные события.
- **Protocol view** (§18, осторожная миграция): legacy-рендерер изолирован,
  feature flag (localStorage), НЕ приводить к new UI-kit сразу; fixtures для
  сравнения human/protocol на одном наборе entries.
- Quiet by default: unread — тихая цифра (localStorage last-read clock).

### Gate UI-1 — Identity & First Run

- **Бэкенд**: разделить cryptographic identity и display profile. API
  `GET /api/onboarding`, `POST /api/identity/name` {display_name} →
  self-манифест rev+1 + republish (participant умеет ревизии); platform-
  neutral fallback device name; имя меняется без смены ключей.
  `--passphrase` остаётся (текст честно объясняет: шифрование keystore).
- **Экраны** (§11.1–13): welcome (Enter with a pass / Create a space; «no
  phone, no email»); имя→авто-identity→подтверждение с human glyph; пустые
  «It's quiet here so far…»; список = глиф+имя+последняя понятная активность+
  unread; device-экран «This device / verification code: pine·dusk·orbit·
  seven» (фраза из fingerprint-словаря) + «Technical details» (hex внутри).

### Gate UI-2 — Join Pass

- **Бэкенд**: terminals/pass.go (NewPass/DecodePass/BuildJoinRequest/
  AcceptJoin/BuildAccepted); ТРИ схемы (requested/accepted/member.added);
  node MintPass/RevokePass/ListPasses + saga-acceptor в absorb/rendezvous-
  poll + реестр с журналом. API: `POST /api/spaces/{sid}/passes`
  {max_uses(1..10), ttl_hours}, `GET` (список+состояние), `DELETE
  /api/spaces/{sid}/passes/{pass_id}` (revoke≠remove), `POST
  /api/join-requests` {pass} → {request_id, status:"waiting_for_owner"},
  `GET /api/join-requests/{request_id}` (poll saga-состояния). Remove-member
  — `DELETE /api/spaces/{sid}/members/{device}` (rotate+событие).
- **UI** (§12 + async): Invite → готовый безопасный Join Pass (1 вход, 24ч,
  профиль member.v1): **Pass card** glass с pass-glyph, названием,
  «invitation from Alice», verification phrase (`moss·echo·violet` из
  словаря), QR, «1 entry · valid 24h»; Share/Copy/Show QR/Configure(who
  1|10, ttl 1h|24h|7d — БЕЗ прав-настройки: не показываем то, что не
  enforce'им)/Close. **Join — явная машина состояний** (created/opened/
  join_requested/waiting_for_owner/accepted/syncing/ready/rejected/
  expired/expired_while_waiting/revoked/used): Enter → «Request sent —
  waiting for Alice's device to confirm your entry. You can close this
  window.» → (confirmed) «Entry confirmed — the space is ready [Open]» →
  (owner offline) «Alice's device is offline; your request waits and will be
  delivered when one of you reconnects» → (expired-while-waiting/revoked/
  used) тексты §12.7. Секрет только в ссылке/QR; `history.replaceState`
  сразу после парса; pending-intent переживает onboarding.
- Старый device-id+xpub инвайт → Protocol view (для железа/отладки).

### Gate UI-3 — Living Space

- **Living Glyphs**: bot/IoT-типы + анимация + morphing + presence-reactions
  + reduced-motion варианты (поверх Glyph Core из UI-0).
- **Presence** (§15): человеческие состояния, «Alice is here · Bob was here
  7 min ago», компактный «○ Just here» вместо dropdown; устройства/боты из
  member cards.
- **Motion** (§16): 100–450мс, glass fade+rise, доставка — короткая линия;
  prefers-reduced-motion + reduced-transparency (непрозрачные поверхности).
- **A11y** (§19): focus-ring везде, ARIA icon-кнопки, Esc+возврат фокуса,
  контраст ≥4.5, target ≥40px, tab-порядок; **Mobile** (§20): ≤600px
  список↔пространство экранами, context bottom sheet, composer снизу, pass
  полноэкранный. Виртуализация ленты — НЕ в M1 (отмечено).

## Не входит (§24 + ревью)

Редактор пространств, темы, marketplace, AR/NFC, соц-поиск, 3D; RU-перевод
(словарь готов); Instant Pass (advanced-режим позже, честно помеченный);
granular rights-enforcement; мульти-owner-acceptor; OS deep-link протокол;
виртуализация ленты.

## Verification

- Go-тесты: pass lifecycle (mint/request/accept/newcomer-sync/idempotent-
  retry/single-use/revoke≠remove/expiry-while-waiting/pending-no-read/
  member.added-виден-другим/acceptor-key-rotation-отзывает/relay-deaddrop/
  QR-size≤900Б), три схемы, keystore saga-реестр, name-change манифест rev,
  human projection fixtures (schema-имена не текут в модель).
- Сценарии приёмки A–F (§23) вживую, две вкладки: создание без ключей; вход
  по пропуску-ссылке → «request sent» → (владелец онлайн) «accepted» → первое
  сообщение; QR-вход; revoked → человеческая ошибка; offline «will send
  later»; Protocol view туда-обратно.
- DoD §26: нет raw fingerprints/schema в основном пути; пропуск ≤2 действия;
  pending действительно pending (крипто-проверка: до accept реплика
  недоступна); клавиатура/фокус/reduced-motion; скриншоты desktop+mobile.
