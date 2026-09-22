# LT-4 — «слово уходит сразу», один раз и навсегда

*Status: approved by the owner 2026-09-22, in progress. The plan as approved; amendments are appended, never rewritten.*

Repo: `/Users/gurebu/Coding/projects/quiet_places`. Ветка работы: `main` (локально уже лежат
незакоммиченные правки вечера 2026-09-22 — см. «Точка старта»).

## Context

Владелец: «доставка на реле секунд 15 с телефона», потом на стенде — «точка» висит
минутами и уходит только при рестарте процесса. За вечер найдены и измерены
пять причин друг под другом (все уже исправлены точечно, rc1…rc12, не закоммичены):
автовыбор реле держал петлю (и линию отправки) до успешного раунда проб;
линия отправки делила с циклом сокет и мьютекс; проход шёл по всем пространствам;
после Doze — полуоткрытые сокеты; петлевой маршрут стенда. Но под ними лежит
**архитектурная грязь, из-за которой всё это вообще возможно**:

1. **Курсоры «что уже предложено пиру» живут только в памяти** (`node/offers.go`
   `r.offers`, `rs.lastLen`, `rs.legacyBasis`; сводки пиров `kernel/sync` `peerSum`).
   После каждого перезапуска первый push заново предлагает каждому участнику
   **всю историю каждого пространства на каждом эндпоинте**; слово едет за ней.
   Книга к тому же ключуется `[tid][dev]`, а не по эндпоинту: устройство,
   угаданное на трёх реле, получает полную историю на каждом проходе.
2. **Линия отправки почти никогда не на своей линии**: `deliverSpaceRouted`
   выбирает bulk-линию по размеру ВСЕГО лога (`relay.go:757-770`), а не дельты —
   любое пространство >64 КиБ шлёт 400-байтовое слово через bulk вместе с
   выкачкой проекций и медиа.
3. **Квитанции (✓✓) ходят только с циклом**, после всей публичной работы:
   ✓✓ отстаёт от экрана получателя на цикл (2 с / 60–180 с). По LAN квитанций
   DR-1 нет вовсе (`keyReceipt` в kernel/sync — custody-квитанция моста).
4. **Цикл забирает личную почту последней**, после публикации/чтения всех
   публичных пространств.
5. **Гигиена маршрутов**: `recordStatedReturnRoutes` принимает любой host:port
   (loopback тоже) и удаляет прочие заявленные маршруты пира; квитанции, гранты,
   `mailboxTags` читают `ks.PeerRoutes` сырыми, без фильтров; собственный
   `SelfIngress` никогда не забывает адрес и анонсирует loopback-историю всем.
6. Две скрытые ошибки дельта-книги: (а) курсор = число custody-кадров, а
   `skippable` включает `Env.Expired` (`relay.go:486`) — истёкший кадр под
   курсором сдвигает счёт и **прячет новый кадр**; (б) метка заявленного маршрута
   не истекает, а копия на реле — да (48 ч TTL; store реле в памяти —
   `transports/relayserver/store.go`, ни одного файлового вызова): офлайн >48 ч
   или рестарт реле = кадры больше не предлагаются никогда.

Цель: отправка = одна дельта на своей линии, без ожидания чего бы то ни было;
после рестарта узла — ноль повторной истории; ✓✓ через секунды; маршруты чистые.
Честное ограничение: кадры, потерянные реле (его рестарт, TTL 48 ч у офлайн-получателя),
предлагаются заново суточным полным проходом — до 24 ч без сигнала от реле.
Ограничения: ADR-033 (wire-ключи append-only, смысл не меняется; локальные
документы свободны), ADR-008 (статус выше `accepted_by_relay` только по
подписанной квитанции получателя), ADR-020 (маршруты; ограда до T4 — не снимаем).

## Решения (после двух независимых разборов)

