# ADR-016 — Public access: signed projections, revisable policy

Status: accepted · Wave: PA (rev 1, gates PA-0.1–PA-1.4)

## Invariant

> **Публичность Space — это подписанная ПРОЕКЦИЯ его журнала, а не потеря
> zero-custody. Приватная граница задаётся при создании и неизменна.
> Relay остаётся слепым. Уже опубликованное необратимо.**

Публичный доступ раскладывается на ТРИ независимые оси:

- **discovery** — виден ли Space в каталоге (`visibility`: private →
  unlisted → public);
- **read** — можно ли читать содержимое без членства (unlisted/public);
- **participate** — кто может писать (broadcast = owner + curators;
  community = любой присоединившийся).

Space Pass остаётся ТОЛЬКО механизмом входа в приватный круг и не
изменяется этой волной.

## Ground model

Публичный контент — это подписанный **PLAINTEXT** (не epoch-encrypted):
strangers проверяют его по ключу терминала, который И ЕСТЬ id
пространства (ADR-001). Приватные пространства продолжают epoch-sealing
без изменений — `terminals.Space.Private` гейтит запечатывание в `Emit`.

Публичное распространение идёт через **relay public mailbox**: владелец
кладёт подписанную проекцию под `HintPublicOutbox(space, bucket)`; любой
с id пространства тянет её через `Fetch` (недеструктивно, many-readers).
Contributors шлют свои фреймы в `HintPublicIngress(space, bucket, shard)`;
владелец сливает их `Collect` (деструктивно) и вносит в канонический
журнал. Relay видит только непрозрачные байты и hint'ы.

## Invariants (I1–I9)

1. **Manifest authority** — состав/policy задаёт ТОЛЬКО подписанный
   манифест; `SpaceMeta.Visibility` — кэш для UI и никогда не управляет
   криптографией.
2. **No spam in the log** — неавторизованные фреймы отсекаются admission
   gate ДО каноничного журнала; попадают лишь в `PolicyStats`.
3. **Single write gate** — все emit-пути проходят один низкоуровневый
   гейт в `Emit` (reader-replica / frozen / non-writer).
4. **Single-writer durable seq** — проекцию публикует только владелец;
   `ProjectionSeq` durable и растёт при смене content-digest.
5. **Atomic Replace** — relay-проекция заменяется атомарно (`msgReplace`).
6. **Independently installable projection** — свежий reader
   материализует УСЕЧЁННУЮ проекцию с нулём недостающих предшественников
   (dependency-closed выбор фреймов, gap-tolerant absorb).
7. **Authenticated metadata** — `PublicProjectionEnvelope` подписан
   ключом пространства; подпись связывает seq + truncation + весь состав.
8. **At-least-once ingress** — канонический журнал контрибьютора = durable
   pending-очередь; ack = его EventID в подписанной проекции.
9. **Checkpoint continuation completeness** — проекция самодостаточна для
   reducer'ов, устойчивых к порядку (ADR-004).

## Decisions

### 1. Visibility tiers и режимы
`private` (по умолчанию, epoch-sealed) → `unlisted` (читаемо по ссылке,
не в каталоге) → `public` (читаемо + видимо в каталоге). Режимы записи:
**broadcast** (`publish=curated`: owner + attested curators) и
**community** (`join=open`: любой присоединившийся). Комбинации
валидируются envelope'ом в `policy.go`.

### 2. Media custody (PA-0.4D)
Медиа-фрейм СРАЗУ входит в журнал (цепочка не рвётся), но скрыт из
проекции `Exclude`-фильтром, пока владелец не скачает и не проверит blob
по хэшу у автора. Статус custody живёт в content-addressed store, НЕ в
состоянии reducer'ов — иначе materialization зависела бы от порядка.

### 3. Policy revisions (PA-1.1)
Владелец пересобирает и переподписывает манифест
(`Space.ReviseManifest`): `Revision+1`, `Previous = Hash(cur)`, подпись
ключом пространства — точный паттерн `Participant.Rename`.
- Разрешено: unlisted↔public, broadcast↔community, add/remove curator,
  freeze on/off.