| вопрос | решение | почему в одну строку |
|---|---|---|
| единица курсора | **индекс лога** (`l.order`, append-only) | счёт custody-кадров сдвигается при истечении (баг 6а) |
| где живёт книга | **sealed-документ** `Root.SaveSealed("offers", …)` | keystore переписывается целиком на каждый save; json — картина соцграфа (ADR-020) |
| `ks.Delivered` | **пол по своим кадрам**, не курсор | это позиция в моём чейне, о чужих кадрах не говорит |
| срок метки | **24 ч от последнего ПОЛНОГО предложения (`FullAt`), не от касания; без исключений** | реле хранит в памяти (TTL 48 ч); в активном пространстве касания не дают сроку истечь, а квитанция о моём чейне не доказывает сохранность чужих кадров в ящике |
| `rs.lastLen` | оставить (детектор роста, тир) | иначе резолв маршрутов на каждый тик для 99 % тихих пространств |
| `rs.legacyBasis` | удалить, признак — в записи книги | одно место правды |
| PutMany (новый verb) | **отложить** (S6, после реле) | боль сегодня не в числе RTT; нужен деплой реле раньше клиента |
| квитанции по LAN | новый sync-msg `msgReceipts` (подписанные DR-1) | сводка — не proof-event по ADR-008 |
| retirement SelfIngress (T4-lite) | **не в этой волне** | тихая потеря почты у пиров, не выходивших неделю (ограда ADR-020) |
| уведомление реле «забрали» депозитору | **нет** | ADR-008 + утечка «когда получатель онлайн» |

## Точка старта (уже сделано вечером, лежит незакоммиченным)

`node/relayprobe.go` (старт на last-known-good сразу), `node/relaypool.go` (lanes
`outbox`/`inbox`), `node/relay.go` (`viaOutbox` thread, `pullFromRelayVia`),
`node/relaysync.go` (`outboxMu`, `pushSpacesVia`), `node/outbox.go` (said-set,
onWake на первой неудаче, лог прохода), `node/foreground.go` (onWake при возврате
>20 с, `DoorbellRing`), `node/routes.go` (`routableFrom`), `android/quietcore/core.go`
(RollingLog на Android, status `relay.armed/pulled/sync_error`), Kotlin: foreground
truth после open, tail лога узла. Тесты вечера зелёные выборочно; **полный набор
`./node/` на текущем дереве красный в двух медиа-тестах** (`TestMediaMatrixAcrossTwoRelays`,
`TestNoSiblingCacheDependency` — «friend NEVER saw the want» для video/file).
**Шаг 0 — разобраться с ними до всего остального** (подозрение: said-only проход
линии отправки съедает want-кик из `assets_index.go:573`, у которого нет `noteSaid`,
когда в said-множестве уже лежит другое пространство; либо флейк — проверить
на 6600463). Коммитить вечерние правки одним коммитом только после зелёного.

## Порядок работы (решение владельца)

Шаг 0 → **S1–S3** (быстрая доставка) → **S5** (устойчивость после рестарта) →
**S4** (быстрые ✓✓) → **S7** (наблюдаемость) → релиз 1.0.26. **S6 — отдельный релиз.**
Главный риск волны — принять квитанцию о доставке *части* истории за доказательство
сохранности *всей* истории; поэтому срок метки без исключений и инвариант курсора выше.

## Slices (каждый отдельный коммит, отдельно откатываемый)

### S1 — линия отправки по размеру дельты (~15 строк) — `node/relay.go`
- `biggest` считать по кадрам, реально выбранным для **наименьшего** курсора среди
  получателей эндпоинта, не по всему логу; `viaOutbox` → `withRelayOutbox`, bulk
  только когда сама дельта ≥ `bulkThreshold`.
- **Явно: до S5 «дельта» = по курсору в памяти.** В живом процессе слово едет на
  express-линии; после рестарта первый проход к каждому пиру — полная история —
  по-прежнему bulk (и это правильно: она большая). Тест S1 проверяет живой процесс,
  а не рестарт; ожидание «после рестарта тоже express» появляется только с S5.
- Разделить получателей эндпоинта на `express` (дельта мала) и `bulk` (первый
  контакт/большая дельта) — две горутины, две линии; первый контакт никогда не
  стоит перед словом.
- Пропускать `withLane(ep, …)` вовсе, когда у всех получателей эндпоинта нет тел и
  нет fleeting/wants (после рестарта — ноль дозвонов).
- Тест: `TestTheOutboxSendsTheDeltaOnTheExpressLane` (история >64 КиБ, цикл
  зажат `rs.beforeCycle`, слово у боба <2 с, `pool().peer(addr).bulk.client == nil`);
  `TestAFirstContactDoesNotHoldTheExpressLane`.

### S2 — один выбор маршрута для всех (~40 строк) — `node/routes.go`, `receipts.go`, `grants.go`, `knock.go`, `revoke.go`, `relay.go`
- Вынести closure `route` из `deliverSpaceAnnouncingVia` в
  `func (r *Runtime) routeFor(dev, alive, syncingAt) chosenRoute{eps, guess, legacy}`
  и `courtesyRoute(dev) (ep, guessed)` = `rankedPeerRoutes(dev)[0]` иначе свой реле.
- Заменить сырые чтения `ks.PeerRoutes`: `receipts.go:203,215-224`,
  `grants.go:531,550,567,579-586` + `mailboxTags`, `knock.go routeForDevice`,
  `revoke.go:184`. `guessRelays` — отбросить `!routableFrom`.
- Ingest `recordStatedReturnRoutes` (`relay.go:2068-2080`): отвергать
  `!routableFrom(ep, own)`, порт 0, link-local/multicast; пустой набор по-прежнему
  ничего не удаляет (закрепить тестом).
- Advertise: `returnRoutes` из `SelfIngressRoutes()` фильтровать `routableFrom`
  (свой loopback анонсируется только в loopback-мире).
- Исключение стенда в `routableFrom` (own == "" или own loopback → пускать) — обязано
  остаться, иначе весь набор красный.
- Тесты: `TestReceiptsAndGrantsUseTheDialableRoute`,
  `TestAStatedLoopbackRouteIsNotRecordedOffTheBench`, `TestALoopbackIngressIsNotAdvertisedOffTheBench`.

### S3 — порядок цикла (~15 строк diff) — `node/relaysync.go relaySyncOnce`
1. `PullFromRelay(armed addr)` — личная почта первой; 2. `pushSpaces` (анонс «я
переехал», забранный в п.1, используется этим же циклом); 3. публичная работа как
есть (ingress-collect **до** publish — внутренняя зависимость сохраняется);
4. pull с остальных исторических ингрессов; 5. `offerGrants`, `sendReceipts`
(сеть-подстраховка), sparse-планы. `ownOK/pullOK/failStreak` едут с п.1/4;
`r.stopped()` — после пуллов.
- Тест: `TestThePersonalPullRunsBeforeThePush` (боб анонсирует переезд на B; один
  `alice.relaySyncOnce(A)` доставляет слово Алисы на B).
- Следить: бюджеты `TestAConvergedSpaceStopsRemailingItsHistory` (`>8`), settle-окно t6.

### S4 — квитанции по приходу (~120 строк) — `node/receipts.go`, `relay.go`, `lan.go`, `node.go`, `kernel/sync/sync.go`
- `sendReceipts` → `receiptsOwed(only map[tid]bool) []receiptItem` (чистая функция
  над логами и `receipts.sent`) + `deliverReceiptsRelay(items, viaOutbox)` (маршрут
  через `courtesyRoute`, **пропускать авторов, живых на LAN** — `r.lanPeerDevice`;
  только на пути «по приходу», путь цикла как был — иначе ✓✓ по LAN пропадёт совсем);
  `sendReceipts()` = owed(nil) → deliver(false) — остаётся сетью.