- **Запрещено**: любой переход через приватную границу. Крипто-режим
  пространства неизменен; «вернуть в truly private» не обещается.
- **Anti-rollback**: `manifestSupersedes` требует строго большего
  `Revision`; равный ревижн обязан быть тем же фреймом; при `held+1`
  проверяется `Previous`-hash-link; бóльшие пропуски терпимы (проекции
  несут лишь последний манифест).
- **Curator binding** — точная пара `WriterBinding{Principal, Device}` в
  `qp.writer=`; конкретное устройство публикует сразу. Мульти-устройство
  куратора и авто-дистрибуция сертификатов — post-beta.

### 4. TRUE freeze
`qp.frozen=1` — ортогональный флаг. Пока заморожено: отвергаются ВСЕ
content-emit'ы, включая владельца; единственное разрешённое действие —
ревизия, снимающая freeze. Ingress не сливается; чекпоинт не меняется,
идут только heartbeat'ы. Contributor-клиенты, получив frozen-манифест,
ПРИОСТАНАВЛИВАЮТ ingress-push (pending остаётся локальным, durable — I8) и
возобновляют после разморозки. Readers видят честный статус `FROZEN`, а
не гадают о сбое relay.

### 5. Determinism после ревизий
`reducers.State.Authorized` (defense-in-depth) действует ТОЛЬКО пока
`Manifest.Revision == 1 И начальная policy = curated`. После ЛЮБОЙ
ревизии он навсегда отключается для этого пространства — цепочка
broadcast→community→broadcast не должна ретроактивно прятать контент,
законно принятый в community-фазе. Вся авторизация после ревизии — на
admission gate; replay одинаков на старых и свежих репликах.

### 6. Каталог = broadcast-пространство (PA-1.2)
Каталог — это обычное публичное broadcast-пространство, чьи посты —
**space-cards**: publication-документ `kind:"space"` с title/summary/
cover/tags и share-ссылкой в первом link-блоке под схемой `qs:<token>`.
Никаких новых сущностей и серверов. Discover открывает настроенный
каталог; клиент несёт дефолтную официальную ссылку, но любой может вести
свой каталог (федеративный шов почти бесплатен).

### 7. Ingress hardening (PA-1.3)
Owner-side защита при сливе: per-author (по claimed signer device)
token-bucket на фреймы и байты за цикл + общий потолок цикла; `rejectedRing`
(LRU+TTL) отбрасывает повторно запушенный плохой фрейм ДО верификации.
Легитимный контент не теряется — over-cap фреймы остаются в durable
pending и приходят следующим циклом.

## Accepted risks (beta)

- **R1 — необратимость ссылок.** Публичная/unlisted ссылка = bearer-токен;
  кто её получил, читает вечно. UI честно предупреждает один раз при
  копировании и в тултипе бейджа.
- **R2 — squatting id.** Кто угодно создаёт пространство с любым
  названием; аутентичность seed-пространств — через официальный каталог,
  не через имена.
- **R3 — media privacy.** Открытие media-карточки в публичном
  пространстве раскрывает device-id качающего автору blob'а (custody
  wants). Медиа on-demand, не eager.
- **R4 — freeze не отзыв.** Freeze приостанавливает публикацию, но не
  стирает уже скачанное у читателей.
- **R5 — single-device publisher.** Публикует одно устройство владельца;
  мульти-устройство — post-beta.
- **R6 — каталог-курация ручная.** Владелец/кураторы каталога добавляют
  карточки вручную; открытая очередь заявок — post-beta.

## Deferred (post-beta)
Открытая очередь заявок каталога; серверный поиск сверх клиентского
фильтра; confirm-join; авто-дистрибуция device-сертификатов и
мульти-устройство кураторов; mirrors + полная история; two-layer spaces;
private↔public переходы.