- Relay: `applyRelayItems` собирает `touched` пространства; в конце
  `pullFromRelayVia` **после освобождения inbox-линии** — `go deliverReceiptsRelay(receiptsOwed(touched), true)`
  на outbox-линии. Честно сказать в коммите: каждая квитанция = один Put в ящик
  автора = один звонок его телефону (coalesce 1 мин ограничивает).
- LAN: `kernel/sync`: `msgReceipts = 10`, `keyReceipts = 13` (append-only; старый
  пир пропускает неизвестный тип — `Handle` без `default`), `EncodeReceiptsMessage`,
  `Engine.OnDeliveryReceipts`, `Engine.SendReceipts(ep, receipts)`. В `node.go` рядом с
  `OnCustodyReceipt`: `installReceiptsLocked`. В LAN-pump после `Handle` с applied>0 —
  отправить owed-квитанции авторам-пирам этой ссылки. Переключатель
  `Settings.DeliveryReceipts` гейтит LAN как и реле.
- **Объединение квитанций:** не «квитанция на каждый pull», а debounce ~1 с после
  последнего применённого кадра, по одной квитанции на `(space, author)` с максимальной
  позицией (max-merge на приёме делает это бесплатным). Проверить тестом, что 20
  сообщений подряд дают ≤2 Put квитанций и ≤2 звонка телефону автора:
  `TestABurstOfMessagesIsOneReceiptNotTwenty` (счётчик `PutsTotal` реле + `pushRegs` probe).
- Ожидание: ✓✓ ≈ 5 RTT + 1 с debounce (≈1–1,5 с на хорошей сети), в фоне — по звонку, не по циклу.
- Тесты: `TestAReceiptLeavesOnArrivalNotOnTheCycle` (цикл боба зажат; `Delivered`
  у Алисы растёт <3 с), `TestALanPeerReceiptsOverTheWireNotTheRelay` (t6-стенд:
  ящик боба на реле не растёт, `Delivered` растёт), `TestTheReceiptSwitchSilencesThisDevice`
  остаётся зелёным; kernel/sync: `TestReceiptsMessageRoundTrip`, `TestAnOldPeerSkipsAReceiptsMessage`.

### S5 — долговечная книга предложений (~250 строк) — `node/offerbook.go` (new), `kernel/storage/offers.go` (new), `node/offers.go`, `relay.go`, `relaysync.go`, `routes.go`, `node.go`
- Запись: `offerKey{space, dev, endpoint}` → `offerMark{Cursor int /*log index*/, Guess, Legacy bool, At int64 /*последнее касание*/, FullAt int64 /*последнее ПОЛНОЕ предложение истории (from == 0)*/}`.
  Кодек в `kernel/storage/offers.go` по образцу `routes.go` (`offerRecFields = 7`,
  arity-prefix, хвост `SkipItem`), версия документа `v:1`; неизвестная версия/битый
  файл = пустая книга + одна строка лога. Имя sealed-файла **без префикса tid**
  (`forget.go deleteSealedFor` удаляет всё, что содержит префикс пространства).
- Лимит 4096 записей, вытеснение по `At`. Запись: debounce 2 с (образец
  `notifyledger.flush`), синхронный flush из `Runtime.Close` **до** закрытия `r.stop`
  (иначе guard «умирающий не пишет» съест последний flush), и сразу после прохода,
  положившего полную историю (`from == 0`).
- Правило курсора: нет метки → 0; **`now-FullAt > 24 h` → 0** (полный проход раз в
  сутки от последнего ПОЛНОГО предложения, а не от последнего касания — иначе в
  активном пространстве дельты обновляют `At` и срок не истекает никогда, а кадры,
  потерянные реле при рестарте, не предлагаются повторно); `Cursor > Log.Len()` → 0;
  иначе `Cursor`. `At` остаётся только для вытеснения записей. Метка guess на том же
  эндпоинте годится для stated-поиска (тот же ящик), обратное — нет. **Срок действует
  всегда** — никакого «быстрого пути» по квитанции: подписанная квитанция говорит о
  моём чейне, а не о сохранности чужих кадров в конкретном ящике реле после его рестарта.
  Честная цена, в цель волны: **без сигнала о рестарте реле повторное предложение
  потерянных кадров может занять до 24 ч** (сигнал — boot-id реле в `MsgProbeOK`,
  append-only ключ — отдельный шаг позже).
- Два пола на выборе кадров получателю `dev`: `f.dev != dev` (свой чейн держит по
  построению) и `!(f.dev == self && seq <= ks.Delivered[tid][dev])`; плюс
  `flipped` как сейчас. Пол по квитанции уменьшает повторную отправку **только моих**
  кадров; чужие кадры после истечения метки предлагаются заново — это цена честности.
- **ИНВАРИАНТ ПРОДВИЖЕНИЯ КУРСОРА (главный в S5):** курсор `(space, dev, endpoint)`
  двигается только после `PutOK` от реле **на каждое тело** этой группы, отдельно для
  каждого ящика; при частичном успехе (третье тело из пяти отбито) курсор остаётся на
  последнем полностью подтверждённом кадре; при ошибке — не двигается; падение
  процесса между `PutOK` и записью книги даёт ПОВТОР (дедуп по event-id у получателя),
  никогда — пропуск. Книга пишется debounce'ом, но всегда *позже* PutOK, никогда раньше.
  Тест: `TestACrashBetweenPutAndTheBookRepeatsAndNeverSkips` — тестовый хук в
  `deliverSpaceRouted` роняет проход после N-го Put (или закрывает реле посреди
  группы), затем reopen: боб получает каждый кадр ровно по разу, ни один не потерян;
  `TestAPartialPutAdvancesTheCursorOnlyToWhatWasAccepted`.
- Регресс квитанции (`Pos < Delivered` в `installReceipts`) = устройство восстановлено
  из бэкапа → `forgetDevice(dev, false)`.
- Инвалидация точечно: `recordPeerRouteLocked` при вытеснении legacy →
  `forgetDevice(dev, legacyOnly)`; `recordStatedReturnRoutes` → `forgetEndpoints(dev, inNew)`;
  `forget.go` → `forgetSpace`. `resetOffers` в цикле (смена route-gen) — очищать и
  документ на диске (иначе рестарт воскресит стёртое); `TestALiveDeviceWithNoRouteIsGuessedAtEveryOfficialRelay`
  переводится на `forgetDevice`.
- `rs.legacyBasis` удалить; `legacyBasis bool` из сигнатур `deliverSpace*` убрать.
- Загрузка в `Open` между `LoadKeystore` и `applyRelaySync`, после реконструкции пространств.
- Тесты: `TestARestartDoesNotRemailTheHistory` (5 слов, Close, reopen тот же dir,
  6 циклов: `mailboxCount(bob)` Δ0, `PutsTotal` Δ≤2; затем Say → приходит ровно дельта),
  `TestOfferMarksAreKeyedPerEndpoint`, `TestAnExpiredFrameUnderTheCursorDoesNotHideANewOne`,
  `TestAMarkUntouchedForADayIsReoffered`, `TestAReceiptShrinksTheReoffer`,
  `TestABookThatDoesNotDecodeIsAnEmptyBook`, `TestAReceiptRegressionForgetsTheDevice`.
  Меняются: `TestAConvergedSpaceStopsRemailingItsHistory` (бюджет `>8` → `>4`).

### S6 — PutMany (после S1–S5, после деплоя реле) — `transports/relay/wire.go`, `client.go`, `relayserver/server.go`, `node/relaypool.go`, `relay.go`
- `MsgPutMany = 19`, `MsgPutManyOK = 20`; поля — существующие ключи `Hints`(5),
  `Expires`(3), `Body`(4); `RelayProtocolVersion = 3`, min 1. Сервер: все hints
  валидны, ≤64, списать N из write-бюджета, all-or-nothing по квоте, `ring` по
  каждому. Клиент: `PutMany`; `relayPeer.noPutMany` на 1 ч при «unknown message type»
  → per-hint Put. Группировка в express/bulk по `(from-group, body)`.
- Тесты: `TestPutManyLaysOneCopyPerHint`, `TestPutManyIsAllOrNothingOnQuota`,
  `TestPutManySpendsOneWritePerHint`, `TestAnOldRelayGetsPerHintPuts`.

### S7 — наблюдаемость (~90 строк) — `node/relaydiag.go`, `node/outbox.go`, `clients/web-ui/assets/app.js`, `i18n.js`
- `RelayDiagnostics.Outbox{LastPassMs, LastPassAgoS, Express, Bulk, Streak, LastErr}`;
  строка лога прохода: `outbox: pass spaces=%d pushed=%d express=%d bulk=%d held=%d failed=%q took=%s`.
- Панель (`app.js:609-615` уже рендерит 3 строки latency при открытии листа): пока
  лист настроек открыт — обновлять каждые 3 с; 5 строк + «typical → relay N мс → ✓✓ M мс»
  (медианы) + строка outbox («last pass N ms · express E · bulk B», «retrying ×k»).
  Ключи EN+RU (`catalogs.cjs`).

## Что явно НЕ делаем в этой волне (и почему)
- Retirement `SelfIngress` (T4): потеря почты пиров, не выходивших >48 ч; отдельная
  волна с правилом «слышали пира через текущий ингресс».
- Уведомление депозитору «забрали»: ADR-008 и утечка присутствия.
- Удаление `rs.lastLen`.

## Verification
1. Шаг 0: `go test ./node/ -run 'TestMediaMatrix|TestNoSiblingCache' -count=3` на
   текущем дереве и на `6600463` (git worktree) — флейк или регресс; починить до S1.
2. Каждый slice: `go vet ./...`, `go test ./node/ ./transports/... ./kernel/...`
   (узел ~10 мин), новые тесты названы выше; перед тегом — `-race` по `node`, и
   `t6_lan_offload`, `outbox`, `relaywake`, `relaylisten`, `offerbook` по 20 раз.
3. Совместимость: сборка 1.0.25 открывает каталог с `sealed/offers` (инертен);
   новая — каталог без него; keystore arity 27 не тронут; kernel/sync старый пир
   пропускает `msgReceipts`.
4. Стенд `~/.quiet-stand` (fa=Robert:8501 fatok, fb:8502 fbtok): рестарт fa →
   `PutsTotal` реле не растёт; Say → одна дельта.
5. Телефон владельца (без гонок замеров, один вечер): `am crash` → открыть → Say:
   строка `outbox: pass … took=` и панель «→ relay / ✓✓» в 1:1 и в 4-местном
   пространстве; ✓✓ не отстаёт от экрана получателя на цикл.
6. Релиз 1.0.26 после шага 0, S1–S3, S5, S4, S7 — в этом порядке; S6 — 1.0.27 после
   обновления трёх реле.
   Заметки `docs/releases/1.0.26.md`, копия плана в `docs/plans/LT-4-A-WORD-LEAVES-FOR-GOOD.md`,
   `docs/plans/AN-1-NOTIFICATIONS-FINDINGS.md` дописать; ротация ключа FCM — по-прежнему
   за владельцем.

## Amendments (appended as the work went; the plan above is as approved)

- **2026-09-23, шаг 0.** Два медиа-теста (`TestMediaMatrixAcrossTwoRelays`,
  `TestNoSiblingCacheDependency`) зелёные по два раза и на дереве, и на 6600463;
  в полном наборе падали под нагрузкой (параллельно шла сборка APK). Флейк
  нагрузки, не регресс. Полные наборы гоняются строго по одному.
- **S2, грабли:** `ResolvePersonalRelay()` берёт `r.mu` (через `GetSettings`).
  Вызов под замком в `deliverSpaceRouted` дал самоблокировку: join зависал на
  «waiting_for_owner», пакет упирался в таймаут. Правило: резолверы — только
  ДО `r.mu.Lock()`, рядом с `SelfIngressRoutes()`.
- **S2, `statableEndpoint`:** заявление принимается только от устройства с
  сертификатом (`r.ident.certificateFor`) — тест с выдуманным device id
  ничего не проверяет; тесты используют настоящего пира.
- **S3:** «доставка по догадке держится как догадка» теперь закреплена на фазе
  push отдельно (`held_report_test`), потому что целый цикл с pull-first уже
  успевает выучить заявленный маршрут из чужого push — что и было целью порядка.
- **S5, ключ метки = ящик (space, dev, endpoint):** из этого следует, что при
  смене знания о маршрутах книгу чистить НЕ надо (новый эндпоинт — новый ключ,
  курсор 0 сам собой); `resetOffers()` остался только для тестов; обнуление
  `lastLen` для legacy-basis пространств при смене route-gen сохранено — оно
  и заставляет push случиться. `rs.legacyBasis` пока оставлен (признак legacy
  дополнительно пишется в запись книги).
- **S5, арность записи = 8** (space, device, endpoint, cursor, guess, legacy, at,
  fullAt); в плане стояло «7» — арифметическая описка при удалении `OwnTop`.
- **S5, суточный полный проход:** одного правила курсора мало — цикл коротит
  пространства без нового (`lastLen`); стало: `offerBook.staleSpaces()` перед
  push обнуляет `lastLen` таких пространств. Тест
  `TestAMarkUntouchedForADayIsReoffered`.
- **S5, флажок guess/stated в поиске:** метка ищется по ящику независимо от
  флага (тот же ящик — те же байты); флаг хранится для диагностики.
- **S1 + пол по квитанции:** у пира с включёнными квитанциями «полная история»
  после потери книги почти пуста (мои кадры отфильтрованы квитанциями) — тест
  первого контакта выключает квитанции у боба, чтобы bulk-линия была наблюдаема.
- **S4, как сделано:** `receiptsOwed(only)` + `deliverReceipts(items, arrival)`;
  приход = `noteArrival(tid)` из `applyHeldRelayItem` (applied>0) и из LAN-pump
  после `Handle` (applied>0); debounce 1 с (`receiptDebounce`) — один таймер на
  узел, множество пространств; по приходу автор, живой на LAN, получает квитанцию
  по ссылке (`Engine.SendReceipts`, sync-msg 10 / ключ 13), остальные — Put в
  ящик на outbox-линии; при неудаче LAN-отправки автор LAN пропускается (сеть
  цикла, никогда реле с этого пути). Цикл (`sendReceipts`) = owed(nil) → deliver(false)
  на control-линии — сеть как была. Замер в тесте: ✓✓ дома через ~1,3 с после
  pull получателя при зажатом цикле; залп из 20 слов = 1 Put квитанции.
  Таймер прихода гасится в `Close` до `r.stop`.
- **S7, как сделано:** счётчики `outboxExpress/outboxBulk` (ящиков на линии,
  только `viaOutbox`), запись последнего прохода `outboxPass` → `OutboxDiag`
  в `RelayDiagnostics.Outbox` (`last_pass_ms/ago_s`, spaces/pushed/held,
  express/bulk, streak, last_error, тоталы с открытия); `LatencyTypical` —
  медианы по ledger (−1 при <2 кадров); панель строится во фрагмент и
  подменяется целиком, обновляется каждые 3 с пока лист виден
  (`offsetParent`); 5 строк последних сообщений вместо 3. Harness
  `scripts/webui/honesty.cjs` научен фрагменту/таймеру и проверяет строки.
- **Побочное:** защёлка «custody lost» теперь помнит и называет первую причину
  (`custodyLostErr`, refusal `custody_lost`) — до этого второй вызов возвращал
  голую фразу без причины, и флейк t6 в петле ×8 был нечитаем.
- **Форма коммита:** разрезать рабочее дерево по hunk'ам на S1…S5 не вышло
  (базовый патч не собирался: поля `node.go` общие); волна коммитится
  несколькими коммитами по файлам-темам, а не по срезам. Отклонение от
  «каждый slice — отдельный коммит» осознанное.
- **Найдено по дороге (t6 5/12 красный после S4): гонка в `IngressHold.Put`.**
  Два одновременных Collect'а (pull цикла и pull звонка, у каждого копия
  одного кадра от разных участников — мешевая избыточность) писали один и тот
  же content-addressed файл через один и тот же `<path>.tmp`; проигравший
  получал ENOENT на rename, что для узла неотличимо от мёртвого диска →
  защёлка «custody lost», сбор остановлен навсегда — узел тихо глохнет.
  S4 лишь участил звонки (квитанция = Put в ящик автора) и вытащил гонку
  на свет. Фикс: мьютекс на Put/Delete (kernel/storage), тест
  `TestConcurrentPutsOfTheSameBytesNeverFail` (красный на старом коде);
  плюс строка лога и первая причина в защёлке. Ещё одно правило S4:
  квитанции по приходу через реле — только при включённом реле
  (`relaySyncArmed`), иначе выключенный узел отвечал на LAN-приход Put'ами
  в чужие ящики (carol 0→1 в t6).
- **S2 под race-детектором (3/6):** «свой мир» для `routableFrom` считался через
  `ResolvePersonalRelay()` — он фильтрует по здоровью, и как только
  предохранитель набора срабатывал (реле недостижимо), ответ был `""` =
  «стенд» → loopback чужого попадал в книгу. Теперь `ownWorld()` =
  `PersonalRelayAddress()` (что узел НАЗВАЛ бы, без здоровья) во всех местах
  решения о мире: ingest, advertise, rankRoutes, guessRelays, siblingIngresses,
  routeFor. Цели набора (courtesyRoute fallback) по-прежнему по здоровью.
- **S1b — курьер истории (найдено на телефоне владельца, rc13, 2026-09-23 01:10):**
  первый проход после обновления шёл **6m32s** (`spaces=26 pushed=2245 express=56
  bulk=52`): книга предложений родилась пустой → каждому ящику полная история на
  bulk, а `pushSpacesVia` ждёт `pushWG.Wait()` КАЖДОГО пространства по очереди;
  слово владельца, сказанное во время прохода, ушло после него (~15 с на его
  часах). Плюс неудачный проход повторял все 26 пространств. Теперь: bulk-джобы
  при `delta` уходят **курьеру** (`r.courier`, горутина на эндпоинт, `r.wg`),
  проход не ждёт; заявка на ящик `bulkInFlight[offerKey]` — один ящик не
  предлагается дважды (и цикл, и outbox проверяют заявку); учёт принятия
  (`noteRelayAccepted`: transport receipt + Relayed-watermark + LT-1 timeline)
  вынесен и вызывается курьером сам; новый возврат `inflight` и hold
  `heldSendingHistory` («sending history to some members») — lastLen не
  двигается, следующий проход находит метку. Ручной verb `pushToRelay`
  (delta=false) по-прежнему ждёт bulk. **Курьер — только у outbox-прохода
  (viaOutbox):** первый вариант отдавал курьеру и bulk цикла — упал
  `TestMediaRidesAheadOfTheRequest` детерминированно: последний `relaySyncOnce`
  перед `Close()` — то, что уносит фото «вперёд запроса», когда телефон
  уходит в карман, а курьер после `r.stop` не шлёт. Цикл ждёт свой bulk как
  раньше, но под теми же заявками (ящик, который несёт курьер, цикл не
  предлагает второй раз; свои заявки цикл отпускает после `pushWG.Wait()`). Диагностика: `history_in_flight`,
  строка Outbox «history to N». Тест
  `TestAnotherSpacesHistoryDoesNotHoldTheWord`: bulk-линия удержана тестом,
  слово в соседней комнате — 213 мс. Честная цена обновления: один полный
  проход истории по всем ящикам в фоне (раньше это было КАЖДОЕ открытие).
